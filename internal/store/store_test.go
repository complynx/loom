package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("CLX_TEST_DATABASE_DSN is required for PostgreSQL behavior tests")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	require.NoError(t, e)
	schema := "store_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, e = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, e)
	u, e := url.Parse(dsn)
	require.NoError(t, e)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn = u.String()
	require.NoError(t, store.Migrate(ctx, dsn))
	poolConfig, e := pgxpool.ParseConfig(dsn)
	require.NoError(t, e)
	poolConfig.ConnConfig.RuntimeParams["lock_timeout"] = "500ms"
	pool, e := pgxpool.NewWithConfig(ctx, poolConfig)
	require.NoError(t, e)
	t.Cleanup(func() {
		pool.Close()
		_, cleanupErr := admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		if cleanupErr != nil {
			t.Error(cleanupErr)
		}
		admin.Close()
	})
	s := store.New(pool, nil)
	require.NoError(
		t,
		s.Bootstrap(
			ctx,
			domain.Settings{
				Models: []domain.Model{
					{
						ID:                           "test",
						ProviderModel:                "test",
						Efforts:                      []string{"low", "high"},
						MaxEffort:                    "high",
						MaxOutputTokens:              100,
						InputMicrocreditsPerMillion:  1000000,
						OutputMicrocreditsPerMillion: 1000000,
					},
				},
				DefaultModelID:         "test",
				DefaultEffort:          "low",
				InitialCredits:         10000,
				MaxRunMicrocredits:     1000,
				ActiveRunLimit:         8,
				HistoryRecentLimit:     50,
				MaxSteps:               10,
				QuestionTimeoutSeconds: 60,
			},
		),
	)
	return s
}
func principal(subject string) domain.Principal {
	return domain.Principal{
		Issuer:    "https://issuer.test",
		Subject:   subject,
		Actor:     subject,
		OrgID:     "org",
		ExpiresAt: time.Now().Add(time.Hour),
		Permissions: map[string]bool{
			"runs.create":             true,
			"runs.control":            true,
			"chat.read":               true,
			"chat.events.append":      true,
			"preferences.self.write":  true,
			"preferences.other.write": true,
			"users.policy.write":      true,
			"settings.write":          true,
			"billing.adjust":          true,
		},
	}
}
func request() domain.CreateRun {
	return domain.CreateRun{
		Input:       domain.Input{Text: "hello"},
		Interaction: domain.Interaction{Questions: "choices_and_text", Approvals: true},
		Budget:      domain.Budget{MaxMicrocredits: 1000},
	}
}
func code(t *testing.T, e error, c string) {
	t.Helper()
	var de *domain.Error
	require.ErrorAs(t, e, &de)
	require.Equal(t, c, de.Code)
}
func TestChatRunConcurrencyAndDurableReplay(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	c, e := s.DefaultChat(ctx, p)
	require.NoError(t, e)
	again, e := s.DefaultChat(ctx, p)
	require.NoError(t, e)
	require.Equal(t, c.ID, again.ID)
	r, e := s.CreateRun(ctx, p, "first", request())
	require.NoError(t, e)
	replay, e := store.New(s.Pool(), nil).CreateRun(ctx, p, "first", request())
	require.NoError(t, e)
	require.Equal(t, r.ID, replay.ID)
	changed := request()
	changed.Input.Text = "other"
	_, e = s.CreateRun(ctx, p, "first", changed)
	code(t, e, "idempotency_conflict")
	_, e = s.CreateRun(ctx, p, "second", request())
	code(t, e, "chat_busy")
	c2, e := s.CreateChat(ctx, p)
	require.NoError(t, e)
	in := request()
	in.ChatID = c2.ID
	other, e := s.CreateRun(ctx, p, "other", in)
	require.NoError(t, e)
	require.NotEqual(t, r.ID, other.ID)
	_, e = s.GetRun(ctx, principal("bob"), r.ID)
	code(t, e, "not_found")
	c3, e := s.CreateChat(ctx, p)
	require.NoError(t, e)
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	for _, key := range []string{"race1", "race2"} {
		go func(key string) {
			defer wg.Done()
			concurrentInput := request()
			concurrentInput.ChatID = c3.ID
			_, createErr := s.CreateRun(ctx, p, key, concurrentInput)
			results <- createErr
		}(key)
	}
	wg.Wait()
	close(results)
	success := 0
	busy := 0
	for e := range results {
		if e == nil {
			success++
		} else {
			var de *domain.Error
			require.ErrorAs(t, e, &de)
			if de.Code == "chat_busy" {
				busy++
			}
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, busy)
}
func TestPassiveHistoryAndRegex(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	c, e := s.DefaultChat(ctx, p)
	require.NoError(t, e)
	in := domain.AppendEvent{SourceEventID: "selected", Type: "user.action", Text: "Selected Андрей"}
	a, e := s.AppendEvent(ctx, p, c.ID, in)
	require.NoError(t, e)
	b, e := s.AppendEvent(ctx, p, c.ID, in)
	require.NoError(t, e)
	require.Equal(t, a.Seq, b.Seq)
	in.Text = "changed"
	_, e = s.AppendEvent(ctx, p, c.ID, in)
	code(t, e, "event_conflict")
	history, e := s.History(ctx, p, c.ID, 0, 100)
	require.NoError(t, e)
	require.Len(t, history, 1)
	results, e := s.Search(ctx, p, c.ID, domain.SearchRequest{Mode: "regex", Query: "Андре[йя]", Limit: 10})
	require.NoError(t, e)
	require.Len(t, results.Events, 1)
	_, e = s.Search(ctx, p, c.ID, domain.SearchRequest{Mode: "regex", Query: "(?=bad)"})
	code(t, e, "invalid_pattern")
	run, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	require.Equal(t, domain.Queued, run.State)
}
func TestCreditsSettleOnceAndUnknownRecovery(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{"prompt":"hello"}`), 500)
	require.NoError(t, e)
	balance, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.EqualValues(t, 500, balance.ReservedMicrocredits)
	usage := domain.Usage{InputTokens: 2, OutputTokens: 3, Microcredits: 5}
	require.NoError(t, s.CompleteStep(ctx, r.ID, "worker", r.Fence, step.ID, json.RawMessage(`{"text":"done"}`), usage))
	require.NoError(t, s.CompleteStep(ctx, r.ID, "worker", r.Fence, step.ID, json.RawMessage(`{"text":"done"}`), usage))
	balance, e = s.Balance(ctx, p)
	require.NoError(t, e)
	require.EqualValues(t, 9995, balance.AvailableMicrocredits)
	require.EqualValues(t, 5, balance.SpentMicrocredits)
	require.Zero(t, balance.ReservedMicrocredits)
	step, e = s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 300)
	require.NoError(t, e)
	require.NoError(t, s.MarkStepUnknown(ctx, r.ID, "worker", r.Fence, step.ID))
	durable, e := store.New(s.Pool(), nil).ExecutionRun(ctx, r.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Reconciling, durable.State)
	balance, e = s.Balance(ctx, p)
	require.NoError(t, e)
	require.EqualValues(t, 300, balance.ReservedMicrocredits)
	_, e = s.ClaimRun(ctx, r.ID, "replacement", time.Minute)
	code(t, e, "run_not_runnable")
	e = s.CompleteStep(ctx, r.ID, "worker", r.Fence, step.ID, json.RawMessage(`{}`), domain.Usage{})
	code(t, e, "lease_lost")
}
func TestQuestionsAndControl(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	require.NoError(
		t,
		s.AskQuestion(
			ctx,
			r.ID,
			"worker",
			r.Fence,
			domain.Question{
				ID:        "which",
				Text:      "Choose",
				Choices:   []domain.Choice{{ID: "a", Label: "A"}},
				AllowText: false,
				ExpiresAt: time.Now().Add(time.Minute),
			},
		),
	)
	r, e = s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	require.Equal(t, domain.WaitingInput, r.State)
	_, e = s.AddInput(
		ctx,
		p,
		r.ID,
		"bad",
		domain.RunInput{Kind: "answer", QuestionID: "which", ChoiceIDs: []string{"invalid"}},
	)
	code(t, e, "invalid_choice")
	input := domain.RunInput{Kind: "answer", QuestionID: "which", ChoiceIDs: []string{"a"}}
	r, e = s.AddInput(ctx, p, r.ID, "answer", input)
	require.NoError(t, e)
	require.Equal(t, domain.Queued, r.State)
	_, e = s.AddInput(ctx, p, r.ID, "answer", input)
	require.NoError(t, e)
	r, e = s.Control(ctx, p, r.ID, "pause", "pause")
	require.NoError(t, e)
	require.Equal(t, domain.Paused, r.State)
	_, e = s.CreateRun(ctx, p, "new", request())
	code(t, e, "chat_busy")
	r, e = s.Control(ctx, p, r.ID, "cancel", "cancel")
	require.NoError(t, e)
	require.Equal(t, domain.Cancelled, r.State)
	_, e = s.CreateRun(ctx, p, "new", request())
	require.NoError(t, e)
}
func TestEncryptedSessionBindingAndIdempotentBootstrap(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	session, e := s.CreateAuthSession(ctx, p, json.RawMessage(`{"ciphertext":"opaque"}`), time.Now().Add(time.Hour))
	require.NoError(t, e)
	in := request()
	in.AuthSessionID = session.ID
	in.AutoAuthSession = true
	r, e := s.CreateRun(ctx, p, "run", in)
	require.NoError(t, e)
	replacement, e := s.CreateAuthSession(ctx, p, json.RawMessage(`{"ciphertext":"other"}`), time.Now().Add(time.Hour))
	require.NoError(t, e)
	in.AuthSessionID = replacement.ID
	replay, e := s.CreateRun(ctx, p, "run", in)
	require.NoError(t, e)
	require.Equal(t, r.AuthSessionID, replay.AuthSessionID)
	_, e = s.LoadAuthSession(ctx, principal("bob"), session.ID)
	require.ErrorIs(t, e, domain.ErrNotFound)
	require.NoError(t, s.RevokeAuthSession(ctx, p, session.ID))
	_, e = s.ReauthorizeAuthSession(ctx, p, session.ID, json.RawMessage(`{}`), time.Now().Add(time.Hour), 1)
	code(t, e, "session_version_conflict")
}

func TestPreferencesPolicyAndAuditCredits(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	uid, e := s.ResolveUser(ctx, p)
	require.NoError(t, e)
	prefs, e := s.Preferences(ctx, p)
	require.NoError(t, e)
	prefs.Effort = "high"
	prefs, e = s.UpdatePreferences(ctx, p, "", prefs.Version, prefs)
	require.NoError(t, e)
	_, e = s.UpdatePreferences(ctx, p, "", 1, prefs)
	code(t, e, "version_conflict")
	policy, e := s.Policy(ctx, p, uid)
	require.NoError(t, e)
	policy.Models[0].MaxEffort = "low"
	policy, e = s.UpdatePolicy(ctx, p, uid, 0, policy)
	require.NoError(t, e)
	_, e = s.CreateRun(ctx, p, "denied-effort", request())
	code(t, e, "forbidden")
	prefs.Effort = "low"
	_, e = s.UpdatePreferences(ctx, p, "", prefs.Version, prefs)
	require.NoError(t, e)
	r, e := s.CreateRun(ctx, p, "allowed", request())
	require.NoError(t, e)
	require.NoError(t, s.CheckRunPolicy(ctx, r.ID))
	policy.Models = []domain.Model{}
	_, e = s.UpdatePolicy(ctx, p, uid, policy.Version, policy)
	require.NoError(t, e)
	code(t, s.CheckRunPolicy(ctx, r.ID), "forbidden")
	b, e := s.AdjustCredits(ctx, p, uid, "grant", 50, "test grant")
	require.NoError(t, e)
	require.EqualValues(t, 10050, b.AvailableMicrocredits)
	b, e = s.AdjustCredits(ctx, p, uid, "grant", 50, "test grant")
	require.NoError(t, e)
	require.EqualValues(t, 10050, b.AvailableMicrocredits)
	_, e = s.AdjustCredits(ctx, p, uid, "grant", 51, "test grant")
	code(t, e, "idempotency_conflict")
}
func TestArtifactQuotaAtomic(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	uid, e := s.ResolveUser(ctx, p)
	require.NoError(t, e)
	var wg sync.WaitGroup
	wg.Add(2)
	result := make(chan error, 2)
	for range 2 {
		go func() {
			defer wg.Done()
			result <- s.CommitArtifact(ctx, domain.Artifact{ID: uuid.NewString(), OwnerID: uid, OrgID: "org", MIME: "image/png", SHA256: strings.Repeat("a", 64), Size: 60}, 100)
		}()
	}
	wg.Wait()
	close(result)
	success := 0
	rejected := 0
	for e := range result {
		if e == nil {
			success++
		} else {
			var de *domain.Error
			require.ErrorAs(t, e, &de)
			if de.Code == "artifact_quota" {
				rejected++
			}
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, rejected)
}
func TestSummaryDurabilityAndMonotonicity(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	c, e := s.DefaultChat(ctx, p)
	require.NoError(t, e)
	_, e = s.AppendEvent(
		ctx,
		p,
		c.ID,
		domain.AppendEvent{SourceEventID: "constraint", Type: "user.action", Text: "Keep a low budget"},
	)
	require.NoError(t, e)
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	summary := domain.ContextSummary{
		ChatID:     c.ID,
		ThroughSeq: 1,
		Text:       "User requires a low budget",
		SourceHash: strings.Repeat("a", 64),
		ModelID:    "test",
	}
	require.NoError(t, s.SaveSummary(ctx, r.ID, "worker", r.Fence, summary))
	got, e := store.New(s.Pool(), nil).ExecutionSummary(ctx, r.ID)
	require.NoError(t, e)
	require.Equal(t, summary, got)
	old := summary
	old.Text = "wrong overwrite"
	require.NoError(t, s.SaveSummary(ctx, r.ID, "worker", r.Fence, old))
	got, e = s.ExecutionSummary(ctx, r.ID)
	require.NoError(t, e)
	require.Equal(t, summary, got)
	events, e := s.ExecutionHistoryAfter(ctx, r.ID, 1, 100)
	require.NoError(t, e)
	require.Len(t, events, 1)
	require.Equal(t, "hello", events[0].Text)
}
func TestApprovalDecisionAndWakeAtomic(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	approval := domain.Approval{
		ID:          "approval",
		OperationID: uuid.NewString(),
		Text:        "Perform action",
		ExpiresAt:   time.Now().Add(time.Minute),
	}
	require.NoError(t, s.AskApproval(ctx, r.ID, "worker", r.Fence, approval))
	s.SetApprovalHandler(func(context.Context, pgx.Tx, domain.Run, domain.RunInput) error {
		return errors.New("operation rejection")
	})
	in := domain.RunInput{Kind: "approval", ApprovalID: "approval", Decision: "approve"}
	_, e = s.AddInput(ctx, p, r.ID, "decision", in)
	require.Error(t, e)
	got, e := s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	require.Equal(t, domain.WaitingApproval, got.State)
	require.Empty(t, got.Approval.Decision)
	calls := 0
	s.SetApprovalHandler(func(context.Context, pgx.Tx, domain.Run, domain.RunInput) error { calls++; return nil })
	got, e = s.AddInput(ctx, p, r.ID, "decision", in)
	require.NoError(t, e)
	require.Equal(t, domain.Queued, got.State)
	require.Equal(t, "approve", got.Approval.Decision)
	_, e = s.AddInput(ctx, p, r.ID, "decision", in)
	require.NoError(t, e)
	require.Equal(t, 1, calls)
}

func TestFulltextWithOptionalFilters(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	c, e := s.DefaultChat(ctx, p)
	require.NoError(t, e)
	_, e = s.AppendEvent(
		ctx,
		p,
		c.ID,
		domain.AppendEvent{SourceEventID: "menu", Type: "user.action", Text: "Opened massage orders menu"},
	)
	require.NoError(t, e)
	for _, types := range [][]string{nil, {"user.action"}} {
		out, searchErr := s.Search(
			ctx,
			p,
			c.ID,
			domain.SearchRequest{Mode: "fulltext", Query: "massage", EventTypes: types},
		)
		require.NoError(t, searchErr)
		require.Len(t, out.Events, 1)
	}
	out, e := s.Search(
		ctx,
		p,
		c.ID,
		domain.SearchRequest{Mode: "fulltext", Query: "massage", EventTypes: []string{"message.user"}},
	)
	require.NoError(t, e)
	require.Empty(t, out.Events)
}

func TestSettlementJSONReplayAndFreePendingStep(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 100)
	require.NoError(t, e)
	usage := domain.Usage{Microcredits: 10}
	require.NoError(
		t,
		s.CompleteStep(ctx, r.ID, "worker", r.Fence, step.ID, json.RawMessage(`{"b":1.00,"a":2}`), usage),
	)
	require.NoError(t, s.CompleteStep(ctx, r.ID, "worker", r.Fence, step.ID, json.RawMessage(`{"a":2,"b":1}`), usage))
	b, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.EqualValues(t, 10, b.SpentMicrocredits)
	free, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 0)
	require.NoError(t, e)
	code(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Completed, "", "done"), "unsettled_usage")
	require.NoError(t, s.CompleteStep(ctx, r.ID, "worker", r.Fence, free.ID, json.RawMessage(`{}`), domain.Usage{}))
	require.NoError(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Completed, "", "done"))
}

func TestCancellationRetainsUncertainReserveAndSafeReconcileResume(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 100)
	require.NoError(t, e)
	_, e = s.Control(ctx, p, r.ID, "cancel", "cancel")
	require.NoError(t, e)
	require.NoError(t, s.MarkStepUnknown(ctx, r.ID, "worker", r.Fence, step.ID))
	got, e := s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Cancelled, got.State)
	balance, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.EqualValues(t, 100, balance.ReservedMicrocredits)
	r, e = s.CreateRun(ctx, p, "new", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	require.NoError(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Reconciling, "operation_unknown", ""))
	r, e = s.Control(ctx, p, r.ID, "recover", "resume")
	require.NoError(t, e)
	require.Equal(t, domain.Queued, r.State)
}

func TestEvidenceReceiptSettlementAndResume(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	p.Permissions["operations.reconcile"] = true
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 500)
	require.NoError(t, e)
	require.NoError(t, s.MarkStepUnknown(ctx, r.ID, "worker", r.Fence, step.ID))
	r, e = s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	require.Len(t, r.UnresolvedSteps, 1)
	require.Equal(t, step.ID, r.UnresolvedSteps[0].ClientRequestID)
	receipt := domain.ReconcileStepRequest{
		Usage: domain.Usage{InputTokens: 2, OutputTokens: 3, Microcredits: 5, RequestID: "provider-receipt"},
		Evidence: domain.ReconcileEvidence{
			Kind:      "provider_receipt",
			Reference: "provider-receipt",
			Reason:    "verified provider usage",
		},
	}
	wrong := p
	wrong.OrgID = "foreign"
	_, e = s.ReconcileStep(ctx, wrong, r.ID, step.ID, "receipt", receipt)
	code(t, e, "not_found")
	bad := receipt
	bad.Usage.Microcredits = 4
	_, e = s.ReconcileStep(ctx, p, r.ID, step.ID, "bad-cost", bad)
	code(t, e, "receipt_tariff_mismatch")
	r, e = s.ReconcileStep(ctx, p, r.ID, step.ID, "receipt", receipt)
	require.NoError(t, e)
	require.Equal(t, domain.Paused, r.State)
	require.Empty(t, r.UnresolvedSteps)
	_, e = s.ReconcileStep(ctx, p, r.ID, step.ID, "receipt", receipt)
	require.NoError(t, e)
	balance, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.EqualValues(t, 5, balance.SpentMicrocredits)
	require.Zero(t, balance.ReservedMicrocredits)
	steps, e := s.ExecutionSteps(ctx, r.ID)
	require.NoError(t, e)
	require.JSONEq(t, `{"ErrorCode":"receipt_output_unavailable"}`, string(steps[0].Result))
	receipt.Evidence.Reason = "changed"
	_, e = s.ReconcileStep(ctx, p, r.ID, step.ID, "receipt", receipt)
	code(t, e, "idempotency_conflict")
	r, e = s.Control(ctx, p, r.ID, "resume", "resume")
	require.NoError(t, e)
	require.Equal(t, domain.Queued, r.State)
}
func TestReceiptCannotSettleLiveInferenceAndSemanticRepeat(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	p.Permissions["operations.reconcile"] = true
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 10)
	require.NoError(t, e)
	receipt := domain.ReconcileStepRequest{
		Usage: domain.Usage{Microcredits: 0},
		Evidence: domain.ReconcileEvidence{
			Kind:      "operator_attestation",
			Reference: "incident-42",
			Reason:    "provider rejected before billing",
		},
		Result: json.RawMessage(`{"ErrorCode":"not_billed","a":1.00}`),
	}
	_, e = s.ReconcileStep(ctx, p, r.ID, step.ID, "receipt", receipt)
	code(t, e, "run_not_reconciling")
	require.NoError(t, s.MarkStepUnknown(ctx, r.ID, "worker", r.Fence, step.ID))
	_, e = s.ReconcileStep(ctx, p, r.ID, step.ID, "receipt", receipt)
	require.NoError(t, e)
	receipt.Result = json.RawMessage(`{"a":1,"ErrorCode":"not_billed"}`)
	_, e = s.ReconcileStep(ctx, p, r.ID, step.ID, "receipt", receipt)
	require.NoError(t, e)
}

func TestCurrentCatalogIntersectionAndPassiveAction(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	uid, e := s.ResolveUser(ctx, p)
	require.NoError(t, e)
	policy, e := s.Policy(ctx, p, uid)
	require.NoError(t, e)
	policy.Models[0].MaxEffort = "low"
	_, e = s.UpdatePolicy(ctx, p, uid, 0, policy)
	require.NoError(t, e)
	settings, e := s.GetSettings(ctx)
	require.NoError(t, e)
	settings.Models[0].OutputMicrocreditsPerMillion = 2000000
	settings.Models[0].SupportsImages = true
	settings, e = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, e)
	models, e := s.Models(ctx, p)
	require.NoError(t, e)
	require.Len(t, models, 1)
	require.EqualValues(t, 2000000, models[0].OutputMicrocreditsPerMillion)
	require.True(t, models[0].SupportsImages)
	require.Equal(t, "low", models[0].MaxEffort)
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	require.EqualValues(t, 2000000, r.Model.OutputMicrocreditsPerMillion)
	settings.Models[0].MaxEffort = "low"
	settings.Models[0].SupportsImages = false
	_, e = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, e)
	model, e := s.ExecutionModel(ctx, r.ID)
	require.NoError(t, e)
	require.False(t, model.SupportsImages)
	event := domain.AppendEvent{
		SourceEventID: "action",
		Type:          "user.action",
		Action:        "massage.selected",
		Text:          "selected",
	}
	first, e := s.AppendEvent(ctx, p, r.ChatID, event)
	require.NoError(t, e)
	require.Equal(t, event.Action, first.Action)
	again, e := s.AppendEvent(ctx, p, r.ChatID, event)
	require.NoError(t, e)
	require.Equal(t, event.Action, again.Action)
	history, e := s.History(ctx, p, r.ChatID, 0, 100)
	require.NoError(t, e)
	require.Equal(t, event.Action, history[len(history)-1].Action)
}
func TestFinalSteeringCannotBeLost(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	_, e = s.AddInput(ctx, p, r.ID, "steering", domain.RunInput{Kind: "steering", Text: "also compare prices"})
	require.NoError(t, e)
	code(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Completed, "", "done"), "input_pending")
	inputs, e := s.PendingInputs(ctx, r.ID, "worker", r.Fence)
	require.NoError(t, e)
	require.Len(t, inputs, 1)
	require.NoError(t, s.MarkInputsApplied(ctx, r.ID, "worker", r.Fence, []string{inputs[0].ID}))
	require.NoError(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Completed, "", "done"))
}
func TestCachedReceiptAndRevokedExecution(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	p.Permissions["operations.reconcile"] = true
	settings, e := s.GetSettings(ctx)
	require.NoError(t, e)
	settings.Models[0].InputCachedMicrocreditsPerMillion = 100000
	_, e = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, e)
	session, e := s.CreateAuthSession(ctx, p, json.RawMessage(`{"ciphertext":"protected"}`), time.Now().Add(time.Hour))
	require.NoError(t, e)
	in := request()
	in.AuthSessionID = session.ID
	r, e := s.CreateRun(ctx, p, "run", in)
	require.NoError(t, e)
	require.NoError(t, s.RevokeAuthSession(ctx, p, session.ID))
	_, e = s.ExecutionAuthSession(ctx, r.ID)
	require.ErrorIs(t, e, domain.ErrNotFound)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 100)
	require.NoError(t, e)
	require.NoError(t, s.MarkStepUnknown(ctx, r.ID, "worker", r.Fence, step.ID))
	_, e = s.ReconcileStep(
		ctx,
		p,
		r.ID,
		step.ID,
		"receipt",
		domain.ReconcileStepRequest{
			Usage: domain.Usage{InputTokens: 10, CachedInputTokens: 8, OutputTokens: 3, Microcredits: 6},
			Evidence: domain.ReconcileEvidence{
				Kind:      "provider_receipt",
				Reference: "verified",
				Reason:    "cached tokens verified",
			},
		},
	)
	require.NoError(t, e)
	balance, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.EqualValues(t, 6, balance.SpentMicrocredits)
}

func TestRequestedControlWinsFinalTransition(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"cancel", "pause"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()

			s := testStore(t)
			ctx := context.Background()
			p := principal("alice")
			r, e := s.CreateRun(ctx, p, "run", request())
			require.NoError(t, e)
			r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
			require.NoError(t, e)
			_, e = s.Control(ctx, p, r.ID, "control", command)
			require.NoError(t, e)
			require.NoError(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Completed, "", "late final"))
			got, e := s.GetRun(ctx, p, r.ID)
			require.NoError(t, e)
			if command == "cancel" {
				require.Equal(t, domain.Cancelled, got.State)
			} else {
				require.Equal(t, domain.Paused, got.State)
			}
			require.Empty(t, got.Result)
			events, e := s.History(ctx, p, r.ChatID, 0, 100)
			require.NoError(t, e)
			for _, event := range events {
				require.NotEqual(t, "message.assistant", event.Type)
			}
		})
	}
}
func TestTombstonedArtifactQuotaReleasedOnlyAfterCollection(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	p.Permissions["chat.delete"] = true
	p.Permissions["artifacts.read"] = true
	uid, e := s.ResolveUser(ctx, p)
	require.NoError(t, e)
	a := domain.Artifact{
		ID:        uuid.NewString(),
		OwnerID:   uid,
		OrgID:     p.OrgID,
		MIME:      "text/plain",
		SHA256:    "hash",
		Size:      60,
		CreatedAt: time.Now(),
	}
	require.NoError(t, s.CommitArtifact(ctx, a, 100))
	require.ErrorIs(t, s.MarkArtifactCollected(ctx, a.ID), domain.ErrNotFound)
	chat, e := s.CreateChat(ctx, p)
	require.NoError(t, e)
	require.NoError(t, s.AttachChatArtifact(ctx, p, chat.ID, a.ID))
	require.NoError(t, s.DeleteChat(ctx, p, chat.ID))
	b := a
	b.ID = uuid.NewString()
	require.Error(t, s.CommitArtifact(ctx, b, 100))
	deleted, e := s.ListDeletedArtifacts(ctx, 100)
	require.NoError(t, e)
	require.Len(t, deleted, 1)
	require.Equal(t, a.ID, deleted[0].ID)
	require.NoError(t, s.MarkArtifactCollected(ctx, a.ID))
	require.NoError(t, s.MarkArtifactCollected(ctx, a.ID))
	require.NoError(t, s.CommitArtifact(ctx, b, 100))
}

func TestDeletedChatDeniesAllRunIdempotencyReplays(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	p.Permissions["chat.delete"] = true
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	input := domain.RunInput{Kind: "steering", Text: "private preference"}
	_, e = s.AddInput(ctx, p, r.ID, "input", input)
	require.NoError(t, e)
	_, e = s.Control(ctx, p, r.ID, "pause", "pause")
	require.NoError(t, e)
	require.NoError(t, s.DeleteChat(ctx, p, r.ChatID))
	got, e := s.CreateRun(ctx, p, "run", request())
	code(t, e, "not_found")
	require.Empty(t, got.ID)
	got, e = s.AddInput(ctx, p, r.ID, "input", input)
	code(t, e, "not_found")
	require.Empty(t, got.ID)
	got, e = s.Control(ctx, p, r.ID, "pause", "pause")
	code(t, e, "not_found")
	require.Empty(t, got.ID)
}

func TestControlOnlyCallerReceivesNoChatContent(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	delete(p.Permissions, "chat.read")
	input := domain.RunInput{Kind: "steering", Text: "private"}
	for range 2 {
		got, controlErr := s.AddInput(ctx, p, r.ID, "input", input)
		require.NoError(t, controlErr)
		require.Equal(t, r.ID, got.ID)
		require.Empty(t, got.Input.Text)
		require.Empty(t, got.ChatID)
		require.Nil(t, got.Question)
		require.Nil(t, got.Approval)
		got, controlErr = s.Control(ctx, p, r.ID, "pause", "pause")
		require.NoError(t, controlErr)
		require.Equal(t, r.ID, got.ID)
		require.Empty(t, got.Input.Text)
		require.Empty(t, got.OwnerID)
	}
}
func TestConcurrentFinalizationAndDeletionPreservesSettlement(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	p.Permissions["chat.delete"] = true
	for range 8 {
		chat, e := s.CreateChat(ctx, p)
		require.NoError(t, e)
		in := request()
		in.ChatID = chat.ID
		r, e := s.CreateRun(ctx, p, uuid.NewString(), in)
		require.NoError(t, e)
		r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
		require.NoError(t, e)
		step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 10)
		require.NoError(t, e)
		require.NoError(
			t,
			s.CompleteStep(
				ctx,
				r.ID,
				"worker",
				r.Fence,
				step.ID,
				json.RawMessage(`{"text":"done"}`),
				domain.Usage{Microcredits: 3},
			),
		)
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() { <-start; done <- s.DeleteChat(ctx, p, chat.ID) }()
		go func() { <-start; done <- s.Transition(ctx, r.ID, "worker", r.Fence, domain.Completed, "", "done") }()
		close(start)
		for range 2 {
			err := <-done
			if err != nil {
				code(t, err, "lease_lost")
			}
		}
		_, e = s.GetRun(ctx, p, r.ID)
		code(t, e, "not_found")
	}
	balance, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.EqualValues(t, 24, balance.SpentMicrocredits)
	require.Zero(t, balance.ReservedMicrocredits)
}

func TestEditableModelsRequireConfiguredProvider(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	s.SetAllowedProviders([]string{"openai"})
	settings, e := s.GetSettings(ctx)
	require.NoError(t, e)
	settings.Models[0].Provider = "unconfigured"
	_, e = s.UpdateSettings(ctx, p, settings.Version, settings)
	code(t, e, "unknown_provider")
	settings.Models[0].Provider = "openai"
	settings, e = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, e)
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	require.Equal(t, "openai", r.Model.Provider)
	s.SetAllowedProviders([]string{"openai", "other"})
	settings.Models[0].Provider = "other"
	_, e = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, e)
	_, e = s.ExecutionModel(ctx, r.ID)
	code(t, e, "model_contract_changed")
}

func TestControlWinsInteractionPublication(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"question", "approval"} {
		for _, command := range []string{"cancel", "pause"} {
			t.Run(kind+"/"+command, func(t *testing.T) {
				t.Parallel()
				checkControlWinsInteraction(t, kind, command)
			})
		}
	}
}
func TestRegexOversizedEventIsActionable(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	settings, e := s.GetSettings(ctx)
	require.NoError(t, e)
	settings.RegexScanMaxBytes = 20
	_, e = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, e)
	chat, e := s.CreateChat(ctx, p)
	require.NoError(t, e)
	_, e = s.AppendEvent(
		ctx,
		p,
		chat.ID,
		domain.AppendEvent{Type: "user.action", SourceEventID: "large", Text: strings.Repeat("a", 21)},
	)
	require.NoError(t, e)
	_, e = s.AppendEvent(
		ctx,
		p,
		chat.ID,
		domain.AppendEvent{Type: "user.action", SourceEventID: "small", Text: "needle"},
	)
	require.NoError(t, e)
	partial, e := s.Search(ctx, p, chat.ID, domain.SearchRequest{Mode: "regex", Query: "needle", Limit: 10})
	require.NoError(t, e)
	require.True(t, partial.Partial)
	require.Equal(t, "oversized_events_skipped", partial.Reason)
	require.Equal(t, []int64{1}, partial.SkippedSeqs)
	require.Len(t, partial.Events, 1)
	result, e := s.Search(ctx, p, chat.ID, domain.SearchRequest{Mode: "regex", Query: "needle", AfterSeq: 1, Limit: 10})
	require.NoError(t, e)
	require.Len(t, result.Events, 1)
}

func TestPausePreservesReceiptReconciliation(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	p.Permissions["operations.reconcile"] = true
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 10)
	require.NoError(t, e)
	_, e = s.Control(ctx, p, r.ID, "running-pause", "pause")
	require.NoError(t, e)
	require.NoError(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Reconciling, "inference_uncertain", ""))
	got, e := s.Control(ctx, p, r.ID, "uncertain-pause", "pause")
	require.NoError(t, e)
	require.Equal(t, domain.Reconciling, got.State)
	got, e = s.ReconcileStep(
		ctx,
		p,
		r.ID,
		step.ID,
		"receipt",
		domain.ReconcileStepRequest{
			Usage:    domain.Usage{InputTokens: 2, OutputTokens: 1, Microcredits: 3},
			Evidence: domain.ReconcileEvidence{Kind: "provider_receipt", Reference: "receipt", Reason: "verified"},
		},
	)
	require.NoError(t, e)
	require.Equal(t, domain.Paused, got.State)
	got, e = s.Control(ctx, p, r.ID, "resume", "resume")
	require.NoError(t, e)
	require.Equal(t, domain.Queued, got.State)
	balance, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.EqualValues(t, 3, balance.SpentMicrocredits)
	require.Zero(t, balance.ReservedMicrocredits)
}

func TestUnknownInferencePauseRemainsSettleable(t *testing.T) {
	t.Parallel()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	p.Permissions["operations.reconcile"] = true
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 10)
	require.NoError(t, e)
	require.NoError(t, s.MarkStepUnknown(ctx, r.ID, "worker", r.Fence, step.ID))
	got, e := s.Control(ctx, p, r.ID, "pause", "pause")
	require.NoError(t, e)
	require.Equal(t, domain.Reconciling, got.State)
	got, e = s.ReconcileStep(
		ctx,
		p,
		r.ID,
		step.ID,
		"receipt",
		domain.ReconcileStepRequest{
			Usage:    domain.Usage{InputTokens: 1, Microcredits: 1},
			Evidence: domain.ReconcileEvidence{Kind: "provider_receipt", Reference: "receipt", Reason: "verified"},
		},
	)
	require.NoError(t, e)
	require.Equal(t, domain.Paused, got.State)
	got, e = s.Control(ctx, p, r.ID, "resume", "resume")
	require.NoError(t, e)
	require.Equal(t, domain.Queued, got.State)
}

func checkControlWinsInteraction(t *testing.T, kind, command string) {
	t.Helper()

	s := testStore(t)
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "run", request())
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	_, e = s.Control(ctx, p, r.ID, "control", command)
	require.NoError(t, e)
	if kind == "question" {
		e = s.AskQuestion(
			ctx,
			r.ID,
			"worker",
			r.Fence,
			domain.Question{ID: "q", Text: "pick", AllowText: true},
		)
	} else {
		e = s.AskApproval(
			ctx,
			r.ID,
			"worker",
			r.Fence,
			domain.Approval{
				ID:          "a",
				OperationID: "op",
				Text:        "approve",
				ExpiresAt:   time.Now().Add(time.Hour),
			},
		)
	}
	require.NoError(t, e)
	got, e := s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	if command == "cancel" {
		require.Equal(t, domain.Cancelled, got.State)
		require.Nil(t, got.Question)
		require.Nil(t, got.Approval)
		events, eventsErr := s.RunEvents(ctx, p, r.ID, 0, 100)
		require.NoError(t, eventsErr)
		for _, event := range events {
			require.NotEqual(t, kind+".created", event.Type)
		}
	} else {
		require.Equal(t, domain.Paused, got.State)
		got, e = s.Control(ctx, p, r.ID, "resume", "resume")
		require.NoError(t, e)
		if kind == "question" {
			require.Equal(t, domain.WaitingInput, got.State)
			require.Equal(t, "q", got.Question.ID)
		} else {
			require.Equal(t, domain.WaitingApproval, got.State)
			require.Equal(t, "a", got.Approval.ID)
		}
	}
}

func TestRecoveryCancelDoesNotBootstrapOwner(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	p := domain.Principal{
		Issuer:      "https://recovery.example",
		Subject:     "missing",
		OrgID:       "org-a",
		Permissions: map[string]bool{"runs.control": true},
	}
	_, err := s.ControlExistingUser(t.Context(), p, uuid.NewString(), "cancel-existing", "cancel")
	require.Error(t, err)
	var count int
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT count(*) FROM users WHERE issuer=$1 AND subject=$2", p.Issuer, p.Subject).
			Scan(&count),
	)
	require.Zero(t, count)
	_, err = s.ControlExistingUser(t.Context(), p, uuid.NewString(), "resume-denied", "resume")
	require.Error(t, err)
}
