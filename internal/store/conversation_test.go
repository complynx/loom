package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/store"
)

func conversationRun(t *testing.T, s *store.Store, mode string) (domain.Run, domain.Approval) {
	t.Helper()
	in := request()
	in.Interaction.ReplyMode = "agent"
	r, err := s.CreateRun(t.Context(), principal("alice"), "run", in)
	require.NoError(t, err)
	r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
	require.NoError(t, err)
	a := domain.Approval{ID: "approval", OperationID: uuid.NewString(), ArgumentHash: "bound-hash",
		ReplyMode: mode, ConfirmationText: "Подтверждаю бронирование Андрея на 15:00",
		Call: &domain.ToolCall{ID: "book", Name: "module_call"}, ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, s.AskApproval(t.Context(), r.ID, "worker", r.Fence, a))
	return r, a
}

func TestConversationInputWakeAndResolutionSurviveRestart(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	r, a := conversationRun(t, s, "agent")
	p := principal("alice")
	in := domain.RunInput{Kind: "steering", Text: "Да, подтверждаю"}
	got, err := s.AddInput(t.Context(), p, r.ID, "message", in)
	require.NoError(t, err)
	require.Equal(t, domain.Queued, got.State)
	require.Empty(t, got.Approval.Decision)
	require.Equal(t, a.ID, got.ConversationInput.Context.ApprovalID)
	receipt := got.ConversationInput
	restarted := store.New(s.Pool(), nil)
	replay, err := restarted.AddInput(t.Context(), p, r.ID, "message", in)
	require.NoError(t, err)
	require.Equal(t, receipt.ID, replay.ConversationInput.ID)
	require.EqualValues(t, 1, replay.ConversationRevision)
	_, err = restarted.AddInput(t.Context(), p, r.ID, "message", domain.RunInput{Kind: "steering", Text: "Нет"})
	code(t, err, "idempotency_conflict")
	got, err = restarted.ClaimRun(t.Context(), r.ID, "next", time.Minute)
	require.NoError(t, err)
	decisions := 0
	restarted.SetApprovalHandler(
		func(context.Context, pgx.Tx, domain.Run, domain.RunInput) error { decisions++; return nil },
	)
	decision := domain.InteractionResolution{
		InputID:      receipt.ID,
		ApprovalID:   a.ID,
		ArgumentHash: a.ArgumentHash,
		Decision:     "approve",
	}
	resolved, err := restarted.ResolveInteraction(t.Context(), r.ID, "next", got.Fence, "resolve", decision)
	require.NoError(t, err)
	require.Equal(t, receipt.ID, resolved.Approval.DecisionInputID)
	_, err = restarted.ResolveInteraction(t.Context(), r.ID, "next", got.Fence, "resolve", decision)
	require.NoError(t, err)
	require.Equal(t, 1, decisions)
	_, err = restarted.ResolveInteraction(t.Context(), r.ID, "wrong-owner", got.Fence, "resolve", decision)
	code(t, err, "lease_lost")
}

func TestConversationCannotResolveStaleOrDifferentProposal(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"missing input", "newer correction", "changed hash", "wrong action", "old presentation", "expired", "protocol"} {
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			mode := "agent"
			if variant == "protocol" {
				mode = "protocol"
			}
			r, a := conversationRun(t, s, mode)
			in := domain.RunInput{Kind: "steering", Text: "Yes"}
			if variant == "old presentation" {
				in.Presentation = &domain.InputPresentation{ApprovalID: "previous-action"}
			}
			got, err := s.AddInput(t.Context(), principal("alice"), r.ID, "message", in)
			require.NoError(t, err)
			decision := domain.InteractionResolution{
				InputID:      got.ConversationInput.ID,
				ApprovalID:   a.ID,
				ArgumentHash: a.ArgumentHash,
				Decision:     "approve",
			}
			switch variant {
			case "missing input":
				decision.InputID = uuid.NewString()
			case "newer correction":
				_, err = s.AddInput(
					t.Context(),
					principal("alice"),
					r.ID,
					"correction",
					domain.RunInput{Kind: "steering", Text: "Wait"},
				)
				require.NoError(t, err)
			case "changed hash":
				decision.ArgumentHash = "other"
			case "wrong action":
				decision.ApprovalID = "other"
			case "expired":
				_, err = s.Pool().
					Exec(t.Context(), `UPDATE runs SET doc=jsonb_set(doc,'{approval,expires_at}',to_jsonb($2::text)) WHERE id=$1`, r.ID, time.Now().Add(-time.Minute).Format(time.RFC3339Nano))
				require.NoError(t, err)
			}
			got, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
			require.NoError(t, err)
			s.SetApprovalHandler(func(context.Context, pgx.Tx, domain.Run, domain.RunInput) error {
				t.Error("must not resolve")
				return nil
			})
			_, err = s.ResolveInteraction(t.Context(), r.ID, "worker", got.Fence, "resolve", decision)
			require.Error(t, err)
			current, err := s.GetRun(t.Context(), principal("alice"), r.ID)
			require.NoError(t, err)
			require.Empty(t, current.Approval.Decision)
		})
	}
}

