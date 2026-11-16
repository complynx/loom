package store_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
)

func TestRecoveryContinuationChecksCurrentAccess(t *testing.T) {
	t.Parallel()
	for _, denial := range []string{"session", "model"} {
		t.Run(denial, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			p := principal("continue-access")
			session, err := s.CreateAuthSession(t.Context(), p, json.RawMessage(`{}`), time.Now().Add(time.Hour))
			require.NoError(t, err)
			in := request()
			in.AuthSessionID = session.ID
			r, err := s.CreateRun(t.Context(), p, "create", in)
			require.NoError(t, err)
			_, err = s.Pool().Exec(t.Context(), "UPDATE runs SET state='paused',reason='step_limit' WHERE id=$1", r.ID)
			require.NoError(t, err)
			reason := "authorization_required"
			var policyVersion int64
			if denial == "session" {
				require.NoError(t, s.RevokeAuthSession(t.Context(), p, session.ID))
			} else {
				changed, e := s.UpdatePolicy(
					t.Context(),
					p,
					r.OwnerID,
					0,
					domain.UserPolicy{ActiveRunLimit: 8, Models: []domain.Model{}},
				)
				require.NoError(t, e)
				policyVersion = changed.Version
				reason = "model_not_allowed"
			}
			locator, err := s.ActiveChatRun(t.Context(), p, r.ChatID)
			require.NoError(t, err)
			require.Equal(t, reason, locator.Reason)
			message := domain.RecoverMessage{Text: "Continue"}
			receipt, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "continue", message)
			require.NoError(t, err)
			require.Equal(t, locator.Reason, receipt.Reason)
			require.Equal(t, locator.NextAction, receipt.NextAction)
			require.False(t, receipt.Delivered)
			current, err := s.GetRun(t.Context(), p, r.ID)
			require.NoError(t, err)
			require.Equal(t, domain.Paused, current.State)
			if denial == "session" {
				_, err = s.ReauthorizeAuthSession(
					t.Context(),
					p,
					session.ID,
					json.RawMessage(`{}`),
					time.Now().Add(time.Hour),
					session.Version+1,
				)
			} else {
				_, err = s.UpdatePolicy(
					t.Context(),
					p,
					r.OwnerID,
					policyVersion,
					domain.UserPolicy{ActiveRunLimit: 8, Models: []domain.Model{r.Model}},
				)
			}
			require.NoError(t, err)
			retry, err := s.RecoverChatMessage(t.Context(), p, r.ChatID, "continue", message)
			require.NoError(t, err)
			require.True(t, retry.Delivered)
			require.Equal(t, receipt.MessageID, retry.MessageID)
			require.Equal(t, "queued", retry.Status)
		})
	}
}
