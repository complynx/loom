package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/complynx/clx-scripter/internal/artifacts"
	"github.com/complynx/clx-scripter/internal/config"
	"github.com/complynx/clx-scripter/internal/domain"
	"github.com/complynx/clx-scripter/internal/modules"
	"github.com/complynx/clx-scripter/internal/provider"
	"github.com/complynx/clx-scripter/internal/sandbox"
	"github.com/complynx/clx-scripter/internal/store"
)

type runtimeTools struct {
	cfg        config.Config
	registry   *modules.Registry
	repository *store.Store
	files      *artifacts.Service
	executor   sandbox.Client
	authorize  RunAuthorizer
	bridge     *ModuleBridge
}

func (t runtimeTools) tools(ctx context.Context, r domain.Run, p domain.Principal, token string) ([]provider.Tool, error) {
	fresh, credential, err := t.fresh(ctx, r)
	if err != nil {
		return nil, err
	}
	mp := modulePrincipal(fresh, credential, r.SettingsSnapshot.Version)
	reads, err := t.registry.List(ctx, mp)
	if err != nil {
		return nil, err
	}
	actions, err := t.registry.ListActions(ctx, mp)
	if err != nil {
		return nil, err
	}
	catalog, _ := json.Marshal(reads)
	description := "Execute JavaScript. Read-only APIs: " + string(catalog) + ". Builtins: modules.history.search({mode:'fulltext'|'regex',query,limit}); modules.artifacts.read({artifact_id}) returns metadata and base64. Await calls; return JSON. Never use writes in scripts."
	tools := []provider.Tool{{Type: "function", Name: "script", Description: description, Strict: true, Parameters: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}},"required":["code"],"additionalProperties":false}`)}}
	if len(actions) > 0 {
		actionCatalog, _ := json.Marshal(actions)
		tools = append(tools, provider.Tool{Type: "function", Name: "module_call", Description: "Perform one durable action from this permitted catalog: " + string(actionCatalog) + ". Approval is collected separately when required.", Strict: false, Parameters: json.RawMessage(`{"type":"object","properties":{"module":{"type":"string"},"method":{"type":"string"},"arguments":{"type":"object"}},"required":["module","method","arguments"],"additionalProperties":false}`)})
	}
	return tools, nil
}
func (t runtimeTools) fresh(ctx context.Context, r domain.Run) (domain.Principal, string, error) {
	return t.authorize(ctx, r)
}
func (t runtimeTools) script(ctx context.Context, r domain.Run, p domain.Principal, token, code string) (json.RawMessage, error) {
	fresh, credential, err := t.fresh(ctx, r)
	if err != nil {
		return nil, err
	}
	reads, err := t.registry.List(ctx, modulePrincipal(fresh, credential, r.SettingsSnapshot.Version))
	if err != nil {
		return nil, err
	}
	bindings := []sandbox.Binding{{Module: "history", Methods: []string{"search"}}, {Module: "artifacts", Methods: []string{"read"}}}
	indexes := map[string]int{"history": 0, "artifacts": 1}
	for _, tool := range reads {
		if tool.Module == "history" || tool.Module == "artifacts" {
			return nil, errors.New("reserved builtin module name collision")
		}
		index, ok := indexes[tool.Module]
		if !ok {
			index = len(bindings)
			indexes[tool.Module] = index
			bindings = append(bindings, sandbox.Binding{Module: tool.Module})
		}
		bindings[index].Methods = append(bindings[index].Methods, tool.Method)
	}
	remote := t.registry.BrokerLive(func(c context.Context) (modules.Principal, error) {
		fresh, credential, e := t.fresh(c, r)
		value := modulePrincipal(fresh, credential, r.SettingsSnapshot.Version)
		value.UserID = r.OwnerID
		return value, e
	}, r.ID, func(c context.Context) error { return t.bridge.guard(c, r) })
	parallelLimit := t.cfg.BootstrapSettings.ParallelRPCLimit
	if parallelLimit <= 0 || parallelLimit > t.cfg.Sandbox.ParallelRPCCeiling {
		parallelLimit = t.cfg.Sandbox.ParallelRPCCeiling
	}
	if parallelLimit <= 0 || parallelLimit > 8 {
		parallelLimit = 8
	}
	slots := make(chan struct{}, parallelLimit)
	broker := sandbox.BrokerFunc(func(c context.Context, module, method string, raw json.RawMessage) (json.RawMessage, error) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-c.Done():
			return nil, c.Err()
		}
		if module != "history" && module != "artifacts" {
			return remote.Call(c, module, method, raw)
		}
		if e := t.bridge.guard(c, r); e != nil {
			return nil, e
		}
		fresh, _, e := t.fresh(c, r)
		if e != nil {
			return nil, e
		}
		switch module + "." + method {
		case "history.search":
			if !fresh.Has("chat.read") {
				return nil, domain.Forbidden()
			}
			var input domain.SearchRequest
			if e = json.Unmarshal(raw, &input); e != nil {
				return nil, e
			}
			result, e := t.repository.Search(c, fresh, r.ChatID, input)
			if e != nil {
				return nil, e
			}
			return json.Marshal(result)
		case "artifacts.read":
			if !fresh.Has("artifacts.read") {
				return nil, domain.Forbidden()
			}
			var input struct {
				ID string `json:"artifact_id"`
			}
			if e = json.Unmarshal(raw, &input); e != nil {
				return nil, e
			}
			meta, reader, e := t.files.Download(c, artifacts.Principal{OwnerID: r.OwnerID, OrgID: r.OrgID}, input.ID)
			if e != nil {
				return nil, e
			}
			defer func() { _ = reader.Close() }()
			settings, policyErr := t.repository.GetSettings(c)
			if policyErr != nil {
				return nil, policyErr
			}
			if !settings.ExternalDisclosure || !r.SettingsSnapshot.ExternalDisclosure || !meta.ExternalDisclosureAllowed {
				return nil, artifacts.ErrForbidden
			}
			const limit = int64(512 << 10)
			if meta.Size > limit {
				return nil, artifacts.ErrTooLarge
			}
			data, e := io.ReadAll(io.LimitReader(reader, limit+1))
			if e != nil || int64(len(data)) > limit {
				return nil, artifacts.ErrTooLarge
			}
			return json.Marshal(map[string]any{"metadata": meta, "encoding": "base64", "data": base64.StdEncoding.EncodeToString(data)})
		default:
			return nil, errors.New("unknown builtin")
		}
	})
	limit := t.cfg.BootstrapSettings.ScriptTimeoutMS
	if limit <= 0 || limit > t.cfg.Sandbox.ScriptTimeoutCeilingMS {
		limit = t.cfg.Sandbox.ScriptTimeoutCeilingMS
	}
	scriptCtx, cancel := context.WithTimeout(ctx, time.Duration(limit)*time.Millisecond)
	defer cancel()
	return t.executor.Execute(scriptCtx, code, bindings, broker)
}
func (t runtimeTools) modelArtifacts(ctx context.Context, r domain.Run) ([]provider.Item, error) {
	fresh, _, err := t.fresh(ctx, r)
	if err != nil {
		return nil, err
	}
	if !fresh.Has("artifacts.read") {
		return nil, domain.Forbidden()
	}
	settings, err := t.repository.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !settings.ExternalDisclosure || !r.SettingsSnapshot.ExternalDisclosure {
		return nil, artifacts.ErrForbidden
	}
	model, err := t.repository.ExecutionModel(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	providerID := model.Provider
	if _, configured := t.cfg.Providers[providerID]; !configured {
		return nil, artifacts.ErrForbidden
	}
	content := []map[string]string{}
	var totalBytes int64
	for _, id := range r.Input.ArtifactIDs {
		meta, e := t.files.Metadata(ctx, artifacts.Principal{OwnerID: r.OwnerID, OrgID: r.OrgID}, id)
		if e != nil {
			return nil, e
		}
		// Apply the service-origin disclosure policy to each resolved artifact.
		if !meta.ExternalDisclosureAllowed {
			return nil, artifacts.ErrForbidden
		}
		totalBytes += meta.Size
		if totalBytes > 5<<20 {
			return nil, artifacts.ErrTooLarge
		}
		image, e := t.files.ForModel(ctx, artifacts.Principal{OwnerID: r.OwnerID, OrgID: r.OrgID}, id, providerID, []string{providerID}, model.SupportsImages, t.cfg.Artifacts.MaxUploadBytes)
		if e != nil {
			return nil, e
		}
		content = append(content, map[string]string{"type": "input_image", "image_url": "data:" + strings.ToLower(image.Metadata.MIME) + ";base64," + base64.StdEncoding.EncodeToString(image.Bytes)})
	}
	item, err := json.Marshal(map[string]any{"type": "message", "role": "user", "content": content})
	if err != nil {
		return nil, err
	}
	return []provider.Item{item}, nil
}
