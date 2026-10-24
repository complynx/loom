package app

import (
	"context"
	"encoding/json"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/sandbox"
)

// fetchExternalArtifact resolves an opaque service ID through the existing read broker.
// The service controls the download path; callers cannot supply a URL or credentials.
func fetchExternalArtifact(ctx context.Context, broker sandbox.Broker, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Module     string `json:"module"`
		Method     string `json:"method"`
		ExternalID string `json:"external_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	if input.Module == "" || input.Method == "" || input.ExternalID == "" || len(input.ExternalID) > 1024 {
		return nil, artifacts.ErrInvalid
	}
	args, err := json.Marshal(map[string]string{"external_id": input.ExternalID})
	if err != nil {
		return nil, err
	}
	return broker.Call(ctx, input.Module, input.Method, args)
}

func (t runtimeTools) fetchExternal(
	ctx context.Context,
	run domain.Run,
	broker sandbox.Broker,
	raw json.RawMessage,
) (json.RawMessage, error) {
	if run.NativeFilesOnly() {
		return nil, domain.Forbidden()
	}
	current, _, err := t.fresh(ctx, run)
	if err != nil {
		return nil, err
	}
	if !current.Has("artifacts.read") {
		return nil, domain.Forbidden()
	}
	return fetchExternalArtifact(ctx, broker, raw)
}
