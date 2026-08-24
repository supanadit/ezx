// Package runtime is the scripting-runtime use-case: run a user-supplied
// script against host modules exposed under require("ezx/..."). It owns the
// language-neutral Ports consumed by delivery (internal/script,
// internal/terminal) and implemented by technology adapters
// (internal/repository/js today, lua or others tomorrow). Swapping the
// scripting language is a one-line change in the composition root.
package runtime

import (
	"context"
	"sync"
)

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
	// JSON encoding. Returns an error if fn is not callable in this runtime.
	Call(fn any, args ...any) (any, error)
}

// Binder lets a host module obtain facilities of the active scripting engine
// while it is being constructed. Technology adapters provide the
// implementation; a runtime without callbacks may return a nil Invoker.
type Binder interface {
	// Invoker returns the callback invoker for this runtime, or nil when the
	// engine cannot call back into scripts.
	Invoker() Invoker
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
