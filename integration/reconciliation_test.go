package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/api"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/provider"
)

type operatorAuth struct{}

func (operatorAuth) Authenticate(ctx context.Context, r *http.Request) (domain.Principal, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "operator" || token == "foreign-operator" {
		org := "test-org"
		if token == "foreign-operator" {
			org = "foreign-org"
		}
		return domain.Principal{
			Issuer:      "https://test.identity",
			Subject:     token,
			Actor:       token,
			OrgID:       org,
			ExpiresAt:   time.Now().Add(time.Hour),
			Permissions: map[string]bool{"operations.reconcile": true},
		}, nil
	}
	return testAuth{}.Authenticate(ctx, r)
}
func TestProviderReceiptHTTPSettlesExactlyOnceAndResumesWithoutInference(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	model, engine := fakeModel(
		t,
		f,
		func(int) any { return map[string]any{"id": "unreadable-provider-output", "status": "completed"} },
	)
	run := f.createRun(t, runRequest("", "", "PRIVATE user request"), "receipt-run")
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	uncertain := f.run(t, run.ID)
	require.Equal(t, domain.Reconciling, uncertain.State)
	require.Len(t, uncertain.UnresolvedSteps, 1)
	require.Positive(t, uncertain.ReservedMicrocredits)
	endpoint := "/v1/admin/runs/" + run.ID + "/steps/" + uncertain.UnresolvedSteps[0].ID + "/reconcile"
	final := provider.Response{
		ID:     "verified-provider-receipt",
		Text:   "PRIVATE recovered answer",
		Output: []provider.Item{provider.Message("assistant", "PRIVATE recovered answer")},
	}
	result, err := json.Marshal(final)
	require.NoError(t, err)
	receipt := domain.ReconcileStepRequest{
		Usage: domain.Usage{
			InputTokens:  10,
			OutputTokens: 5,
			Microcredits: 15,
			RequestID:    "verified-provider-receipt",
		},
		Evidence: domain.ReconcileEvidence{
			Kind:      "provider_receipt",
			Reference: "verified-provider-receipt",
			Reason:    "verified provider usage and output",
		},
		Result: result,
	}
	code, raw := f.request(t, "alice", "POST", endpoint, "receipt", receipt)
	require.Equal(t, 403, code, string(raw))
	f.server.Close()
	f.server = httptest.NewServer(api.New(f.repository, operatorAuth{}, api.Options{}))
	t.Cleanup(f.server.Close)
	code, raw = f.request(t, "foreign-operator", "POST", endpoint, "receipt", receipt)
	require.Equal(t, 404, code, string(raw))
	bad := receipt
	bad.Usage.Microcredits = 14
	code, raw = f.request(t, "operator", "POST", endpoint, "bad-receipt", bad)
	require.Equal(t, 422, code, string(raw))
	for range 2 {
		code, raw = f.request(t, "operator", "POST", endpoint, "receipt", receipt)
		require.Equal(t, 200, code, string(raw))
		require.NotContains(t, string(raw), "PRIVATE")
		require.NotContains(t, string(raw), "settings_snapshot")
		require.Contains(t, string(raw), `"spent_microcredits":15`)
	}
	reconciled := f.run(t, run.ID)
	require.Equal(t, domain.Paused, reconciled.State)
	require.Empty(t, reconciled.UnresolvedSteps)
	require.EqualValues(t, 15, reconciled.SpentMicrocredits)
	require.Zero(t, reconciled.ReservedMicrocredits)
	code, raw = f.request(t, "alice", "POST", "/v1/runs/"+run.ID+"/resume", "receipt-resume", nil)
	require.Equal(t, 200, code, string(raw))
	require.NoError(t, engine.Execute(context.Background(), run.ID))
	completed := f.run(t, run.ID)
	require.Equal(t, domain.Completed, completed.State)
	require.Equal(t, final.Text, completed.Result)
	require.EqualValues(t, 15, completed.SpentMicrocredits)
	model.mu.Lock()
	require.Len(t, model.requests, 1)
	model.mu.Unlock()
}
