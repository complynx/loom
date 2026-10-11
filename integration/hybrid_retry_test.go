package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/runs"
	"github.com/complynx/clx-scripter/internal/store"
)

func TestHybridRetryDurableScheduleRestartAndSingleUsage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
			return
		}
		_, _ = w.Write(
			[]byte(
				`{"id":"verified","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`,
			),
		)
	}))
	defer server.Close()
	client, err := provider.NewResponses(server.URL, "secret", server.Client())
	require.NoError(t, err)
	opts := recoveryOptions("hybrid-worker")
	opts.RetryAttemptLimit = 6
	opts.RetryWindow = 5 * time.Minute
	opts.RetryShortLimit = 2
	opts.RetryShortBudget = 3 * time.Second
	run := f.createRun(t, runRequest("", "", "Retry safely"), "hybrid-retry")
	err = runs.New(f.repository, client, opts).Execute(context.Background(), run.ID)
	var deferred *runs.RetryDeferredError
	require.ErrorAs(t, err, &deferred)
	require.Greater(t, time.Until(deferred.Until), 25*time.Second)
	current := f.run(t, run.ID)
	require.Equal(t, domain.Queued, current.State)
	require.Empty(t, current.LeaseOwner)
	require.Zero(t, current.ReservedMicrocredits)
	require.Zero(t, current.SpentMicrocredits)
	retry, err := f.repository.ProviderRetry(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, retry.Attempts)
	// A fresh worker cannot dispatch before the stored provider deadline.
	err = runs.New(f.repository, client, opts).Execute(context.Background(), run.ID)
	require.ErrorAs(t, err, &deferred)
	require.EqualValues(t, 1, calls.Load())
	_, err = f.pool.Exec(
		context.Background(),
		`UPDATE provider_retries SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE run_id=$1`,
		run.ID,
	)
	require.NoError(t, err)
	require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
	require.EqualValues(t, 2, calls.Load())
	current = f.run(t, run.ID)
	require.Equal(t, domain.Completed, current.State)
	require.EqualValues(t, 15, current.SpentMicrocredits)
	require.Zero(t, current.ReservedMicrocredits)
	var exposure int
	require.NoError(
		t,
		f.pool.QueryRow(context.Background(), "SELECT count(*) FROM platform_provider_attempts WHERE run_id=$1", run.ID).
			Scan(&exposure),
	)
	require.Zero(t, exposure)
	retry, err = f.repository.ProviderRetry(context.Background(), run.ID)
	require.NoError(t, err)
	require.Zero(t, retry.Attempts, "verified settlement closes the outage episode")
}

func TestHybridRetryExhaustionAndCancelReleaseReservation(t *testing.T) {
	t.Parallel()
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "budget", true: "cancel"}[cancel], func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			run := f.createRun(t, runRequest("", "", "Bound retries"), "hybrid-budget")
			calls := 0
			client := retryClientFunc(func(ctx context.Context, _ provider.Request) (provider.Response, error) {
				calls++
				if cancel {
					_, err := f.pool.Exec(ctx, `UPDATE runs SET cancel_requested=true WHERE id=$1`, run.ID)
					require.NoError(t, err)
				}
				return provider.Response{}, &provider.Failure{
					Code:       "rate_limit_exceeded",
					Status:     429,
					RetryAfter: time.Minute,
				}
			})
			opts := recoveryOptions("bounded-worker")
			opts.RetryAttemptLimit = 1
			opts.RetryWindow = 5 * time.Minute
			require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
			current := f.run(t, run.ID)
			require.Equal(t, 1, calls)
			require.Zero(t, current.ReservedMicrocredits)
			require.Zero(t, current.SpentMicrocredits)
			if cancel {
				require.Equal(t, domain.Cancelled, current.State)
			} else {
				require.Equal(t, domain.Paused, current.State)
				require.Equal(t, "provider_retry_limit", current.Reason)
			}
		})
	}
}

type retryClientFunc func(context.Context, provider.Request) (provider.Response, error)

