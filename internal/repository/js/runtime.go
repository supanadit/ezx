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
	"sync"

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
// host modules are bound into a fresh VM. gate is shared by every binder of
// that VM, so all callback paths (lifecycle callbacks, scheduler ticks, HTTP
// routes, streaming handlers) serialize through one gate.
type vmBinder struct {
	vm   *goja.Runtime
	gate *vmGate
}

// Invoker returns a callback invoker bound to this VM.
func (b vmBinder) Invoker() runtime.Invoker { return b.gate }

// Gate returns the script-entry gate bound to this VM. Host modules park
// through it around blocking calls so callbacks can be admitted safely.
func (b vmBinder) Gate() runtime.Gate { return b.gate }

// vmGate is the goja adapter's runtime.Gate. It is the single point that
// decides whether a callback may enter the VM right now:
//
//   - parked: the script goroutine is suspended inside a blocking host call
//     (chain.run's event router, a one-shot process run, a probe poll), which
//     is exactly when lifecycle and scheduler callbacks fire.
//   - running: the script frame is live — a callback must not enter; Call
//     returns runtime.ErrNotParked and the caller skips it.
//
// entry serializes admitted callbacks so two node goroutines exiting at the
// same instant cannot be inside the VM together. Entry is deliberately
// non-reentrant: a callback must not call a blocking host function, which the
// callback contract already requires ("short and non-blocking"); best-effort
// deliveries (process.run's streaming lines) use TryCall so they degrade to a
// skip instead of blocking a callback that is already inside the VM.
type vmGate struct {
	vm *goja.Runtime

	// gateCh is closed while the script is parked (callbacks admitted).
	gateCh chan struct{}
	// entry admits one callback at a time.
	entry sync.Mutex

	mu       sync.Mutex
	parkN    int // nesting depth of Park/Unpark
	inFlight int // callbacks currently inside the VM
	idle     *sync.Cond
}

// newVMGate returns a gate for the given VM, starting in the running state.
func newVMGate(vm *goja.Runtime) *vmGate {
	g := &vmGate{
		vm:     vm,
		gateCh: make(chan struct{}),
	}
	close(g.gateCh)
	g.idle = sync.NewCond(&g.mu)
	return g
}

// Park declares the script suspended in a blocking host call, opening the
// callback window. It is called by the script goroutine only, and nests with
// Unpark.
func (g *vmGate) Park() {
	g.mu.Lock()
	g.parkN++
	if g.parkN == 1 {
		g.gateCh = make(chan struct{})
	}
	g.mu.Unlock()
}

// Unpark resumes the script. The outermost Unpark closes the callback window,
// then waits for any in-flight callback to leave the VM before returning, so a
// script frame never resumes while a callback is still executing.
func (g *vmGate) Unpark() {
	g.mu.Lock()
	if g.parkN > 0 {
		g.parkN--
	}
	if g.parkN == 0 {
		g.gateCh = make(chan struct{})
		for g.inFlight > 0 {
			g.idle.Wait()
		}
	}
	g.mu.Unlock()
}

// Parked returns a channel closed while the script is parked, i.e. while
// callbacks are admitted.
func (g *vmGate) Parked() <-chan struct{} {
	g.mu.Lock()
	ch := g.gateCh
	g.mu.Unlock()
	return ch
}

// Call invokes fn inside the VM. It refuses with runtime.ErrNotParked when the
// script is not parked (entering then would race a live script frame), and
// otherwise serializes with any other callback.
func (g *vmGate) Call(fn any, args ...any) (any, error) {
	if !g.isParked() {
		return nil, runtime.ErrNotParked
	}
	g.entry.Lock()
	defer g.entry.Unlock()

	// Re-check after waiting for the entry lock: Unpark may have resumed the
	// script in the meantime, in which case the VM must not be entered.
	if !g.isParked() {
		return nil, runtime.ErrNotParked
	}
	return g.enter(fn, args...)
}

