// Package goja implements the runtime.Engine Port on top of goja. It runs
// user-supplied CommonJS JavaScript against host modules registered in the
// runtime.Registry (e.g. require("ezx")). This is the only place in the
// project that imports goja; swapping scripting languages means writing a
// sibling package and one wiring line in app/main.go.
package js

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/require"

	"github.com/supanadit/ezx/runtime"
)

// Engine implements the runtime.Engine Port using goja. It binds host modules
// from the registry into each fresh JS runtime and exposes callback support
// to them via runtime.Binder.
type Engine struct {
	registry *runtime.Registry
}

// NewEngine returns an Engine backed by the given module registry.
func NewEngine(registry *runtime.Registry) *Engine {
	return &Engine{registry: registry}
}

// RunFile loads and executes the JavaScript file at path. The file is
// compiled with its absolute path as the program name so relative
// require("./x.js") calls from the entry script resolve against its directory
// (goja_nodejs/require requires the initial script name to be absolute).
func (e *Engine) RunFile(ctx context.Context, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read script %q: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return e.runSource(ctx, abs, string(src))
}

// RunString loads and executes JavaScript source from a string. Imports of
// registered host modules resolve via the goja_nodejs require registry. The
// source name is empty, so relative require() from a RunString entry resolves
// against the current directory; use RunFile for multi-file scripts.
func (e *Engine) RunString(ctx context.Context, source string) error {
	return e.runSource(ctx, "", source)
}

// vmBinder is the runtime.Binder implementation handed to ModuleLoaders while
// host modules are bound into a fresh VM.
type vmBinder struct {
	vm *goja.Runtime
}

// Invoker returns a callback invoker bound to this VM.
func (b vmBinder) Invoker() runtime.Invoker { return vmInvoker{vm: b.vm} }

// vmInvoker calls script function values (delivered as `any` by delivery
// code) inside this VM.
type vmInvoker struct {
	vm *goja.Runtime
}

// Call invokes fn — a callable goja value delivered by reflection binding.
// For an `any` parameter goja delivers a native wrapper of type
// func(goja.FunctionCall) goja.Value; direct goja.Callable / goja.Value
// shapes are accepted too. Args are converted from Go values and the result
// is exported to a plain Go value.
func (i vmInvoker) Call(fn any, args ...any) (any, error) {
	var callable goja.Callable
	switch v := fn.(type) {
	case goja.Callable:
		callable = v
	case goja.Value:
		f, ok := goja.AssertFunction(v)
		if !ok {
			return nil, fmt.Errorf("handler is not a callable script function")
		}
		callable = f
	case func(goja.FunctionCall) goja.Value:
		wrapped := func(this goja.Value, callArgs ...goja.Value) (goja.Value, error) {
			return v(goja.FunctionCall{This: this, Arguments: callArgs}), nil
		}
		callable = wrapped
	default:
		return nil, fmt.Errorf("handler is not a callable script function")
	}
	goArgs := make([]goja.Value, len(args))
	for j, a := range args {
		goArgs[j] = i.vm.ToValue(a)
	}
	res, err := callable(goja.Undefined(), goArgs...)
	if err != nil {
		return nil, err
	}
	if res == nil || goja.IsUndefined(res) || goja.IsNull(res) {
		return nil, nil
	}
	return res.Export(), nil
}

// runSource executes the given source on a fresh goja runtime with the given
// program name, registering the host modules and wiring context cancellation.
func (e *Engine) runSource(ctx context.Context, name, source string) error {
	vm := goja.New()
	vm.SetFieldNameMapper(newFieldNameMapper())

	// Set up the require registry so require("ezx/...") resolves to the
	// registered host modules.
	registry := require.NewRegistry(require.WithGlobalFolders())
	registry.Enable(vm)
	binder := vmBinder{vm: vm}
	for name, loader := range e.registry.Modules() {
		n := name
		l := loader
		registry.RegisterNativeModule(n, func(rt *goja.Runtime, mod *goja.Object) {
			mod.Set("exports", l(binder))
		})
	}

	// Enable interrupt on context cancellation (e.g. SIGTERM triggers the
	// caller's context cancellation, which interrupts a runaway script).
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt("context cancelled")
		case <-done:
		}
	}()
	defer close(done)

	prg, err := goja.Compile(name, source, false)
	if err != nil {
		return fmt.Errorf("compile script: %w", err)
	}
	if _, err := vm.RunProgram(prg); err != nil {
		return fmt.Errorf("run script: %w", err)
	}
	return nil
}