func (f retryClientFunc) Respond(ctx context.Context, req provider.Request) (provider.Response, error) {
	return f(ctx, req)
}

type compactionRetryClient struct {
	*activeCompactionClient

	rejections int
}

func (c *compactionRetryClient) Respond(ctx context.Context, request provider.Request) (provider.Response, error) {
	if strings.HasPrefix(request.Instructions, "Compact conversation") && c.reject() {
		return provider.Response{}, &provider.Failure{
			Code:       "rate_limit_exceeded",
			Status:     http.StatusTooManyRequests,
			RetryAfter: time.Minute,
		}
	}
	return c.activeCompactionClient.Respond(ctx, request)
}

func (c *compactionRetryClient) Compact(ctx context.Context, request provider.Request) (provider.Response, error) {
	if c.reject() {
		return provider.Response{}, &provider.Failure{
			Code:       "rate_limit_exceeded",
			Status:     http.StatusTooManyRequests,
			RetryAfter: time.Minute,
		}
	}
	return c.activeCompactionClient.Compact(ctx, request)
}

func (c *compactionRetryClient) reject() bool { c.rejections++; return c.rejections == 1 }

func TestHybridRetryCompactionPreservesToolCheckpoint(t *testing.T) {
	t.Parallel()
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary", true: "native"}[native], func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			configureActiveCompaction(t, f, native)
			client := &compactionRetryClient{
				activeCompactionClient: &activeCompactionClient{
					t:      t,
					native: native,
					initialCalls: []provider.Call{
						{ID: "read-order", Name: "script", Arguments: json.RawMessage(`{"code":"return readOrder()"}`)},
					},
				},
			}
			scripts := 0
			opts := recoveryOptions("hybrid-compact")
			opts.RetryAttemptLimit = 6
			opts.RetryWindow = 5 * time.Minute
			opts.Script = func(context.Context, domain.Run, domain.Principal, string, string) (json.RawMessage, error) {
				scripts++
				return json.Marshal(strings.Repeat(activeCompactionPayload, 500))
			}
			request := runRequest("", "", "Find order A-12; no booking before approval.")
			request.Budget.MaxMicrocredits = 100000
			run := f.createRun(t, request, "hybrid-compact")
			var deferred *runs.RetryDeferredError
			require.ErrorAs(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID), &deferred)
			require.Equal(t, 1, scripts)
			require.Zero(t, f.run(t, run.ID).ReservedMicrocredits)
			_, err := f.pool.Exec(
				context.Background(),
				`UPDATE provider_retries SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE run_id=$1`,
				run.ID,
			)
			require.NoError(t, err)
			require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
			require.Equal(t, domain.WaitingInput, f.run(t, run.ID).State)
			require.Equal(t, 1, scripts, "recovery must not dispatch a completed tool again")
			require.Len(t, client.compactRequests, 1, "only verified compaction contributes context")
			retry, err := f.repository.ProviderRetry(context.Background(), run.ID)
			require.NoError(t, err)
			require.Zero(t, retry.Attempts)
		})
	}
}

func TestHybridRetryResumeChecksDeadlineAndAuthorization(t *testing.T) {
	t.Parallel()
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "authorization", true: "deadline"}[expired], func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			calls := 0
			client := retryClientFunc(func(context.Context, provider.Request) (provider.Response, error) {
				calls++
				return provider.Response{}, &provider.Failure{
					Code:       "rate_limit_exceeded",
					Status:     http.StatusTooManyRequests,
					RetryAfter: time.Minute,
				}
			})
			opts := recoveryOptions("resume-guard")
			opts.RetryAttemptLimit = 6
			opts.RetryWindow = 5 * time.Minute
			run := f.createRun(t, runRequest("", "", "Guard retry resume"), "retry-guards")
			var deferred *runs.RetryDeferredError
			require.ErrorAs(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID), &deferred)
			_, err := f.pool.Exec(
				context.Background(),
				`UPDATE provider_retries SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE run_id=$1`,
				run.ID,
			)
			require.NoError(t, err)
			if expired {
				_, err = f.pool.Exec(
					context.Background(),
					`UPDATE provider_retries SET deadline=clock_timestamp()-interval '1 second' WHERE run_id=$1`,
					run.ID,
				)
				require.NoError(t, err)
			} else {
				opts.Authorize = func(context.Context, domain.Run) (domain.Principal, string, error) {
					return domain.Principal{}, "", domain.Forbidden()
				}
			}
			require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
			require.Equal(t, 1, calls)
			current := f.run(t, run.ID)
			if expired {
				require.Equal(t, domain.Paused, current.State)
				require.Equal(t, "provider_retry_limit", current.Reason)
			} else {
				require.Equal(t, domain.WaitingAuth, current.State)
			}
			require.Zero(t, current.ReservedMicrocredits)
		})
	}
}

