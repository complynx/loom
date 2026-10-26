// Package sandbox executes scripts without giving JavaScript credentials.
package sandbox

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"

	"github.com/complynx/loom/internal/domain"

	"github.com/dop251/goja"
)

//go:embed file_helper.js
var fileHelper string

const MaxFrameBytes = 1 << 20
const (
	MaxValueBytes    = MaxFrameBytes - frameBufferBytes
	maxScriptBytes   = 65536
	maxCalls         = 128
	maxParallelCalls = 8
	frameBufferBytes = 4096
)

type Binding struct {
	Module  string   `json:"module"`
	Methods []string `json:"methods"`
}
type Broker interface {
	Call(context.Context, string, string, json.RawMessage) (json.RawMessage, error)
}
type Executor interface {
	Execute(context.Context, string, []Binding, Broker) (json.RawMessage, error)
}
type BrokerFunc func(context.Context, string, string, json.RawMessage) (json.RawMessage, error)

func (f BrokerFunc) Call(ctx context.Context, m, n string, a json.RawMessage) (json.RawMessage, error) {
	return f(ctx, m, n, a)
}

// Inline is for isolated workers and tests, not an operating-system boundary.
type Inline struct{}

func (Inline) Execute(ctx context.Context, source string, bindings []Binding, broker Broker) (json.RawMessage, error) {
	if len(source) > maxScriptBytes {
		return nil, failure("script_too_large", "Keep script source within 65536 bytes.")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	vm := goja.New()
	tasks := make(chan func(), maxCalls)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-done:
		}
	}()
	var result json.RawMessage
	var scriptErr error
	finished := false
	rejections := map[*goja.Object]*domain.ToolError{}
	_ = vm.Set("__finish", func(call goja.FunctionCall) goja.Value {
		result = json.RawMessage(call.Argument(0).String())
		finished = true
		return goja.Undefined()
	})
	_ = vm.Set("__fail", func(call goja.FunctionCall) goja.Value {
		value := call.Argument(0)
		if object, ok := value.(*goja.Object); ok && rejections[object] != nil {
			scriptErr = rejections[object]
		} else {
			scriptErr = javascriptFailure(vm, value)
		}
		finished = true
		return goja.Undefined()
	})
	broker = safeBroker(broker)
	installBindingsWithFailures(ctx, vm, bindings, broker, tasks, rejections)
	if _, err := vm.RunString(fileHelper); err != nil {
		return nil, err
	}
	program, err := goja.Compile(
		"",
		"(async function(){\n"+source+"\n})().then(v=>__finish(JSON.stringify(v===undefined?null:v)),e=>__fail(e));",
		false,
	)
	if err != nil {
		return nil, failure("script_syntax_error", "Correct the JavaScript syntax before executing again.")
	}
	_, err = vm.RunProgram(program)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, ok := errors.AsType[*goja.CompilerSyntaxError](err); ok {
			return nil, failure("script_syntax_error", "Correct the JavaScript syntax before executing again.")
		}
		return nil, failure("script_error", "Correct the JavaScript before executing again.")
	}
	for !finished {
		select {
		case task := <-tasks:
			task()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if scriptErr != nil {
		return nil, scriptErr
	}
	if len(result) > MaxValueBytes {
		return nil, failure(
			"script_result_too_large",
			"Return selected fields or a smaller page; keep the result below 1044480 UTF-8 bytes.",
		)
	}
	return result, nil
}

func installBindingsWithFailures(
	ctx context.Context,
	vm *goja.Runtime,
	bindings []Binding,
	broker Broker,
	tasks chan<- func(),
	rejections map[*goja.Object]*domain.ToolError,
) {
	modules := vm.NewObject()
	calls := 0
	parallel := make(chan struct{}, maxParallelCalls)
	for _, binding := range bindings {
		obj := vm.NewObject()
		for _, method := range binding.Methods {
			moduleName, methodName := binding.Module, method
			_ = obj.Set(
				methodName,
				boundMethod(ctx, vm, broker, tasks, parallel, &calls, moduleName, methodName, rejections),
			)
		}
		_ = modules.Set(binding.Module, obj)
	}
	_ = vm.Set("modules", modules)
}

func boundMethod(
	ctx context.Context,
	vm *goja.Runtime,
	broker Broker,
	tasks chan<- func(),
	parallel chan struct{},
	calls *int,
	moduleName, methodName string,
	rejections map[*goja.Object]*domain.ToolError,
) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		promise, resolve, reject := vm.NewPromise()
		(*calls)++
		if *calls > maxCalls {
			settlePromise(
				vm,
				nil,
				failure("script_call_limit", "Use at most 128 module calls per script."),
				resolve,
				reject,
				rejections,
			)
			return vm.ToValue(promise)
		}
		argument := call.Argument(0).Export()
		if goja.IsUndefined(call.Argument(0)) {
			argument = map[string]any{}
		}
		arg, err := json.Marshal(argument)
		if err != nil {
			settlePromise(
				vm,
				nil,
				failure("invalid_arguments", "Supply JSON-compatible arguments."),
				resolve,
				reject,
				rejections,
			)
			return vm.ToValue(promise)
		}
		select {
		case parallel <- struct{}{}:
		default:
			settlePromise(
				vm,
				nil,
				failure(
					"script_parallel_limit",
					"Use at most 8 concurrent calls; await each batch before starting another.",
				),
				resolve,
				reject,
				rejections,
			)
			return vm.ToValue(promise)
		}
		go func() {
			defer func() { <-parallel }()
			value, callErr := broker.Call(ctx, moduleName, methodName, arg)
			task := func() { settlePromise(vm, value, callErr, resolve, reject, rejections) }
			select {
			case tasks <- task:
			case <-ctx.Done():
			}
		}()
		return vm.ToValue(promise)
	}
}

func settlePromise(
	vm *goja.Runtime,
	value json.RawMessage,
	callErr error,
	resolve, reject func(any) error,
	rejections map[*goja.Object]*domain.ToolError,
) {
	if callErr != nil {
		f := SafeFailure(callErr)
		raw, _ := json.Marshal(f)
		var data map[string]any
		_ = json.Unmarshal(raw, &data)
		object := vm.ToValue(data).ToObject(vm)
		rejections[object] = f
		_ = reject(object)
		return
	}
	if len(value) > MaxValueBytes {
		settlePromise(
			vm,
			nil,
			failure("module_result_too_large", "Request a smaller page or fewer fields."),
			resolve,
			reject,
			rejections,
		)
		return
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		settlePromise(
			vm,
			nil,
			failure(
				"invalid_module_result",
				"The module returned invalid JSON. Report the failure; do not assume a write failed.",
			),
			resolve,
			reject,
			rejections,
		)
		return
	}
	_ = resolve(decoded)
}
