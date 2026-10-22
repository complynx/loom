package app

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/complynx/loom/internal/domain"
)

const (
	memoryModule      = "memory"
	memoryStateMethod = "state"
	memorySetMicro    = "set_micro"
)

func memoryMethods(_ domain.Principal) []string {
	return []string{memoryStateMethod, "list", "read", memorySetMicro, "set_index", "put", "delete", "clear"}
}

func (t runtimeTools) memory(ctx context.Context, run domain.Run, p domain.Principal,
	method string, raw json.RawMessage,
) (json.RawMessage, error) {
	// A service credential must resolve to the same user as the current run.
	owner, err := t.repository.ResolveUser(ctx, p)
	if err != nil {
		return nil, err
	}
	if owner != run.OwnerID || p.OrgID != run.OrgID {
		return nil, domain.Forbidden()
	}
	started := time.Now()
	result, err := t.callMemory(ctx, p, method, raw)
	if t.observer != nil {
		t.observer.Operation(ctx, "user_memory", operationOutcome(err), run.ID, started)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (t runtimeTools) callMemory(ctx context.Context, p domain.Principal,
	method string, raw json.RawMessage,
) (any, error) {
	switch method {
	case memoryStateMethod:
		var input struct{}
		if err := decodeMemoryInput(raw, &input); err != nil {
			return nil, err
		}
		return t.repository.MemoryState(ctx, p)
	case memorySetMicro, "set_index":
		var input domain.MemoryWrite
		if err := decodeMemoryInput(raw, &input); err != nil {
			return nil, err
		}
		if method == memorySetMicro {
			return t.repository.SetMemoryMicro(ctx, p, input)
		}
		return t.repository.SetMemoryIndex(ctx, p, input)
	case "list":
		var input domain.MemoryListRequest
		if err := decodeMemoryInput(raw, &input); err != nil {
			return nil, err
		}
		return t.repository.ListMemoryEntries(ctx, p, input)
	case "read":
		var input struct {
			domain.MemoryReadRequest

			Key string `json:"key"`
		}
		if err := decodeMemoryInput(raw, &input); err != nil {
			return nil, err
		}
		return t.repository.ReadMemoryEntry(ctx, p, input.Key, input.MemoryReadRequest)
	case "put":
		var input struct {
			domain.MemoryEntryWrite

			Key string `json:"key"`
		}
		if err := decodeMemoryInput(raw, &input); err != nil {
			return nil, err
		}
		return t.repository.PutMemoryEntry(ctx, p, input.Key, input.MemoryEntryWrite)
	case "delete":
		var input struct {
			Key     string `json:"key"`
			Version int64  `json:"version"`
		}
		if err := decodeMemoryInput(raw, &input); err != nil {
			return nil, err
		}
		return nil, t.repository.DeleteMemoryEntry(ctx, p, input.Key, input.Version)
	case "clear":
		var input struct {
			Revision int64 `json:"revision"`
		}
		if err := decodeMemoryInput(raw, &input); err != nil {
			return nil, err
		}
		return nil, t.repository.ClearUserMemory(ctx, p, input.Revision)
	default:
		return nil, domain.NotFound()
	}
}

func decodeMemoryInput(raw json.RawMessage, target any) error {
	if err := decodeDiagnosticsInput(raw, target); err != nil {
		return domain.NewError(http.StatusUnprocessableEntity, "invalid_memory_input")
	}
	return nil
}