func TestHybridRetryAcceptedRequestKeepsInferenceDeadline(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
			return
		}
		timer := time.NewTimer(4 * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		_, _ = w.Write(
			[]byte(
				`{"id":"slow-verified","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`,
			),
		)
	}))
	defer server.Close()
	client, err := provider.NewResponses(server.URL, "secret", server.Client())
	require.NoError(t, err)
	opts := recoveryOptions("slow-retry")
	opts.RetryAttemptLimit = 6
	opts.RetryWindow = 5 * time.Minute
	opts.RetryShortLimit = 2
	opts.RetryShortBudget = 3 * time.Second
	opts.InferenceTimeout = 10 * time.Second
	run := f.createRun(t, runRequest("", "", "Allow accepted inference to finish"), "slow-retry")
	require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
	require.EqualValues(t, 2, calls.Load())
	result := f.run(t, run.ID)
	require.Equal(t, domain.Completed, result.State)
	require.EqualValues(t, 15, result.SpentMicrocredits)
	require.Zero(t, result.ReservedMicrocredits)
	var exposure int
	require.NoError(
		t,
		f.pool.QueryRow(context.Background(), "SELECT count(*) FROM platform_provider_attempts WHERE run_id=$1", run.ID).
			Scan(&exposure),
	)
	require.Zero(t, exposure)
}

func TestHybridRetryExplicitResumeRestartsExhaustedEpisodeWhenDue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	healthy := false
	calls := 0
	client := retryClientFunc(func(context.Context, provider.Request) (provider.Response, error) {
		calls++
		if !healthy {
			return provider.Response{}, &provider.Failure{
				Code:       "rate_limit_exceeded",
				Status:     http.StatusTooManyRequests,
				RetryAfter: time.Minute,
			}
		}
		return provider.Response{
			ID:     "healthy",
			Text:   "done",
			Output: []provider.Item{provider.Message("assistant", "done")},
			Usage:  provider.Usage{InputTokens: 10, OutputTokens: 5},
		}, nil
	})
	opts := recoveryOptions("explicit-resume")
	opts.RetryAttemptLimit = 1
	opts.RetryWindow = 5 * time.Minute
	run := f.createRun(t, runRequest("", "", "Resume after recovery"), "explicit-resume")
	require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
	require.Equal(t, "provider_retry_limit", f.run(t, run.ID).Reason)
	code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "too-early", nil)
	require.Equal(t, http.StatusConflict, code, string(raw))
	require.Contains(t, string(raw), "retry_not_due")
	require.Equal(t, domain.Paused, f.run(t, run.ID).State)
	require.Equal(t, 1, calls)
	retry, err := f.repository.ProviderRetry(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, retry.Attempts)
	_, err = f.pool.Exec(
		context.Background(),
		`UPDATE provider_retries SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE run_id=$1`,
		run.ID,
	)
	require.NoError(t, err)
	healthy = true
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "due-resume", nil)
	require.Equal(t, http.StatusOK, code, string(raw))
	retry, err = f.repository.ProviderRetry(context.Background(), run.ID)
	require.NoError(t, err)
	require.Zero(t, retry.Attempts)
	require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
	current := f.run(t, run.ID)
	require.Equal(t, domain.Completed, current.State)
	require.Equal(t, 2, calls)
	require.EqualValues(t, 15, current.SpentMicrocredits)
}