func TestCriticalTextConfirmationIsExactAndOtherTextRejects(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"Подтверждаю бронирование Андрея на 15:00", "Да", "Сколько стоит?", "подтверждаю бронирование Андрея на 15:00"} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			r, a := conversationRun(t, s, "exact_text")
			var decision string
			s.SetApprovalHandler(func(_ context.Context, _ pgx.Tx, _ domain.Run, in domain.RunInput) error {
				decision = in.Decision
				return nil
			})
			got, err := s.AddInput(
				t.Context(),
				principal("alice"),
				r.ID,
				"message",
				domain.RunInput{Kind: "steering", Text: text},
			)
			require.NoError(t, err)
			want := "reject"
			if text == a.ConfirmationText {
				want = "approve"
			}
			require.Equal(t, want, decision)
			require.Equal(t, want, got.Approval.Decision)
			require.Equal(t, domain.Queued, got.State)
		})
	}
}

func TestConversationQueueFailureRollsBackInputAndDecision(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	r, _ := conversationRun(t, s, "agent")
	broken := store.New(
		s.Pool(),
		func(context.Context, pgx.Tx, string) error { return errors.New("queue unavailable") },
	)
	_, err := broken.AddInput(
		t.Context(),
		principal("alice"),
		r.ID,
		"message",
		domain.RunInput{Kind: "steering", Text: "Hello"},
	)
	require.Error(t, err)
	got, err := s.GetRun(t.Context(), principal("alice"), r.ID)
	require.NoError(t, err)
	require.Equal(t, domain.WaitingApproval, got.State)
	require.Zero(t, got.ConversationRevision)
	var count int
	require.NoError(
		t,
		s.Pool().QueryRow(t.Context(), "SELECT count(*) FROM run_inputs WHERE run_id=$1", r.ID).Scan(&count),
	)
	require.Zero(t, count)
}

func TestConversationInputAcceptedDuringInferenceDoesNotGetStranded(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"question", "approval"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			in := request()
			in.Interaction.ReplyMode = "agent"
			r, err := s.CreateRun(t.Context(), principal("alice"), "run", in)
			require.NoError(t, err)
			r, err = s.ClaimRun(t.Context(), r.ID, "worker", time.Minute)
			require.NoError(t, err)
			_, err = s.AddInput(
				t.Context(),
				principal("alice"),
				r.ID,
				"message",
				domain.RunInput{Kind: "steering", Text: "Wait"},
			)
			require.NoError(t, err)
			if kind == "question" {
				err = s.AskQuestion(t.Context(), r.ID, "worker", r.Fence, domain.Question{Text: "Continue?"})
			} else {
				err = s.AskApproval(
					t.Context(),
					r.ID,
					"worker",
					r.Fence,
					domain.Approval{
						ID:          "action",
						OperationID: uuid.NewString(),
						ReplyMode:   "agent",
						ExpiresAt:   time.Now().Add(time.Minute),
					},
				)
			}
			require.NoError(t, err)
			got, err := s.GetRun(t.Context(), principal("alice"), r.ID)
			require.NoError(t, err)
			require.Equal(t, domain.Queued, got.State)
		})
	}
}

func TestExactTextRejectsConflictingPresentation(t *testing.T) {
	t.Parallel()
	for _, presentation := range []domain.InputPresentation{{ApprovalID: "previous-action"}, {QuestionID: "previous-question"}} {
		t.Run(presentation.ApprovalID+presentation.QuestionID, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			r, a := conversationRun(t, s, "exact_text")
			s.SetApprovalHandler(func(_ context.Context, _ pgx.Tx, _ domain.Run, in domain.RunInput) error {
				require.Equal(t, "reject", in.Decision)
				return nil
			})
			got, err := s.AddInput(
				t.Context(),
				principal("alice"),
				r.ID,
				"message",
				domain.RunInput{Kind: "steering", Text: a.ConfirmationText, Presentation: &presentation},
			)
			require.NoError(t, err)
			require.Equal(t, "reject", got.Approval.Decision)
		})
	}
}

func TestExactTextRetryReturnsOriginalInputAfterLaterMessage(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"agent", "protocol"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			r, _ := conversationRun(t, s, "exact_text")
			_, err := s.Pool().
				Exec(t.Context(), `UPDATE runs SET doc=jsonb_set(doc,'{interaction,reply_mode}',to_jsonb($2::text)) WHERE id=$1`, r.ID, mode)
			require.NoError(t, err)
			s.SetApprovalHandler(func(context.Context, pgx.Tx, domain.Run, domain.RunInput) error { return nil })
			first := domain.RunInput{Kind: "steering", Text: "Not now"}
			accepted, err := s.AddInput(t.Context(), principal("alice"), r.ID, "first", first)
			require.NoError(t, err)
			_, err = s.AddInput(
				t.Context(),
				principal("alice"),
				r.ID,
				"second",
				domain.RunInput{Kind: "steering", Text: "Another message"},
			)
			require.NoError(t, err)
			replayed, err := store.New(s.Pool(), nil).AddInput(t.Context(), principal("alice"), r.ID, "first", first)
			require.NoError(t, err)
			require.Equal(t, accepted.ConversationInput, replayed.ConversationInput)
		})
	}
}
