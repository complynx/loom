package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/app"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/operations"
	"github.com/complynx/loom/internal/provider"
)

func TestModuleCallInvalidArgumentsNeverReachBackend(t *testing.T) {
	t.Parallel()
	for _, arguments := range []string{`{}`, `{"slot_id":123,"private_input":"DO_NOT_EXPOSE"}`} {
		t.Run(arguments, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			service, registry := newBookingService(t, false)
			p := domain.Principal{
				Issuer: "https://test.identity", Subject: "alice", Actor: "alice", OrgID: "test-org",
				Permissions: map[string]bool{"booking.massage.access": true, "booking.massage.book": true},
			}
			authorize := func(context.Context, domain.Run) (domain.Principal, string, error) {
				return p, "delegated-user-token", nil
			}
			coordinator := &operations.Coordinator{Pool: f.pool}
			bridge := app.NewModuleBridge(registry, coordinator, f.repository, authorize)
			run := f.createRun(t, runRequest("", "", "Book a slot"), "invalid-arguments")
			run, err := f.repository.ClaimRun(t.Context(), run.ID, "argument-worker", time.Minute)
			require.NoError(t, err)
			call := provider.Call{ID: "invalid", Name: "module_call", Arguments: json.RawMessage(
				`{"module":"massage","method":"book_slot","arguments":` + arguments + `,"confirmation_text":"Confirm booking"}`,
			)}
			raw, approval, err := bridge.Call(t.Context(), run, p, "", call)
			require.NoError(t, err)
			require.Nil(t, approval)
			var result struct {
				Error      string `json:"error"`
				Validation struct {
					Fields []string `json:"fields"`
				} `json:"validation"`
			}
			require.NoError(t, json.Unmarshal(raw, &result))
			require.Equal(t, "invalid_arguments", result.Error)
			require.Contains(t, result.Validation.Fields, "slot_id")
			require.NotContains(t, string(raw), "DO_NOT_EXPOSE")
			require.NotContains(t, string(raw), "private_input")
			op, found, err := coordinator.FindByCall(t.Context(), run.ID, call.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "failed", op.State)
			require.True(t, op.Preparation.ExpiresAt.IsZero())
			service.mu.Lock()
			require.Empty(t, service.prepared)
			require.Empty(t, service.executed)
			require.Empty(t, service.status)
			service.mu.Unlock()

			// A corrected, distinct call still requires the ordinary preparation and consent.
			call.ID = "corrected"
			call.Arguments = json.RawMessage(
				`{"module":"massage","method":"book_slot","arguments":{"slot_id":"slot-15"},"confirmation_text":"Confirm booking"}`,
			)
			_, approval, err = bridge.Call(t.Context(), run, p, "", call)
			require.NoError(t, err)
			require.NotNil(t, approval)
			service.mu.Lock()
			require.Len(t, service.prepared, 1)
			require.Empty(t, service.executed)
			service.mu.Unlock()

			broken := app.NewModuleBridge(registry, coordinator, f.repository,
				func(context.Context, domain.Run) (domain.Principal, string, error) {
					return domain.Principal{}, "", errors.New("PRIVATE_DEPENDENCY_DETAIL")
				})
			call.ID = "dependency"
			raw, _, err = broken.Call(t.Context(), run, p, "", call)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"error":"dependency_unavailable"`)
			require.NotContains(t, string(raw), "PRIVATE_DEPENDENCY_DETAIL")
		})
	}
}
