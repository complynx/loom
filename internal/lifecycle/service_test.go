package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/store"
)

func fixture(t *testing.T, settings domain.Settings) *store.Store {
	t.Helper()
	dsn := os.Getenv("CLX_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("CLX_TEST_DATABASE_DSN is required for PostgreSQL behavior tests")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	require.NoError(t, e)
	schema := "lifecycle_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, e = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, e)
	u, e := url.Parse(dsn)
	require.NoError(t, e)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	require.NoError(t, store.Migrate(ctx, u.String()))
	pool, e := pgxpool.New(ctx, u.String())
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
	settings.Models = []domain.Model{
		{
			ID:                           "test",
			ProviderModel:                "test",
			MaxOutputTokens:              100,
			InputMicrocreditsPerMillion:  1000000,
			OutputMicrocreditsPerMillion: 1000000,
		},
	}
	settings.DefaultModelID = "test"
	settings.InitialCredits = 10000
	settings.MaxRunMicrocredits = 1000
	settings.ActiveRunLimit = 8
	settings.HistoryRecentLimit = 50
	settings.MaxSteps = 10
	settings.QuestionTimeoutSeconds = 60
	require.NoError(t, s.Bootstrap(ctx, settings))
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
			"runs.create":        true,
			"runs.control":       true,
			"chat.read":          true,
			"chat.events.append": true,
			"chat.delete":        true,
			"settings.write":     true,
		},
	}
}
func request(cid string) domain.CreateRun {
	return domain.CreateRun{
		ChatID:      cid,
		Input:       domain.Input{Text: "Remember my constraints"},
		Interaction: domain.Interaction{Questions: "choices_and_text", Approvals: true},
		Budget:      domain.Budget{MaxMicrocredits: 1000},
	}
}
func requireNotFound(t *testing.T, e error) {
	t.Helper()
	var de *domain.Error
	require.ErrorAs(t, e, &de)
	require.Equal(t, "not_found", de.Code)
}

func TestDeleteChatBlocksAccessAndKeepsUnknownCredits(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{})
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "first", request(""))
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 100)
	require.NoError(t, e)
	requireNotFound(t, s.DeleteChat(ctx, principal("other"), r.ChatID))
	require.NoError(t, s.DeleteChat(ctx, p, r.ChatID))
	require.NoError(t, s.DeleteChat(ctx, p, r.ChatID))
	_, e = s.History(ctx, p, r.ChatID, 0, 50)
	requireNotFound(t, e)
	_, e = s.Search(ctx, p, r.ChatID, domain.SearchRequest{Mode: "regex", Query: "Remember", Limit: 5})
	requireNotFound(t, e)
	_, e = s.GetRun(ctx, p, r.ID)
	requireNotFound(t, e)
	_, e = s.CreateRun(ctx, p, "deleted", request(r.ChatID))
	requireNotFound(t, e)
	internal, e := s.ExecutionRun(ctx, r.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Cancelled, internal.State)
	require.Equal(t, "chat_deleted", internal.Reason)
	balance, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.Equal(t, int64(100), balance.ReservedMicrocredits)
	unresolved, e := s.UnresolvedSteps(ctx, r.ID)
	require.NoError(t, e)
	require.Len(t, unresolved, 1)
	require.Equal(t, step.ID, unresolved[0].ID)
	fresh, e := s.DefaultChat(ctx, p)
	require.NoError(t, e)
	require.NotEqual(t, r.ChatID, fresh.ID)
	_, e = s.CreateRun(ctx, p, "fresh", request(fresh.ID))
	require.NoError(t, e)
}

func TestPausedDeadlineFreesChatSlotAndKeepsHistory(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{PausedWaitSeconds: 1})
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "first", request(""))
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	require.NoError(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Paused, "requested", ""))
	_, e = s.Pool().
		Exec(ctx, "UPDATE runs SET state_entered_at=clock_timestamp()-interval '2 seconds' WHERE id=$1", r.ID)
	require.NoError(t, e)
	swept, e := s.SweepLifecycle(ctx, 100)
	require.NoError(t, e)
	require.Equal(t, int64(1), swept.ExpiredRuns)
	got, e := s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Failed, got.State)
	require.Equal(t, "wait_expired", got.Reason)
	repeated, e := s.SweepLifecycle(ctx, 100)
	require.NoError(t, e)
	require.Zero(t, repeated.ExpiredRuns)
	history, e := s.History(ctx, p, r.ChatID, 0, 50)
	require.NoError(t, e)
	require.NotEmpty(t, history)
	_, e = s.CreateRun(ctx, p, "next", request(r.ChatID))
	require.NoError(t, e)
}