func TestHybridRetryExpiryDuringPreparationPreventsDispatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	calls := 0
	client := retryClientFunc(func(context.Context, provider.Request) (provider.Response, error) {
		calls++
		return provider.Response{}, &provider.Failure{
			Code:       "rate_limit_exceeded",
			Status:     http.StatusTooManyRequests,
			RetryAfter: time.Minute,
		}
	})
	opts := recoveryOptions("preparation-expiry")
	opts.RetryAttemptLimit = 6
	opts.RetryWindow = 5 * time.Minute
	run := f.createRun(t, runRequest("", "", "Check dispatch boundary"), "preparation-expiry")
	var deferred *runs.RetryDeferredError
	require.ErrorAs(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID), &deferred)
	_, err := f.pool.Exec(
		context.Background(),
		`UPDATE provider_retries SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE run_id=$1`,
		run.ID,
	)
	require.NoError(t, err)
	opts.Tools = func(ctx context.Context, _ domain.Run, _ domain.Principal, _ string) ([]provider.Tool, error) {
		_, updateErr := f.pool.Exec(
			ctx,
			`UPDATE provider_retries SET deadline=clock_timestamp()-interval '1 second' WHERE run_id=$1`,
			run.ID,
		)
		return nil, updateErr
	}
	require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
	require.Equal(t, 1, calls, "expired preparation must not dispatch another request")
	current := f.run(t, run.ID)
	require.Equal(t, domain.Paused, current.State)
	require.Equal(t, "provider_retry_limit", current.Reason)
	require.Zero(t, current.ReservedMicrocredits)
	require.Zero(t, current.SpentMicrocredits)
}

func TestHybridRetryInterruptedWaitPreservesKnownRejection(t *testing.T) {
	t.Parallel()
	for _, cancelRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "pause", true: "cancel"}[cancelRun], func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			var calls atomic.Int32
			rejected := make(chan struct{})
			client := retryClientFunc(func(context.Context, provider.Request) (provider.Response, error) {
				if calls.Add(1) == 1 {
					close(rejected)
				}
				return provider.Response{}, &provider.Failure{
					Code:       "rate_limit_exceeded",
					Status:     http.StatusTooManyRequests,
					RetryAfter: 2 * time.Second,
				}
			})
			opts := recoveryOptions("interrupt-retry-wait")
			opts.RetryAttemptLimit = 6
			opts.RetryWindow = 5 * time.Minute
			opts.RetryShortLimit = 2
			opts.RetryShortBudget = 3 * time.Second
			run := f.createRun(t, runRequest("", "", "Preserve rejected request timing"), "interrupt-retry-wait")
			done := make(chan error, 1)
			go func() { done <- runs.New(f.repository, client, opts).Execute(context.Background(), run.ID) }()
			select {
			case <-rejected:
			case <-time.After(5 * time.Second):
				t.Fatal("provider was not called")
			}
			action := "pause"
			if cancelRun {
				action = "cancel"
			}
			code, raw := f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/"+action, "interrupt", nil)
			require.Equal(t, http.StatusOK, code, string(raw))
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("interruption was not checkpointed")
			}
			current := f.run(t, run.ID)
			retry, err := f.repository.ProviderRetry(context.Background(), run.ID)
			require.NoError(t, err)
			require.Equal(t, 1, retry.Attempts)
			require.True(t, retry.NextAttemptAt.After(time.Now()), "known provider wait survives interruption")
			require.Zero(t, current.SpentMicrocredits)
			require.Zero(t, current.ReservedMicrocredits)
			var exposure int
			require.NoError(
				t,
				f.pool.QueryRow(context.Background(), "SELECT count(*) FROM platform_provider_attempts WHERE run_id=$1", run.ID).
					Scan(&exposure),
			)
			require.Zero(t, exposure)
			if cancelRun {
				require.Equal(t, domain.Cancelled, current.State)
				require.Equal(t, "client_cancel", current.Reason)
			} else {
				require.Equal(t, domain.Paused, current.State)
				code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "resume-interrupted", nil)
				require.Equal(t, http.StatusConflict, code, string(raw))
				require.Contains(t, string(raw), "retry_not_due")
				// A provider wait beyond the episode deadline still gates resume.
				_, err = f.pool.Exec(
					context.Background(),
					`UPDATE provider_retries SET next_attempt_at=deadline+interval '1 second' WHERE run_id=$1`,
					run.ID,
				)
				require.NoError(t, err)
				code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "resume-beyond-window", nil)
				require.Equal(t, http.StatusConflict, code, string(raw))
				require.Contains(t, string(raw), "retry_not_due")
				require.Equal(t, domain.Paused, f.run(t, run.ID).State)
			}
			require.EqualValues(t, 1, calls.Load(), "interruption must not repeat the rejected request")
		})
	}
}

