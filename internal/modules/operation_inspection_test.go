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

	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/operations"
)

func TestOperationInspectionChecksOriginalScopeAndUsesNativeID(t *testing.T) {
	t.Parallel()
	for _, variant := range []string{"valid", "issuer", "subject", "actor", "org_id", "service_id", "module", "method", "contract_revision", "operation_id", "arguments", "confirmation_id"} {
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			manifest := example(t)
			manifest.Modules = manifest.Modules[:1]
			p := principal()
			p.Actor, p.Organization = "caller", "org"
			var prepares, effects, statuses atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/manifest" {
					w.Header().Set("Loom-Operation-Inspect-Path", "/operation-inspect")
					_ = json.NewEncoder(w).Encode(manifest)
					return
				}
				assert.Equal(t, "Bearer delegated-token", r.Header.Get("Authorization"))
				var request map[string]json.RawMessage
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
					return
				}
				assert.JSONEq(t, `"native-id"`, string(request["operation_id"]))
				switch r.URL.Path {
				case "/operation-inspect":
					_ = json.NewEncoder(w).Encode(inspectionResponse(manifest, p, variant))
				case "/internal/agent/massage/book-slot":
					effects.Add(1)
					assert.JSONEq(t, `"native-id"`, string(request["idempotency_key"]))
					assert.JSONEq(t, `{"slot_id":"original"}`, string(request["arguments"]))
					_ = json.NewEncoder(w).
						Encode(map[string]any{"operation_id": "native-id", "status": "succeeded", "result": map[string]string{"booking_id": "original-receipt"}})
				case "/internal/agent/operations/status":
					statuses.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]string{"operation_id": "native-id", "status": "not_found"})
				default:
					prepares.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(server.Close)
			registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
				return []modules.Endpoint{endpoint(t, server.URL, "native")}, nil
			}), modules.Options{})
			require.NoError(t, err)
			require.NoError(t, registry.Refresh(t.Context()))
			descriptor, err := registry.Describe(t.Context(), p, "massage", "book_slot")
			require.NoError(t, err)
			require.True(t, descriptor.SupportsOperationAdoption)
			op := operations.Operation{
				ID:                 "loom-id",
				BackendOperationID: "native-id",
				RunID:              "loom-run",
				Module:             "massage",
				Method:             "book_slot",
				Mode:               "service_id",
				Arguments:          json.RawMessage(`{}`),
				Binding: operations.Binding{
					Issuer:       p.Issuer,
					Subject:      p.Subject,
					Actor:        p.Actor,
					OrgID:        p.Organization,
					ContractHash: descriptor.ContractHash,
				},
			}
			backend := modules.WriteBackend{Registry: registry, Principal: p}
			inspection, err := backend.Inspect(t.Context(), op)
			if variant != "valid" {
				require.Error(t, err)
				require.Zero(t, effects.Load())
				require.Zero(t, prepares.Load())
				return
			}
			require.NoError(t, err)
			require.Equal(t, "prepared", inspection.Result.State)
			op.Preparation = inspection.Preparation
			result, err := backend.Execute(t.Context(), op)
			require.NoError(t, err)
			require.Equal(t, "succeeded", result.State)
			result, err = backend.Status(t.Context(), op)
			require.NoError(t, err)
			require.Equal(t, "unknown", result.State)
			require.EqualValues(t, 1, effects.Load())
			require.EqualValues(t, 1, statuses.Load())
			require.Zero(t, prepares.Load())
		})
	}
}

func TestInspectionCapabilityDoesNotChangeLegacyManifestBinding(t *testing.T) {
	t.Parallel()
	manifest := example(t)
	var capability atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if capability.Load() {
			w.Header().Set("Loom-Operation-Inspect-Path", "/inspect")
		}
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	t.Cleanup(server.Close)
	registry, err := modules.NewRegistry(modules.SourceFunc(func(context.Context) ([]modules.Endpoint, error) {
		return []modules.Endpoint{endpoint(t, server.URL, "native")}, nil
	}), modules.Options{})
	require.NoError(t, err)
	require.NoError(t, registry.Refresh(t.Context()))
	old, err := registry.Describe(t.Context(), principal(), "massage", "book_slot")
	require.NoError(t, err)
	require.False(t, old.SupportsOperationAdoption)
	capability.Store(true)
	require.NoError(t, registry.Refresh(t.Context()))
	current, err := registry.Describe(t.Context(), principal(), "massage", "book_slot")
	require.NoError(t, err)
	require.True(t, current.SupportsOperationAdoption)
	require.Equal(t, old.ContractHash, current.ContractHash)
}

func inspectionResponse(manifest modules.Manifest, p modules.Principal, variant string) map[string]any {
	binding := map[string]string{"issuer": p.Issuer, "subject": p.Subject, "actor": p.Actor, "org_id": p.Organization}
	response := map[string]any{
		"protocol_version": 1, "status": "prepared", "operation_id": "native-id", "service_id": manifest.ServiceID,
		"module": "massage", "method": "book_slot", "contract_revision": manifest.Revision, "binding": binding,
		"arguments": json.RawMessage(`{"slot_id":"original"}`), "confirmation_id": "native-confirmation",
		"expires_at": time.Now().Add(30 * time.Minute), "description": "Original slot",
	}
	if variant == "valid" {
		return response
	}
	if _, scoped := binding[variant]; scoped {
		binding[variant] = "foreign"
	} else if variant == "arguments" {
		response[variant] = json.RawMessage(`{"slot_id":42}`)
	} else {
		response[variant] = ""
	}
	return response
}
