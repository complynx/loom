package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/complynx/loom/internal/artifacts"
	"github.com/complynx/loom/internal/config"
	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/provider"
	"github.com/complynx/loom/internal/runs"
	"github.com/complynx/loom/internal/sandbox"
	"github.com/complynx/loom/internal/store"
	"github.com/complynx/loom/internal/telemetry"
)

type runtimeTools struct {
	observer   *telemetry.Telemetry
	cfg        config.Config
	registry   *modules.Registry
	repository *store.Store
	files      *artifacts.Service
	executor   sandbox.Executor
	authorize  RunAuthorizer
	bridge     *ModuleBridge
}

func (t runtimeTools) tools(
	ctx context.Context,
	r domain.Run,
	_ domain.Principal,
	_ string,
) ([]provider.Tool, error) {
	fresh, credential, err := t.fresh(ctx, r)
	if err != nil {
		return nil, err
	}
	mp := modulePrincipal(fresh, credential, r.SettingsSnapshot.Version)
	reads, err := t.diagnosticReadCatalog(ctx, mp, fresh)
	if err != nil {
		return nil, err
	}
	actions, err := t.registry.ListActions(ctx, mp)
	if err != nil {
		if !fresh.Has(diagnosticsPermission) || ctx.Err() != nil {
			return nil, err
		}
		actions = nil
	}
	catalog, err := json.Marshal(reads)
	if err != nil {
		return nil, err
	}
	description := "Execute JavaScript. Read-only APIs: " + string(
		catalog,
	) + ". Builtins: modules.history.search({mode:'literal'|'fulltext'|'regex',query,after_seq,before_seq,event_types,limit,max_bytes,snippet_chars}); literal is default, no regex escaping needed. Results include partial, reason, next_cursor, truncated_seqs and skipped_seqs. Use modules.history.read({after_seq,limit,max_bytes}) for bounded event pages. modules.artifacts.read({artifact_id,offset,limit}) returns bounded base64 and next_offset; modules.artifacts.open({artifact_id}) returns a temporary handle, never bytes. Await calls; return JSON. Never use external service writes in scripts."
	description += memoryToolDescription(fresh)
	if fresh.Has(diagnosticsPermission) {
		description += " Diagnostics read-only: modules.diagnostics.health({}), modules.diagnostics.overview({}), modules.diagnostics.runs({state,after_id,limit}), modules.diagnostics.run({run_id}). Organization-scoped metadata only. Lists partial; follow next_cursor as after_id. Never treat exposure as actual provider billing."
	}
	if t.cfg.Search.SemanticEnabled {
		description += " Semantic enabled: modules.history.semantic({embedding_model,embedding,offset,limit,before_seq}) ranks externally generated vectors. Use same embedding model for indexing/query; follow next_offset."
	}
	tools := []provider.Tool{
		{
			Type:        toolFunctionType,
			Name:        "script",
			Description: description,
			Strict:      true,
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"code":{"type":"string"}},"required":["code"],"additionalProperties":false}`,
			),
		},
	}
	if fresh.Has("artifacts.read") {
		tools = append(
			tools,
			provider.Tool{
				Type:        toolFunctionType,
				Name:        "artifact_attach",
				Description: "Attach supported image from temporary handle directly to model, outside JS. Get handle with modules.artifacts.open. Current model/disclosure policy checked; bytes never returned to JS.",
				Strict:      true,
				Parameters: json.RawMessage(
					`{"type":"object","properties":{"handle_id":{"type":"string"}},"required":["handle_id"],"additionalProperties":false}`,
				),
			},
		)
	}
	if len(actions) > 0 {
		actionCatalog, marshalErr := json.Marshal(actions)
		if marshalErr != nil {
			return nil, marshalErr
		}
		tools = append(
			tools,
			provider.Tool{
				Type: toolFunctionType,
				Name: "module_call",
				Description: "Perform one durable action from this permitted catalog: " + string(
					actionCatalog,
				) + ". Approval is collected separately when required.",
				Strict: false,
				Parameters: json.RawMessage(
					`{"type":"object","properties":{"module":{"type":"string"},"method":{"type":"string"},"arguments":{"type":"object"}},"required":["module","method","arguments"],"additionalProperties":false}`,
				),
			},
		)
	}
	return tools, nil
}
func (t runtimeTools) fresh(ctx context.Context, r domain.Run) (domain.Principal, string, error) {
	return t.authorize(ctx, r)
}

func (t runtimeTools) script(
	ctx context.Context,
	r domain.Run,
	_ domain.Principal,
	_ string, code string,
) (json.RawMessage, error) {
	started := time.Now()
	var err error
	defer func() {
		if t.observer != nil {
			t.observer.Operation(ctx, "script", operationOutcome(err), r.ID, started)
		}
	}()
	result, err := t.executeScript(ctx, r, code)
	return result, err
}

// authorizeScript marks admission denials before any sandbox or module RPC starts.
func (t runtimeTools) authorizeScript(ctx context.Context, r domain.Run) (domain.Principal, string, error) {
	principal, credential, err := t.fresh(ctx, r)
	if _, suspended := errors.AsType[*runs.Suspension](err); suspended {
		err = &runs.ScriptNotStartedError{Cause: err}
	}
	return principal, credential, err
}

func (t runtimeTools) executeScript(ctx context.Context, r domain.Run, code string) (json.RawMessage, error) {
	fresh, credential, err := t.authorizeScript(ctx, r)
	if err != nil {
		return nil, err
	}
	reads, err := t.diagnosticReadCatalog(ctx, modulePrincipal(fresh, credential, r.SettingsSnapshot.Version), fresh)
	if err != nil {
		return nil, err
	}
	bindings, err := scriptBindings(reads)
	if err != nil {
		return nil, err
	}
	if fresh.Has(diagnosticsPermission) {
		bindings = append(
			bindings,
			sandbox.Binding{Module: diagnosticsModule, Methods: []string{"health", "overview", "runs", "run"}},
		)
	}
	if methods := memoryMethods(fresh); len(methods) > 0 {
		bindings = append(bindings, sandbox.Binding{Module: memoryModule, Methods: methods})
	}
	if t.cfg.Search.SemanticEnabled {
		bindings[0].Methods = append(bindings[0].Methods, "semantic")
	}
	remote := t.registry.BrokerLive(func(c context.Context) (modules.Principal, error) {
		currentPrincipal, currentCredential, refreshErr := t.fresh(c, r)
		value := modulePrincipal(currentPrincipal, currentCredential, r.SettingsSnapshot.Version)
		value.UserID = r.OwnerID
		return value, refreshErr
	}, r.ID, func(c context.Context) error { return t.bridge.guard(c, r) })
	parallelLimit := t.cfg.BootstrapSettings.ParallelRPCLimit
	if parallelLimit <= 0 || parallelLimit > t.cfg.Sandbox.ParallelRPCCeiling {
		parallelLimit = t.cfg.Sandbox.ParallelRPCCeiling
	}
	if parallelLimit <= 0 || parallelLimit > parallelRPCLimit {
		parallelLimit = parallelRPCLimit
	}
	slots := make(chan struct{}, parallelLimit)
	broker := sandbox.BrokerFunc(
		func(c context.Context, module, method string, raw json.RawMessage) (json.RawMessage, error) {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-c.Done():
				return nil, c.Err()
			}
			if module != historyModule && module != artifactsModule && module != diagnosticsModule &&
				module != memoryModule {
				rpcStarted := time.Now()
				rpcResult, rpcErr := remote.Call(c, module, method, raw)
				if t.observer != nil {
					t.observer.Operation(c, "module_rpc", operationOutcome(rpcErr), r.ID, rpcStarted)
				}
				return rpcResult, rpcErr
			}
			return t.builtin(c, r, module, method, raw)
		},
	)
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
		image, e := t.files.ForModel(
			ctx,
			artifacts.Principal{OwnerID: r.OwnerID, OrgID: r.OrgID},
			id,
			providerID,
			[]string{providerID},
			model.SupportsImages,
			t.cfg.Artifacts.MaxUploadBytes,
		)
		if e != nil {
			return nil, e
		}
		content = append(
			content,
			map[string]string{
				"type": "input_image",
				"image_url": "data:" + strings.ToLower(
					image.Metadata.MIME,
				) + ";base64," + base64.StdEncoding.EncodeToString(
					image.Bytes,
				),
			},
		)
	}
	item, err := json.Marshal(map[string]any{"type": "message", "role": "user", "content": content})
	if err != nil {
		return nil, err
	}
	return []provider.Item{item}, nil
}

func scriptBindings(reads []modules.Tool) ([]sandbox.Binding, error) {
	bindings := []sandbox.Binding{
		{Module: historyModule, Methods: []string{"search", builtinReadMethod}},
		{Module: artifactsModule, Methods: []string{builtinReadMethod, "open"}},
	}
	indexes := map[string]int{historyModule: 0, artifactsModule: 1}
	for _, tool := range reads {
		if tool.Module == historyModule || tool.Module == artifactsModule || tool.Module == diagnosticsModule ||
			tool.Module == memoryModule {
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

	return bindings, nil
}

func (t runtimeTools) builtin(
	c context.Context,
	r domain.Run,
	module, method string,
	raw json.RawMessage,
) (json.RawMessage, error) {
	if e := t.bridge.guard(c, r); e != nil {
		return nil, e
	}
	fresh, _, e := t.fresh(c, r)
	if e != nil {
		return nil, e
	}
	if module == memoryModule {
		return t.memory(c, r, fresh, method, raw)
	}
	if module == diagnosticsModule {
		return t.diagnostics(c, fresh, method, raw)
	}
	switch module + "." + method {
	case "history.semantic":
		if !t.cfg.Search.SemanticEnabled {
			return nil, domain.NotFound()
		}
		var input domain.SearchRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		input.Mode = "semantic"
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		return t.searchHistory(c, fresh, r.ChatID, encoded)
	case "history.read":
		var input domain.ReadHistoryRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		result, err := t.repository.ReadHistory(c, fresh, r.ChatID, input)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	case "artifacts.open":
		return t.openArtifact(c, r, fresh, raw)
	case "history.search":
		if !fresh.Has("chat.read") {
			return nil, domain.Forbidden()
		}
		return t.searchHistory(c, fresh, r.ChatID, raw)
	case "artifacts.read":
		if !fresh.Has("artifacts.read") {
			return nil, domain.Forbidden()
		}
		return t.readArtifact(c, r, raw)
	default:
		return nil, errors.New("unknown builtin")
	}
}

func (t runtimeTools) searchHistory(
	c context.Context,
	fresh domain.Principal,
	chatID string,
	raw json.RawMessage,
) (json.RawMessage, error) {
	var input domain.SearchRequest
	if e := json.Unmarshal(raw, &input); e != nil {
		return nil, e
	}
	result, e := t.repository.Search(c, fresh, chatID, input)
	if e != nil {
		var failure *domain.Error
		if errors.As(e, &failure) && failure.Status == 422 {
			return json.Marshal(
				map[string]any{
					"error": failure.Code,
					"hint":  "Use mode literal for plain text. Regex uses RE2, no lookaround/backreferences. Narrow event_types, after_seq/before_seq; use read for full text.",
				},
			)
		}
		return nil, e
	}
	return json.Marshal(result)
}

func (t runtimeTools) readArtifact(c context.Context, r domain.Run, raw json.RawMessage) (json.RawMessage, error) {
	var input struct {
		ID     string `json:"artifact_id"`
		Offset int64  `json:"offset"`
		Limit  int64  `json:"limit"`
	}
	if e := json.Unmarshal(raw, &input); e != nil {
		return nil, e
	}
	meta, reader, e := t.files.Download(
		c,
		artifacts.Principal{OwnerID: r.OwnerID, OrgID: r.OrgID},
		input.ID,
	)
	if e != nil {
		return nil, e
	}
	defer func() { _ = reader.Close() }()
	settings, policyErr := t.repository.GetSettings(c)
	if policyErr != nil {
		return nil, policyErr
	}
	if !settings.ExternalDisclosure || !r.SettingsSnapshot.ExternalDisclosure ||
		!meta.ExternalDisclosureAllowed {
		return nil, artifacts.ErrForbidden
	}
	const limit = int64(512 << 10)
	if input.Offset < 0 || input.Offset > meta.Size {
		return nil, artifacts.ErrInvalid
	}
	readLimit := input.Limit
	if readLimit <= 0 || readLimit > limit {
		readLimit = limit
	}
	if _, e = reader.Seek(input.Offset, io.SeekStart); e != nil {
		return nil, e
	}
	data, e := io.ReadAll(io.LimitReader(reader, readLimit))
	if e != nil {
		return nil, e
	}
	return json.Marshal(
		map[string]any{
			"offset":      input.Offset,
			"next_offset": input.Offset + int64(len(data)),
			"partial":     input.Offset+int64(len(data)) < meta.Size,
			"metadata":    meta,
			"encoding":    "base64",
			"data":        base64.StdEncoding.EncodeToString(data),
		},
	)
}

func (t runtimeTools) openArtifact(
	ctx context.Context,
	r domain.Run,
	p domain.Principal,
	raw json.RawMessage,
) (json.RawMessage, error) {
	var input struct {
		ID string `json:"artifact_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	handle, err := t.repository.CreateArtifactHandle(ctx, p, r.ID, input.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(handle)
}

// attachArtifact resolves bytes outside JS and rechecks disclosure on every replay.
func (t runtimeTools) attachArtifact(ctx context.Context, r domain.Run, hid string) ([]provider.Item, error) {
	fresh, _, err := t.fresh(ctx, r)
	if err != nil {
		return nil, err
	}
	handle, err := t.repository.LoadArtifactHandle(ctx, fresh, hid)
	if err != nil {
		return nil, err
	}
	if handle.RunID != r.ID {
		return nil, domain.Forbidden()
	}
	r.Input.ArtifactIDs = []string{handle.ArtifactID}
	return t.modelArtifacts(ctx, r)
}
