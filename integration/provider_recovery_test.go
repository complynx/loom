package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/identity"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
)

type recoveringProvider struct {
	failures int
	requests []provider.Request
}

func (c *recoveringProvider) Respond(_ context.Context, request provider.Request) (provider.Response, error) {
	c.requests = append(c.requests, request)
	if len(c.requests) <= c.failures {
		return provider.Response{
			Calls: []provider.Call{
				{ID: "unverified-effect", Name: "script", Arguments: json.RawMessage(`{"code":"return mutate()"}`)},
			},
		}, &provider.Failure{
			Code:         "transport",
			UnknownUsage: true,
		}
	}
	return provider.Response{
		ID:     "verified-recovery",
		Text:   "Recovered answer",
		Output: []provider.Item{provider.Message("assistant", "Recovered answer")},
		Usage:  provider.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func recoveryPrincipal() domain.Principal {
	return domain.Principal{
		Issuer:      "https://test.identity",
		Subject:     "alice",
		Actor:       "alice",
		OrgID:       "test-org",
		ExpiresAt:   time.Now().Add(time.Hour),
		Permissions: map[string]bool{"runs.create": true, "chat.read": true},
	}
}

func recoveryOptions(owner string) runs.Options {
	return runs.Options{
		Owner:                 owner,
		SponsoredAttemptLimit: 2,
		Authorize: func(context.Context, domain.Run) (domain.Principal, string, error) {
			return recoveryPrincipal(), "user-token", nil
		},
	}
}

func TestSponsoredNetworkRecoveryChargesOnlyVerifiedSuccess(t *testing.T) {
	t.Parallel()
	for _, failures := range []int{1, 2} {
		t.Run(fmt.Sprintf("lost-replies-%d", failures), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			client := &recoveringProvider{failures: failures}
			opts := recoveryOptions("network-recovery-worker")
			opts.Script = func(context.Context, domain.Run, domain.Principal, string, string) (json.RawMessage, error) {
				t.Error("unverified response dispatched a tool")
				return json.RawMessage(`{}`), nil
			}
			run := f.createRun(t, runRequest("", "", "Recover safely"), "network-recovery")
			require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
			result := f.run(t, run.ID)
			require.Len(t, client.requests, 2)
			require.NotEmpty(t, client.requests[0].ClientRequestID)
			require.NotEqual(t, client.requests[0].ClientRequestID, client.requests[1].ClientRequestID)
			if failures == 1 {
				require.Equal(t, domain.Completed, result.State)
				require.EqualValues(t, 15, result.SpentMicrocredits)
			} else {
				require.Equal(t, domain.Paused, result.State)
				require.Equal(t, "platform_retry_limit", result.Reason)
				require.Zero(t, result.SpentMicrocredits)
			}
			require.Zero(t, result.ReservedMicrocredits)
			balance, err := f.repository.Balance(context.Background(), recoveryPrincipal())
			require.NoError(t, err)
			require.Equal(t, result.SpentMicrocredits, balance.SpentMicrocredits)
			require.Equal(t, 100000-result.SpentMicrocredits, balance.AvailableMicrocredits)
			require.Zero(t, balance.ReservedMicrocredits)
			var attempts, uncertain int
			var exposure int64
			require.NoError(
				t,
				f.pool.QueryRow(context.Background(), "SELECT count(*),count(*) FILTER(WHERE status='unknown'),coalesce(sum(reserved_exposure),0) FROM platform_provider_attempts WHERE run_id=$1", run.ID).
					Scan(&attempts, &uncertain, &exposure),
			)
			require.Equal(t, failures, attempts)
			require.Equal(t, attempts, uncertain)
			require.Positive(t, exposure)
		})
	}
}

func TestSponsoredRestartRequiresFreshAuthorizationBeforeRecovery(t *testing.T) {
	t.Parallel()
	for _, authorized := range []bool{true, false} {
		t.Run(fmt.Sprintf("authorized-%t", authorized), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ctx := context.Background()
			run := f.createRun(t, runRequest("", "", "Continue after restart"), "restart-recovery")
			claimed, err := f.repository.ClaimRun(ctx, run.ID, "dead-worker", time.Minute)
			require.NoError(t, err)
			step, err := f.repository.BeginStep(
				ctx,
				run.ID,
				"dead-worker",
				claimed.Fence,
				"model",
				json.RawMessage(`{}`),
				200,
			)
			require.NoError(t, err)
			_, err = f.pool.Exec(
				ctx,
				"UPDATE runs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1",
				run.ID,
			)
			require.NoError(t, err)
			client := &recoveringProvider{}
			opts := recoveryOptions("fresh-recovery-worker")
			authorizations := 0
			opts.Authorize = func(context.Context, domain.Run) (domain.Principal, string, error) {
				authorizations++
				if !authorized {
					return domain.Principal{}, "", identity.ErrUnauthorized
				}
				return recoveryPrincipal(), "fresh-token", nil
			}
			executeErr := runs.New(f.repository, client, opts).Execute(ctx, run.ID)
			require.Positive(t, authorizations)
			balance, err := f.repository.Balance(ctx, recoveryPrincipal())
			require.NoError(t, err)
			var attempts int
			require.NoError(
				t,
				f.pool.QueryRow(ctx, "SELECT count(*) FROM platform_provider_attempts WHERE step_id=$1", step.ID).
					Scan(&attempts),
			)
			if !authorized {
				require.Empty(t, client.requests)
				require.Zero(t, attempts)
				require.Zero(t, balance.SpentMicrocredits)
				require.EqualValues(t, 200, balance.ReservedMicrocredits)
				return
			}
			require.NoError(t, executeErr)
			require.Len(t, client.requests, 1)
			require.Equal(t, 1, attempts)
			require.Equal(t, domain.Completed, f.run(t, run.ID).State)
			require.EqualValues(t, 15, balance.SpentMicrocredits)
			require.Zero(t, balance.ReservedMicrocredits)
			require.EqualValues(t, 99985, balance.AvailableMicrocredits)
		})
	}
}

