package store_test

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/migrations"
)

// downgradeToV9 restores the schema of the release before conversation recovery.
func downgradeToV9(t *testing.T, s *store.Store) {
	t.Helper()
	db, err := sql.Open("pgx", s.Pool().Config().ConnString())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	require.NoError(t, err)
	_, err = provider.Down(t.Context())
	require.NoError(t, err)
	version, err := provider.GetDBVersion(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 9, version)
}

func TestConversationMigrationPreservesPopulatedV9AndRepeats(t *testing.T) {
	t.Parallel()
	s := testStore(t) // Also exercises the complete fresh migration path.
	dsn := s.Pool().Config().ConnString()
	p := principal("migration-recovery")
	r, err := s.CreateRun(t.Context(), p, "old-create", request())
	require.NoError(t, err)
	downgradeToV9(t, s)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	step, err := s.BeginStep(t.Context(), r.ID, "worker", r.Fence, "model", json.RawMessage(`{}`), 20)
	require.NoError(t, err)
	require.NoError(t, s.MarkStepUnknown(t.Context(), r.ID, "worker", r.Fence, step.ID))
	input, err := s.AddInput(
		t.Context(),
		p,
		r.ID,
		"old-input",
		domain.RunInput{Kind: domain.InputSteering, Text: "retained old input"},
	)
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), "UPDATE run_inputs SET doc=doc-'history_sequence' WHERE run_id=$1", r.ID)
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), "UPDATE runs SET doc=doc-'admission_sequence' WHERE id=$1", r.ID)
	require.NoError(t, err)
	require.NoError(t, store.Migrate(t.Context(), dsn))
	require.NoError(t, store.Migrate(t.Context(), dsn))
	after, err := s.GetRun(t.Context(), p, r.ID)
	require.NoError(t, err)
	require.EqualValues(t, 20, after.ReservedMicrocredits)
	require.Len(t, after.UnresolvedSteps, 1)
	require.Positive(t, after.AdmissionSequence)
	var inputSequence int64
	require.NoError(
		t,
		s.Pool().
			QueryRow(t.Context(), "SELECT (doc->>'history_sequence')::bigint FROM run_inputs WHERE id=$1 AND NOT applied", input.ConversationInput.ID).
			Scan(&inputSequence),
	)
	require.Positive(t, inputSequence)
	replay, err := s.CreateRun(t.Context(), p, "old-create", request())
	require.NoError(t, err)
	require.Equal(t, r.ID, replay.ID)
	receipt, err := s.RecoverChatMessage(
		t.Context(),
		p,
		r.ChatID,
		"new-message",
		domain.RecoverMessage{Text: "continue", ReplaceRunID: r.ID},
	)
	require.NoError(t, err)
	require.True(t, receipt.Accepted)
	require.Equal(t, "inference_or_effect_uncertain", receipt.Reason)
}

// Retention can remove the admission event of a legacy run. The migration must
// not treat such a run as the oldest one in its chat.
func TestConversationMigrationDoesNotGuessPartialLegacyOrder(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	// legacyRuns creates two runs in one chat. The first run is terminal.
	legacyRuns := func(p domain.Principal) (domain.Run, domain.Run) {
		first, err := s.CreateRun(t.Context(), p, "first", request())
		require.NoError(t, err)
		_, err = s.Control(t.Context(), p, first.ID, "cancel-first", "cancel")
		require.NoError(t, err)
		in := request()
		in.Budget.MaxMicrocredits = 500
		second, err := s.CreateRun(t.Context(), p, "second", in)
		require.NoError(t, err)
		require.Equal(t, first.ChatID, second.ChatID)
		return first, second
	}
	terminal, active := principal("legacy-terminal"), principal("legacy-active")
	older, newer := legacyRuns(terminal)
	_, err := s.Control(t.Context(), terminal, newer.ID, "cancel-second", "cancel")
	require.NoError(t, err)
	removed, current := legacyRuns(active)
	downgradeToV9(t, s)
	_, err = s.Pool().Exec(
		t.Context(),
		"DELETE FROM chat_events WHERE run_id=ANY($1::uuid[]) AND kind='message.user'",
		[]string{newer.ID, removed.ID},
	)
	require.NoError(t, err)
	_, err = s.Pool().Exec(t.Context(), "UPDATE runs SET doc=doc-'admission_sequence'")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(t.Context(), s.Pool().Config().ConnString()))

	// The newer terminal run lost its event. The older run must not be selected.
	located, err := s.ActiveChatRun(t.Context(), terminal, older.ChatID)
	require.NoError(t, err)
	require.Equal(t, "run_order_unavailable", located.Reason)
	require.Equal(t, "create_run", located.NextAction)
	receipt, err := s.RecoverChatMessage(
		t.Context(),
		terminal,
		older.ChatID,
		"continue",
		domain.RecoverMessage{Text: "continue"},
	)
	require.NoError(t, err)
	require.True(t, receipt.Accepted)
	require.False(t, receipt.Delivered)
	require.Equal(t, "run_order_unavailable", receipt.Reason)
	require.Equal(t, located.NextAction, receipt.NextAction)
	var runs int
	require.NoError(
		t,
		s.Pool().QueryRow(t.Context(), "SELECT count(*) FROM runs WHERE chat_id=$1", older.ChatID).Scan(&runs),
	)
	require.Equal(t, 2, runs)

	// A nonterminal run is always the latest run of its chat and keeps its order.
	after, err := s.GetRun(t.Context(), active, current.ID)
	require.NoError(t, err)
	require.Positive(t, after.AdmissionSequence)
	located, err = s.ActiveChatRun(t.Context(), active, current.ChatID)
	require.NoError(t, err)
	require.Equal(t, current.ID, located.RunID)
	_, err = s.Control(t.Context(), active, current.ID, "cancel-current", "cancel")
	require.NoError(t, err)
	located, err = s.ActiveChatRun(t.Context(), active, current.ChatID)
	require.NoError(t, err)
	require.Equal(t, current.ID, located.RunID)
	require.Equal(t, "send_message", located.NextAction)
}
