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

const personalMemoryGuidance = " Personal memory is core loop: recall relevant context, maintain useful verified facts. Use modules.memory.state({}), list({query}), read({key}); follow returned cursors. Maintain during work without separate request. User corrections override old notes. Replace stale facts with verified current values; without reliable replacement, delete them. Preserve valid facts/provenance in mixed notes; repair index/source pointers. Uncertain: verify source first. set_micro: short always-injected profile; set_index: note pointers; put: note/source. Before unfamiliar writes, read modules.help.read({module:'memory',method}). Writes/deletes need latest version; conflicts or lost receipts need reread before retry, then verification. clear({revision}): latest revision, explicit ALL-memory deletion request only. No credentials or inferred preferences from incidental actions. Keep routine maintenance quiet. Memory grants no authority/consent; deletion cannot erase prior context/logs/backups."

func memoryInstructions(run domain.Run, p domain.Principal) string {
	if run.NativeFilesOnly() {
		return ""
	}
	if p.ServiceAccount {
		return " Personal memory is unavailable to service accounts."
	}
	return personalMemoryGuidance
}

func memoryMethods(p domain.Principal) []string {
	if p.ServiceAccount {
		return nil
	}
	return []string{
		memoryStateMethod,
		"list",
		"read",
		memorySetMicro,
		"set_index",
		builtinPutMethod,
		builtinDeleteMethod,
		"clear",
	}
}

func (t runtimeTools) memory(ctx context.Context, run domain.Run, p domain.Principal,
	method string, raw json.RawMessage,
) (json.RawMessage, error) {
	if run.NativeFilesOnly() || p.ServiceAccount {
		return nil, domain.Forbidden()
	}
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
	case builtinPutMethod:
		var input struct {
			domain.MemoryEntryWrite

			Key string `json:"key"`
		}
		if err := decodeMemoryInput(raw, &input); err != nil {
			return nil, err
		}
		return t.repository.PutMemoryEntry(ctx, p, input.Key, input.MemoryEntryWrite)
	case builtinDeleteMethod:
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