func TestLifetimeExpiryPreservesReservationAndRejectsLateWorker(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{RunLifetimeSeconds: 1})
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "first", request(""))
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	step, e := s.BeginStep(ctx, r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 100)
	require.NoError(t, e)
	_, e = s.Pool().Exec(ctx, "UPDATE runs SET created_at=clock_timestamp()-interval '2 seconds' WHERE id=$1", r.ID)
	require.NoError(t, e)
	swept, e := s.SweepLifecycle(ctx, 100)
	require.NoError(t, e)
	require.Equal(t, int64(1), swept.ExpiredRuns)
	got, e := s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Failed, got.State)
	require.Equal(t, "run_expired", got.Reason)
	require.Error(
		t,
		s.CompleteStep(ctx, r.ID, "worker", r.Fence, step.ID, json.RawMessage(`{}`), domain.Usage{Microcredits: 50}),
	)
	balance, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.Equal(t, int64(100), balance.ReservedMicrocredits)
}

func TestRetentionSnapshotsAndLiveArtifactProtection(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{})
	ctx := context.Background()
	p := principal("alice")
	old, e := s.CreateChat(ctx, p)
	require.NoError(t, e)
	_, e = s.AppendEvent(
		ctx,
		p,
		old.ID,
		domain.AppendEvent{SourceEventID: "old", Type: "user.action", Text: "old forever message"},
	)
	require.NoError(t, e)
	settings, e := s.GetSettings(ctx)
	require.NoError(t, e)
	settings.HistoryRetention = domain.Retention{Mode: "duration", Days: 1}
	settings.ArtifactRetention = domain.Retention{Mode: "duration", Days: 1}
	_, e = s.UpdateSettings(ctx, p, settings.Version, settings)
	require.NoError(t, e)
	r, e := s.CreateRun(ctx, p, "new", request(""))
	require.NoError(t, e)
	a := domain.Artifact{
		ID:        uuid.NewString(),
		OwnerID:   r.OwnerID,
		OrgID:     p.OrgID,
		MIME:      "text/plain",
		SHA256:    strings.Repeat("a", 64),
		Size:      1,
		CreatedAt: time.Now().Add(-48 * time.Hour),
	}
	require.NoError(t, s.CommitArtifact(ctx, a, 100))
	require.NoError(t, s.AttachChatArtifact(ctx, p, r.ChatID, a.ID))
	_, e = s.Pool().Exec(ctx, "UPDATE chat_events SET received_at=clock_timestamp()-interval '2 days'")
	require.NoError(t, e)
	sweep, e := s.SweepLifecycle(ctx, 100)
	require.NoError(t, e)
	require.Zero(t, sweep.Artifacts)
	_, e = s.GetArtifact(ctx, a.ID)
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	require.NoError(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Completed, "", "done"))
	sweep, e = s.SweepLifecycle(ctx, 100)
	require.NoError(t, e)
	require.Positive(t, sweep.HistoryEvents)
	require.Equal(t, int64(1), sweep.Artifacts)
	_, e = s.GetArtifact(ctx, a.ID)
	requireNotFound(t, e)
	ids, e := s.ListArtifactIDs(ctx)
	require.NoError(t, e)
	require.NotContains(t, ids, a.ID)
	oldHistory, e := s.History(ctx, p, old.ID, 0, 50)
	require.NoError(t, e)
	require.Len(t, oldHistory, 1)
}

func TestCumulativeActiveBudgetDoesNotChargePausedTime(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{RunActiveTimeSeconds: 3})
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "first", request(""))
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "worker", time.Minute)
	require.NoError(t, e)
	_, e = s.Pool().
		Exec(ctx, "UPDATE runs SET active_started_at=clock_timestamp()-interval '1 second' WHERE id=$1", r.ID)
	require.NoError(t, e)
	require.NoError(t, s.Transition(ctx, r.ID, "worker", r.Fence, domain.Paused, "requested", ""))
	_, e = s.Pool().Exec(ctx, "UPDATE runs SET state_entered_at=clock_timestamp()-interval '3 hours' WHERE id=$1", r.ID)
	require.NoError(t, e)
	swept, e := s.SweepLifecycle(ctx, 100)
	require.NoError(t, e)
	require.Zero(t, swept.ExpiredRuns)
	paused, e := s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	require.GreaterOrEqual(t, paused.ActiveMilliseconds, int64(1000))
	require.Less(t, paused.ActiveMilliseconds, int64(3000))
	_, e = s.Control(ctx, p, r.ID, "resume", "resume")
	require.NoError(t, e)
	r, e = s.ClaimRun(ctx, r.ID, "new-worker", time.Minute)
	require.NoError(t, e)
	_, e = s.Pool().
		Exec(ctx, "UPDATE runs SET active_started_at=clock_timestamp()-interval '3 seconds' WHERE id=$1", r.ID)
	require.NoError(t, e)
	swept, e = s.SweepLifecycle(ctx, 100)
	require.NoError(t, e)
	require.Equal(t, int64(1), swept.ExpiredRuns)
	got, e := s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Failed, got.State)
	require.Equal(t, "active_time_expired", got.Reason)
}

