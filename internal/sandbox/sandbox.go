// Package sandbox executes scripts without giving JavaScript credentials.
package sandbox

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

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
		return nil, errors.New("script exceeds 65536 bytes")
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
	_ = vm.Set("__finish", func(call goja.FunctionCall) goja.Value {
		result = json.RawMessage(call.Argument(0).String())
		finished = true
		return goja.Undefined()
	})
	_ = vm.Set("__fail", func(call goja.FunctionCall) goja.Value {
		scriptErr = fmt.Errorf("script: %s", call.Argument(0).String())
		finished = true
		return goja.Undefined()
	})
	installBindings(ctx, vm, bindings, broker, tasks)
	if _, err := vm.RunString(fileHelper); err != nil {
		return nil, err
	}
	_, err := vm.RunString(
		"(async function(){\n" + source + "\n})().then(v=>__finish(JSON.stringify(v===undefined?null:v)),e=>__fail(String(e)));",
	)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
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
		return nil, errors.New("script result too large")
	}
	return result, nil
}

func installBindings(ctx context.Context, vm *goja.Runtime, bindings []Binding, broker Broker, tasks chan<- func()) {
	modules := vm.NewObject()
	calls := 0
	parallel := make(chan struct{}, maxParallelCalls)
	for _, binding := range bindings {
		obj := vm.NewObject()
		for _, method := range binding.Methods {
			moduleName, methodName := binding.Module, method
			_ = obj.Set(methodName, boundMethod(ctx, vm, broker, tasks, parallel, &calls, moduleName, methodName))
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
) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		promise, resolve, reject := vm.NewPromise()
		(*calls)++
		if *calls > maxCalls {
			_ = reject("script call limit exceeded")
			return vm.ToValue(promise)
		}
		argument := call.Argument(0).Export()
		if goja.IsUndefined(call.Argument(0)) {
			argument = map[string]any{}
		}
		arg, err := json.Marshal(argument)
		if err != nil {
			_ = reject("arguments must be JSON")
			return vm.ToValue(promise)
		}
		select {
		case parallel <- struct{}{}:
		default:
			_ = reject("parallel call limit exceeded")
			return vm.ToValue(promise)
		}
		go func() {
			defer func() { <-parallel }()
			value, callErr := broker.Call(ctx, moduleName, methodName, arg)
			task := func() { settlePromise(value, callErr, resolve, reject) }
			select {
			case tasks <- task:
			case <-ctx.Done():
			}
		}()
		return vm.ToValue(promise)
	}
}

func settlePromise(value json.RawMessage, callErr error, resolve, reject func(any) error) {
	if callErr != nil {
		_ = reject(callErr.Error())
		return
	}
	if len(value) > MaxValueBytes {
		_ = reject("module result too large")
		return
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		_ = reject("module returned invalid JSON")
		return
	}
	_ = resolve(decoded)
}