// TryCall is the non-blocking variant of Call: it returns runtime.ErrNotParked
// when the script is not parked or another callback is currently inside the
// VM, instead of waiting. Best-effort deliveries (process.run's onStdout/
// onStderr lines) use it so they never block or re-enter the VM.
func (g *vmGate) TryCall(fn any, args ...any) (any, error) {
	if !g.isParked() {
		return nil, runtime.ErrNotParked
	}
	if !g.entry.TryLock() {
		return nil, runtime.ErrNotParked
	}
	defer g.entry.Unlock()
	if !g.isParked() {
		return nil, runtime.ErrNotParked
	}
	return g.enter(fn, args...)
}

// enter runs fn inside the VM, tracked so Unpark can wait for it to finish.
func (g *vmGate) enter(fn any, args ...any) (any, error) {
	g.mu.Lock()
	g.inFlight++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.idle.Broadcast()
		g.mu.Unlock()
	}()
	return invoke(g.vm, fn, args...)
}

// Do runs fn inside the callback window, serialized with other callbacks, and
// reports whether it ran. It returns false instead of blocking when the script
// is running.
func (g *vmGate) Do(fn func()) bool {
	if fn == nil {
		return false
	}
	if !g.isParked() {
		return false
	}
	// Serialize with any other callback. The wait is bounded: callbacks are
	// required to be short and non-blocking, and an admitted callback never
	// waits on the script goroutine.
	g.entry.Lock()
	defer g.entry.Unlock()

	// Re-check after waiting for the entry lock: Unpark may have resumed the
	// script in the meantime, in which case the callback must be skipped.
	if !g.isParked() {
		return false
	}
	g.mu.Lock()
	g.inFlight++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.idle.Broadcast()
		g.mu.Unlock()
	}()
	fn()
	return true
}

// isParked reports whether the script is currently suspended.
func (g *vmGate) isParked() bool {
	g.mu.Lock()
	parked := g.parkN > 0
	g.mu.Unlock()
	return parked
}

// invoke calls fn — a callable goja value delivered by reflection binding.
// For an `any` parameter goja delivers a native wrapper of type
// func(goja.FunctionCall) goja.Value; direct goja.Callable / goja.Value
// shapes are accepted too. Args are converted from Go values and the result
// is exported to a plain Go value.
func invoke(vm *goja.Runtime, fn any, args ...any) (any, error) {
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
		goArgs[j] = vm.ToValue(a)
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
// The VM starts in the running state: callbacks are admitted only while the
// script parks inside a blocking host call (see runtime.Gate).
func (e *Engine) runSource(ctx context.Context, name, source string) error {
	vm := goja.New()
	vm.SetFieldNameMapper(newFieldNameMapper())

	// Set up the require registry so require("ezx/...") resolves to the
	// registered host modules.
	registry := require.NewRegistry(require.WithGlobalFolders())
	registry.Enable(vm)
	gate := newVMGate(vm)
	binder := vmBinder{vm: vm, gate: gate}
	for name, loader := range e.registry.Modules() {
		n := name
		l := loader
		registry.RegisterNativeModule(n, func(rt *goja.Runtime, mod *goja.Object) {
			mod.Set("exports", l(binder))
		})
	}

	// Enable interrupt on context cancellation (e.g. SIGTERM triggers the
	// caller's context cancellation, which interrupts a runaway script).
	//
	// The interrupt is withheld while the script is parked in a blocking host
	// call. goja's Interrupt is not park-aware: firing it at a parked script
	// unwinds that host call, and for chain.run — the main parking window — that
	// meant aborting a chain that was already draining, so a clean SIGTERM
	// shutdown surfaced as "run script: context cancelled" and ezx exited 1
	// instead of reporting success. A parked script has, by definition, handed
	// control to Go, so the host call owns the cancellation (chain.run drains
	// its nodes; process.sleep returns ctx.Err()). Only a script that is
	// genuinely executing again is interrupted.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if gate.isParked() {
				return
			}
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
