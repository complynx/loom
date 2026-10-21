package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
)

type nativePreparationService struct {
	mu                                       sync.Mutex
	state                                    string
	effects, prepares, statuses, inspections int
	keys                                     []string
	expiry                                   time.Time
	manifest                                 modules.Manifest
	server                                   *httptest.Server
}

func newNativePreparationService(t *testing.T, state string) (*nativePreparationService, *modules.Registry) {
	t.Helper()
	raw, err := os.ReadFile("../examples/manifest.json")
	require.NoError(t, err)
	s := &nativePreparationService{
		state:  state,
		expiry: time.Now().UTC().Add(30 * time.Minute).Truncate(time.Microsecond),
	}
	require.NoError(t, json.Unmarshal(raw, &s.manifest))
	s.manifest.Modules = s.manifest.Modules[:1]
	s.manifest.Modules[0].Methods = s.manifest.Modules[0].Methods[1:]
	if state == "succeeded" {
		s.effects = 1
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	u, err := url.Parse(s.server.URL)
	require.NoError(t, err)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{
			{ContainerID: "native-service", URI: "http://registered:" + u.Port() + "/manifest", Address: "127.0.0.1"},
		}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(t.Context()))
	return s, registry
}

func (s *nativePreparationService) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/manifest" {
		w.Header().Set("Loom-Operation-Inspect-Path", "/inspect")
		_ = json.NewEncoder(w).Encode(s.manifest)
		return
	}
	if r.Header.Get("Authorization") != "Bearer delegated-user-token" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var input struct {
		OperationID    string `json:"operation_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.OperationID != "native-preparation" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/inspect":
		s.inspections++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocol_version":  1,
			"status":            s.state,
			"operation_id":      input.OperationID,
			"service_id":        s.manifest.ServiceID,
			"module":            "massage",
			"method":            "book_slot",
			"contract_revision": s.manifest.Revision,
			"binding": map[string]string{
				"issuer":  "https://test.identity",
				"subject": "alice",
				"actor":   "alice",
				"org_id":  "test-org",
			},
			"arguments":       json.RawMessage(`{"slot_id":"native-slot"}`),
			"description":     "Original native slot",
			"confirmation_id": "native-confirmation",
			"expires_at":      s.expiry,
			"result":          map[string]string{"booking_id": "booking-native"},
		})
	case "/internal/agent/massage/book-slot":
		if input.IdempotencyKey != input.OperationID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.keys = append(s.keys, input.IdempotencyKey)
		if s.state != "succeeded" {
			s.effects++
			s.state = "succeeded"
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"operation_id": input.OperationID, "status": s.state, "result": map[string]string{"booking_id": "booking-native"}})
	case "/internal/agent/operations/status":
		s.statuses++
		_ = json.NewEncoder(w).Encode(map[string]string{"operation_id": input.OperationID, "status": "not_found"})
	default:
		s.prepares++
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func TestEngineAdoptsNativePreparationAndReceiptWithoutReplacement(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"prepared", "succeeded", "unknown"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			service, registry := newNativePreparationService(t, state)
			model, engine := mutationWorker(t, f, registry)
			model.respond = func(index int) any {
				if index == 1 {
					return conversationCall("module_call", "adopt-native", map[string]any{
						"module": "massage", "method": "book_slot", "arguments": map[string]any{},
						"resume_operation_id": "native-preparation", "confirmation_text": "Confirm original slot",
					})
				}
				return finalResponse("Receipt booking-native")
			}
			request := runRequest("", "", "Continue my existing booking")
			request.Interaction.Approvals = true
			run := f.createRun(t, request, "adopt-native")
			require.NoError(t, engine.Execute(t.Context(), run.ID))
			current := f.run(t, run.ID)
			coordinator := &operations.Coordinator{Pool: f.pool}
			op, found, err := coordinator.FindByCall(t.Context(), run.ID, "adopt-native")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "native-preparation", op.BackendID())
			require.NotEqual(t, op.ID, op.BackendID())
			switch state {
			case "unknown":
				require.Equal(t, domain.Reconciling, current.State)
				require.Equal(t, "unknown", op.State)
			case "succeeded":
				require.Equal(t, domain.Completed, current.State)
				require.Nil(t, current.Approval)
				require.Contains(t, current.Result, "booking-native")
			default:
				require.Equal(t, domain.WaitingApproval, current.State)
				require.Equal(t, service.expiry, current.Approval.ExpiresAt)
				require.Equal(t, "native-preparation", current.Approval.BackendOperationID)
				require.JSONEq(t, `{"slot_id":"native-slot"}`, string(current.Approval.Arguments))
				code, raw := f.request(
					t,
					"alice",
					"POST",
					"/v1/runs/"+run.ID+"/input",
					"approve-native",
					domain.RunInput{Kind: "approval", ApprovalID: current.Approval.ID, Decision: "approve"},
				)
				require.Equal(t, 200, code, string(raw))
				nativeDone := make(chan error, 1)
				go func() { nativeDone <- executeNativePreparation(t.Context(), service.server.URL) }()
				require.NoError(t, engine.Execute(t.Context(), run.ID))
				require.NoError(t, <-nativeDone)
				require.Equal(t, domain.Completed, f.run(t, run.ID).State)
			}
			assertNativeAdoption(t, service, state, op)
		})
	}
}

func assertNativeAdoption(t *testing.T, service *nativePreparationService, state string, op operations.Operation) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	require.Zero(t, service.prepares)
	require.Equal(t, 1, service.inspections)
	if state == "unknown" {
		require.Zero(t, service.effects)
		require.Positive(t, service.statuses)
	} else {
		require.Equal(t, 1, service.effects)
		if state == "prepared" {
			require.Len(t, service.keys, 2)
		} else {
			require.Empty(t, service.keys)
			require.Contains(t, string(op.Result), "booking-native")
		}
	}
	for _, key := range service.keys {
		assert.Equal(t, "native-preparation", key)
	}
}

func executeNativePreparation(ctx context.Context, base string) error {
	raw := []byte(`{"operation_id":"native-preparation","idempotency_key":"native-preparation"}`)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		base+"/internal/agent/massage/book-slot",
		bytes.NewReader(raw),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer delegated-user-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	_, err = io.Copy(io.Discard, response.Body)
	return err
}