type staleRetryPrecheckStore struct {
	*store.Store

	read     atomic.Bool
	observed chan struct{}
	release  chan struct{}
}

func (s *staleRetryPrecheckStore) ProviderRetry(ctx context.Context, id string) (domain.ProviderRetry, error) {
	retry, err := s.Store.ProviderRetry(ctx, id)
	if !s.read.Swap(true) {
		close(s.observed)
		select {
		case <-s.release:
		case <-ctx.Done():
			return retry, ctx.Err()
		}
	}
	return retry, err
}

func TestHybridRetryAtomicClaimPreventsStalePrecheckDispatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	arrived := make(chan struct{})
	reject := make(chan struct{})
	clientA := retryClientFunc(func(ctx context.Context, _ provider.Request) (provider.Response, error) {
		close(arrived)
		select {
		case <-reject:
		case <-ctx.Done():
			return provider.Response{}, ctx.Err()
		}
		return provider.Response{}, &provider.Failure{
			Code:       "rate_limit_exceeded",
			Status:     http.StatusTooManyRequests,
			RetryAfter: time.Minute,
		}
	})
	var callsB atomic.Int32
	clientB := retryClientFunc(func(context.Context, provider.Request) (provider.Response, error) {
		callsB.Add(1)
		return provider.Response{}, nil
	})
	optsA := recoveryOptions("claim-worker-a")
	optsA.RetryAttemptLimit = 6
	optsA.RetryWindow = 5 * time.Minute
	run := f.createRun(t, runRequest("", "", "Serialize retry admission"), "stale-retry-precheck")
	doneA := make(chan error, 1)
	go func() { doneA <- runs.New(f.repository, clientA, optsA).Execute(context.Background(), run.ID) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("first worker did not dispatch")
	}
	stale := &staleRetryPrecheckStore{Store: f.repository, observed: make(chan struct{}), release: make(chan struct{})}
	optsB := optsA
	optsB.Owner = "claim-worker-b"
	doneB := make(chan error, 1)
	go func() { doneB <- runs.New(stale, clientB, optsB).Execute(context.Background(), run.ID) }()
	select {
	case <-stale.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("second worker did not read pre-rejection schedule")
	}
	close(reject)
	var deferred *runs.RetryDeferredError
	select {
	case err := <-doneA:
		require.ErrorAs(t, err, &deferred)
	case <-time.After(5 * time.Second):
		t.Fatal("first rejection was not saved")
	}
	queued := f.run(t, run.ID)
	require.Equal(t, domain.Queued, queued.State)
	close(stale.release)
	select {
	case err := <-doneB:
		require.ErrorAs(t, err, &deferred)
	case <-time.After(5 * time.Second):
		t.Fatal("second worker was not deferred")
	}
	require.Zero(t, callsB.Load(), "an old precheck must not bypass the committed schedule")
	current := f.run(t, run.ID)
	require.Equal(t, domain.Queued, current.State)
	require.Empty(t, current.LeaseOwner)
	require.Nil(t, current.LeaseUntil)
	require.Equal(t, queued.Fence, current.Fence, "a waiting delivery must not acquire a new fence")
	require.Zero(t, current.ReservedMicrocredits)
	require.Zero(t, current.SpentMicrocredits)
}