func TestDeleteChatPreservesSharedArtifacts(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{})
	ctx := context.Background()
	p := principal("alice")
	first, e := s.CreateRun(ctx, p, "first", request(""))
	require.NoError(t, e)
	second, e := s.CreateChat(ctx, p)
	require.NoError(t, e)
	shared := domain.Artifact{
		ID:      uuid.NewString(),
		OwnerID: first.OwnerID,
		OrgID:   p.OrgID,
		MIME:    "text/plain",
		SHA256:  strings.Repeat("b", 64),
		Size:    1,
	}
	private := shared
	private.ID = uuid.NewString()
	require.NoError(t, s.CommitArtifact(ctx, shared, 100))
	require.NoError(t, s.CommitArtifact(ctx, private, 100))
	require.NoError(t, s.AttachChatArtifact(ctx, p, first.ChatID, shared.ID))
	require.NoError(t, s.AttachChatArtifact(ctx, p, second.ID, shared.ID))
	require.NoError(t, s.AttachChatArtifact(ctx, p, first.ChatID, private.ID))
	require.NoError(t, s.DeleteChat(ctx, p, first.ChatID))
	_, e = s.GetArtifact(ctx, shared.ID)
	require.NoError(t, e)
	_, e = s.GetArtifact(ctx, private.ID)
	requireNotFound(t, e)
	require.NoError(t, s.DeleteChat(ctx, p, second.ID))
	_, e = s.GetArtifact(ctx, shared.ID)
	requireNotFound(t, e)
}

func TestQuestionExpiryIsDurableAndDoesNotRunInference(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{})
	ctx := context.Background()
	p := principal("alice")
	r, e := s.CreateRun(ctx, p, "first", request(""))
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
				ID:        uuid.NewString(),
				Text:      "Which option?",
				AllowText: true,
				ExpiresAt: time.Now().Add(-time.Minute),
			},
		),
	)
	swept, e := s.SweepLifecycle(ctx, 100)
	require.NoError(t, e)
	require.Equal(t, int64(1), swept.ExpiredRuns)
	got, e := s.GetRun(ctx, p, r.ID)
	require.NoError(t, e)
	require.Equal(t, domain.Failed, got.State)
	require.Equal(t, "wait_expired", got.Reason)
	balance, e := s.Balance(ctx, p)
	require.NoError(t, e)
	require.Zero(t, balance.SpentMicrocredits)
}

func TestDeletedHistoryDoesNotAllowPassiveEventReplay(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{})
	ctx := context.Background()
	p := principal("alice")
	chat, e := s.DefaultChat(ctx, p)
	require.NoError(t, e)
	input := domain.AppendEvent{SourceEventID: "external-stable-id", Type: "user.action", Text: "Selected a specialist"}
	_, e = s.AppendEvent(ctx, p, chat.ID, input)
	require.NoError(t, e)
	require.NoError(t, s.DeleteChat(ctx, p, chat.ID))
	_, e = s.AppendEvent(ctx, p, "default", input)
	var de *domain.Error
	require.ErrorAs(t, e, &de)
	require.Equal(t, 409, de.Status)
	fresh, e := s.DefaultChat(ctx, p)
	require.NoError(t, e)
	require.NotEqual(t, chat.ID, fresh.ID)
	history, e := s.History(ctx, p, fresh.ID, 0, 50)
	require.NoError(t, e)
	require.Empty(t, history)
}

func TestProtectedArtifactsDoNotStarveRetentionBatch(t *testing.T) {
	t.Parallel()
	s := fixture(t, domain.Settings{ArtifactRetention: domain.Retention{Mode: "duration", Days: 1}})
	ctx := context.Background()
	p := principal("alice")
	p.Permissions["artifacts.read"] = true
	uid, e := s.ResolveUser(ctx, p)
	require.NoError(t, e)
	chat, e := s.CreateChat(ctx, p)
	require.NoError(t, e)
	ids := []string{}
	for range 2 {
		a := domain.Artifact{
			ID:        uuid.NewString(),
			OwnerID:   uid,
			OrgID:     p.OrgID,
			MIME:      "text/plain",
			SHA256:    "hash",
			Size:      1,
			CreatedAt: time.Now().Add(-72 * time.Hour),
		}
		require.NoError(t, s.CommitArtifact(ctx, a, 100))
		require.NoError(t, s.AttachChatArtifact(ctx, p, chat.ID, a.ID))
		ids = append(ids, a.ID)
	}
	_, e = s.CreateRun(ctx, p, "active", request(chat.ID))
	require.NoError(t, e)
	eligible := domain.Artifact{
		ID:        uuid.NewString(),
		OwnerID:   uid,
		OrgID:     p.OrgID,
		MIME:      "text/plain",
		SHA256:    "hash",
		Size:      1,
		CreatedAt: time.Now().Add(-48 * time.Hour),
	}
	require.NoError(t, s.CommitArtifact(ctx, eligible, 100))
	result, e := s.SweepLifecycle(ctx, 2)
	require.NoError(t, e)
	require.EqualValues(t, 1, result.Artifacts)
	for _, id := range ids {
		_, e = s.GetArtifact(ctx, id)
		require.NoError(t, e)
	}
	_, e = s.GetArtifact(ctx, eligible.ID)
	requireNotFound(t, e)
}
