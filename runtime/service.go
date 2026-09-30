// Package runtime is the scripting-runtime use-case: run a user-supplied
// script against host modules exposed under require("ezx/..."). It owns the
// language-neutral Ports consumed by delivery (internal/script,
// internal/terminal) and implemented by technology adapters
// (internal/repository/js today, lua or others tomorrow). Swapping the
// scripting language is a one-line change in the composition root.
package runtime

import (
	"context"
	"errors"
	"sync"
)

// ErrNotParked is returned by Gate.Call when a callback is attempted while the
// script is executing: a single-threaded engine (goja) must never be entered
// from two goroutines, so the engine refuses the call instead of racing a live
// VM frame. Callers see it as a script-callback failure, not a crash.
var ErrNotParked = errors.New("script callback attempted while the script is running")

// Engine is the Port a scripting-language adapter implements. It executes a
// user entrypoint; host modules are resolved by the adapter from the Registry.
type Engine interface {
	// RunFile loads and executes the script at path.
	RunFile(ctx context.Context, path string) error
	// RunString loads and executes script source from a string.
	RunString(ctx context.Context, source string) error
}

// Invoker calls back into a script-provided function value (e.g. a JS arrow
// function registered via ezx.api.post). The value arrives as whatever the
// binding layer delivered (goja.Callable for goja); each adapter's Invoker
// knows how to call its own values. Delivery code only ever holds `any`.
type Invoker interface {
	// Call invokes fn with args and returns a plain Go result suitable for
	// JSON encoding. Returns an error if fn is not callable in this runtime,
	// or if the engine cannot be entered safely at that moment (e.g.
	// ErrNotParked for a single-threaded engine whose script is running).
	Call(fn any, args ...any) (any, error)
}

// TryInvoker is the optional non-blocking form of Invoker, implemented by
// engines that can refuse an entry instead of waiting for it. Engines exposing
// it (the goja adapter does) guarantee that a call which cannot enter the VM
// returns a non-nil error promptly instead of blocking, which lets callers
// degrade to skipping a best-effort delivery.
type TryInvoker interface {
	// TryCall invokes fn, returning promptly with a non-nil error when the
	// engine cannot be entered right now.
	TryCall(fn any, args ...any) (any, error)
}

// Binder lets a host module obtain facilities of the active scripting engine
// while it is being constructed. Technology adapters provide the
// implementation; a runtime without callbacks may return nil.
type Binder interface {
	// Invoker returns the callback invoker for this runtime, or nil when the
	// engine cannot call back into scripts.
	Invoker() Invoker
	// Gate returns the engine's script-entry gate, or nil when the engine
	// cannot call back into scripts. Host modules that block while the script
	// is suspended (chain.run, a one-shot process) park through it so the
	// engine admits callbacks for that window only.
	Gate() Gate
}

// Gate is the engine's script-entry gate: the contract that keeps a
// single-threaded scripting engine safe when callbacks are invoked from host
// goroutines (node lifecycle callbacks, scheduler ticks, HTTP route handlers).
//
// The script goroutine and the callback goroutines both need the VM, and goja
// explicitly allows one goroutine at a time. The rule the gate enforces is
// that callbacks may enter the VM only while the script is parked inside a
// blocking host call — and then only one callback at a time:
//
//	script goroutine:  Park() ... blocking host call ... Unpark()
//	host goroutine:    Call() -> Enter() ... run callback ... Leave()
//
// Park/Unpark nest: a callback that itself makes a blocking host call parks
// again, and only the outermost Unpark waits for in-flight callbacks before
// letting the script frame resume.
//
// Do takes a Go closure — used for lifecycle callbacks the delivery layer has
// already bound (onStart/onReady/onExit/readinessFunc) — while Call takes a
// script value. Both share the same admission rules and serialization.
type Gate interface {
	// Invoker calls back into the script. Call returns ErrNotParked when the
	// script is not parked (the VM must not be entered), so callers skip the
	// callback rather than race the running script.
	Invoker
	// Park marks the script as suspended in a blocking host call, which is
	// when callbacks may run. It is idempotent and nests with Unpark.
	Park()
	// Unpark marks the script as executing again. On the outermost call it
	// waits for any in-flight callback to finish before returning, so the
	// script frame never resumes concurrently with a callback.
	Unpark()
	// Parked returns a channel that is closed while the script is parked.
	// It is intended for host code deciding whether a callback may run at all
	// (e.g. the supervisor skipping a post-drain callback).
	Parked() <-chan struct{}
	// Do runs fn inside the callback window and reports whether it ran.
	Do(fn func()) bool
}

// ModuleLoader builds a host-module value (a Go struct whose exported methods
// become the script-visible API) when the engine binds it. The Binder carries
// runtime facilities so modules like ezx.api can call back into scripts
// without importing the engine's technology.
type ModuleLoader func(b Binder) any

// Registry holds the host modules available to scripts, mirroring k6's module
// registry. The composition root registers the aggregate "ezx" module before
// the engine runs an entrypoint.
type Registry struct {
	mu      sync.Mutex
	modules map[string]ModuleLoader
}

// NewRegistry returns an empty module registry.
func NewRegistry() *Registry {
	return &Registry{modules: map[string]ModuleLoader{}}
}

// Register adds a host module under the given name (e.g. "ezx").
func (r *Registry) Register(name string, loader ModuleLoader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules[name] = loader
}

// Modules returns a snapshot of the registered module loaders.
func (r *Registry) Modules() map[string]ModuleLoader {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]ModuleLoader, len(r.modules))
	for k, v := range r.modules {
		out[k] = v
	}
	return out
}

// Service is the thin use-case wrapper around an Engine. Delivery depends on
// it through local interfaces satisfied structurally.
type Service struct {
	engine Engine
}

// NewService returns a Service backed by the given Engine.
func NewService(engine Engine) *Service {
	return &Service{engine: engine}
}

// RunFile delegates to the wrapped Engine.
func (s *Service) RunFile(ctx context.Context, path string) error {
	return s.engine.RunFile(ctx, path)
}

// RunString delegates to the wrapped Engine.
func (s *Service) RunString(ctx context.Context, source string) error {
	return s.engine.RunString(ctx, source)
}