type recoveringCompactProvider struct {
	*activeCompactionClient

	compactAttempts int
}

func (c *recoveringCompactProvider) Compact(ctx context.Context, request provider.Request) (provider.Response, error) {
	c.compactAttempts++
	if c.compactAttempts == 1 {
		return provider.Response{}, &provider.Failure{Code: "transport", UnknownUsage: true}
	}
	return c.activeCompactionClient.Compact(ctx, request)
}

func TestSponsoredCompactionRecoveryDoesNotRepeatTools(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	configureActiveCompaction(t, f, true)
	client := &recoveringCompactProvider{
		activeCompactionClient: &activeCompactionClient{
			t:      t,
			native: true,
			initialCalls: []provider.Call{
				{ID: "read-order", Name: "script", Arguments: json.RawMessage(`{"code":"return readOrder()"}`)},
			},
		},
	}
	opts := recoveryOptions("compact-recovery-worker")
	scriptCalls := 0
	opts.Script = func(context.Context, domain.Run, domain.Principal, string, string) (json.RawMessage, error) {
		scriptCalls++
		return json.Marshal(strings.Repeat(activeCompactionPayload, 500))
	}
	input := runRequest("", "", "Find order A-12; no booking before approval.")
	input.Budget.MaxMicrocredits = 100000
	run := f.createRun(t, input, "compact-recovery")
	require.NoError(t, runs.New(f.repository, client, opts).Execute(context.Background(), run.ID))
	result := f.run(t, run.ID)
	require.Equal(t, domain.WaitingInput, result.State, result.Reason)
	require.Equal(t, 1, scriptCalls)
	require.Equal(t, 2, client.compactAttempts)
	require.Len(t, client.compactRequests, 1)
	require.Len(t, client.requests, 2)
	raw, err := json.Marshal(client.requests[1].Input)
	require.NoError(t, err)
	require.Contains(t, string(raw), "opaque-order-state")
	require.NotContains(t, string(raw), activeCompactionPayload)
	require.EqualValues(t, 7250, result.SpentMicrocredits)
	require.Zero(t, result.ReservedMicrocredits)
	var exposure int64
	var attempts int
	require.NoError(
		t,
		f.pool.QueryRow(context.Background(), "SELECT count(*),coalesce(sum(reserved_exposure),0) FROM platform_provider_attempts WHERE run_id=$1", run.ID).
			Scan(&attempts, &exposure),
	)
	require.Equal(t, 1, attempts)
	require.Positive(t, exposure)
}
