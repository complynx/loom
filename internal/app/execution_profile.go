package app

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/complynx/loom/internal/domain"
	"github.com/complynx/loom/internal/modules"
	"github.com/complynx/loom/internal/sandbox"
)

// Excluded services must not be consulted, including for help or file recovery.
func (t runtimeTools) runReadCatalog(
	ctx context.Context,
	r domain.Run,
	p domain.Principal,
	token string,
) ([]modules.Tool, error) {
	if r.NativeFilesOnly() {
		return nil, nil
	}
	return t.diagnosticReadCatalog(ctx, modulePrincipal(p, token, r.SettingsSnapshot.Version), p)
}

func nativeFileMethod(module, method string) bool {
	switch module {
	case filesModule:
		return method != transcriptReadMethod && slices.Contains(fileMethods(), method)
	case artifactsModule:
		return method == "open"
	case helpModule:
		return method == helpListMethod || method == builtinReadMethod
	case "$tools":
		return method == "script" || method == "artifact_attach" || method == "question"
	default:
		return false
	}
}

func profileBindings(r domain.Run, bindings []sandbox.Binding) []sandbox.Binding {
	if !r.NativeFilesOnly() {
		return bindings
	}
	filtered := []sandbox.Binding{}
	for _, binding := range bindings {
		methods := []string{}
		for _, method := range binding.Methods {
			if nativeFileMethod(binding.Module, method) {
				methods = append(methods, method)
			}
		}
		if len(methods) > 0 {
			filtered = append(filtered, sandbox.Binding{Module: binding.Module, Methods: methods})
		}
	}
	return filtered
}

func allowsBuiltin(r domain.Run, module, method string) bool {
	return !r.NativeFilesOnly() || nativeFileMethod(module, method)
}

func unavailableNativeFile(err error) (domain.Artifact, error) {
	if err != nil {
		return domain.Artifact{}, err
	}
	return domain.Artifact{}, domain.Forbidden()
}

func profileBroker(r domain.Run, broker sandbox.Broker) sandbox.Broker {
	if !r.NativeFilesOnly() {
		return broker
	}
	return sandbox.BrokerFunc(
		func(ctx context.Context, module, method string, raw json.RawMessage) (json.RawMessage, error) {
			if !nativeFileMethod(module, method) {
				return nil, domain.Forbidden()
			}
			return broker.Call(ctx, module, method, raw)
		},
	)
}
