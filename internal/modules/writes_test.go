package modules_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/complynx/clx-scripter/internal/modules"
	"github.com/complynx/clx-scripter/internal/operations"
)

func TestWriteBackendBindsConfirmationAndReconcilesWithoutResend(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	var effects atomic.Int32
	var status atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		var request struct {
			OperationID    string          `json:"operation_id"`
			Arguments      json.RawMessage `json:"arguments"`
			IdempotencyKey string          `json:"idempotency_key"`
			Confirmation   map[string]any  `json:"confirmation"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		assert.Equal(t, "operation-stable", request.OperationID)
		switch r.URL.Path {
		case "/internal/agent/massage/prepare-booking":
			assert.JSONEq(t, `{"slot_id":"slot-one"}`, string(request.Arguments))
			_ = json.NewEncoder(w).
				Encode(map[string]any{"status": "prepared", "operation_id": request.OperationID, "confirmation_id": "prepared-confirmation", "arguments": json.RawMessage(`{"slot_id":"slot-one"}`), "expires_at": time.Now().Add(time.Minute), "description": "Book requested slot"})
		case "/internal/agent/massage/book-slot":
			effects.Add(1)
			assert.Equal(t, request.OperationID, request.IdempotencyKey)
			assert.Equal(t, "prepared-confirmation", request.Confirmation["confirmation_id"])
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "accepted", "operation_id": request.OperationID})
		case "/internal/agent/operations/status":
			status.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "not_found", "operation_id": request.OperationID})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	p := principal()
	descriptor, err := registry.Describe(context.Background(), p, "massage", "book_slot")
	require.NoError(t, err)
	actions, err := registry.ListActions(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	operation := operations.Operation{
		ID:        "operation-stable",
		RunID:     "run",
		Module:    "massage",
		Method:    "book_slot",
		Mode:      "service_id",
		Arguments: json.RawMessage(`{"slot_id":"slot-one"}`),
		Binding: operations.Binding{
			Issuer:       p.Issuer,
			Subject:      p.Subject,
			Actor:        p.Actor,
			OrgID:        p.Organization,
			ContractHash: descriptor.ContractHash,
		},
	}
	backend := modules.WriteBackend{Registry: registry, Principal: p}
	prep, err := backend.Prepare(context.Background(), operation)
	require.NoError(t, err)
	operation.Preparation = prep
	result, err := backend.Execute(context.Background(), operation)
	require.NoError(t, err)
	require.Equal(t, "pending", result.State)
	result, err = backend.Status(context.Background(), operation)
	require.NoError(t, err)
	require.Equal(t, "unknown", result.State)
	require.EqualValues(t, 1, effects.Load())
	require.EqualValues(t, 1, status.Load())
	revoked := p
	revoked.Permissions = []string{"zns.massage.access"}
	liveBackend := modules.WriteBackend{
		Registry:         registry,
		Principal:        p,
		RefreshPrincipal: func(context.Context) (modules.Principal, error) { return revoked, nil },
	}
	_, err = liveBackend.Execute(context.Background(), operation)
	require.ErrorIs(t, err, modules.ErrForbidden)
	require.EqualValues(t, 1, effects.Load())
	manifest.Revision = "changed"
	require.NoError(t, registry.Refresh(context.Background()))
	require.ErrorIs(t, backend.Authorize(context.Background(), operation), operations.ErrBinding)
	require.EqualValues(t, 1, effects.Load())
}
func TestExactParametersRejectsInvalidPreparedArguments(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	manifest.Modules = manifest.Modules[:1]
	manifest.Modules[0].Methods[1].Confirmation.Mode = "exact_parameters"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		var request struct {
			OperationID string `json:"operation_id"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"status": "prepared", "operation_id": request.OperationID, "arguments": json.RawMessage(`{"slot_id":123}`), "preconditions": map[string]string{"version": "one"}, "expires_at": time.Now().Add(time.Minute)})
	}))
	defer server.Close()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	p := principal()
	descriptor, err := registry.Describe(context.Background(), p, "massage", "book_slot")
	require.NoError(t, err)
	op := operations.Operation{
		ID:        "stable",
		Module:    "massage",
		Method:    "book_slot",
		Mode:      "exact_parameters",
		Arguments: json.RawMessage(`{"slot_id":"original-slot"}`),
		Binding:   operations.Binding{Issuer: p.Issuer, Subject: p.Subject, ContractHash: descriptor.ContractHash},
	}
	_, err = (modules.WriteBackend{Registry: registry, Principal: p}).Prepare(context.Background(), op)
	require.Error(t, err)
}

func TestApprovedDynamicContractChangePreventsWrite(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	method := manifest.Modules[0].Methods[1]
	manifest.Modules = manifest.Modules[1:]
	var changed atomic.Bool
	var effects atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest" {
			_ = json.NewEncoder(w).Encode(manifest)
			return
		}
		if r.URL.Path == "/internal/agent/orders/discover" {
			current := method
			if changed.Load() {
				current.Description = "Changed dynamic contract"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"methods": []modules.Method{current}})
			return
		}
		var request struct {
			ID string `json:"operation_id"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		if r.URL.Path == method.Confirmation.PreparePath {
			_ = json.NewEncoder(w).
				Encode(map[string]any{"status": "prepared", "operation_id": request.ID, "confirmation_id": "confirmation", "arguments": json.RawMessage(`{"slot_id":"slot"}`), "expires_at": time.Now().Add(time.Minute)})
			return
		}
		effects.Add(1)
		_ = json.NewEncoder(w).
			Encode(map[string]any{"status": "succeeded", "operation_id": request.ID, "result": map[string]string{"booking_id": "booking"}})
	}))
	defer server.Close()
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "one")}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(context.Background()))
	p := principal()
	descriptor, err := registry.Describe(context.Background(), p, "orders", "book_slot")
	require.NoError(t, err)
	operation := operations.Operation{
		ID:        "stable",
		State:     "approved",
		Module:    "orders",
		Method:    "book_slot",
		Mode:      "service_id",
		Arguments: json.RawMessage(`{"slot_id":"slot"}`),
		Binding:   operations.Binding{Issuer: p.Issuer, Subject: p.Subject, ContractHash: descriptor.ContractHash},
	}
	backend := modules.WriteBackend{Registry: registry, Principal: p}
	operation.Preparation, err = backend.Prepare(context.Background(), operation)
	require.NoError(t, err)
	changed.Store(true)
	_, err = backend.Execute(context.Background(), operation)
	require.ErrorIs(t, err, operations.ErrBinding)
	require.EqualValues(t, 0, effects.Load())
}
