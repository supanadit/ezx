// Package script defines the Port for running user-supplied JavaScript
// scripts against the ezx host API. Host modules (ezx/env, ezx/editor, etc.)
// are registered here and exposed to scripts via require("ezx/...").
package script

import (
	"context"
	"sync"
)

// ModuleLoader builds a host module value (a Go struct whose exported methods
// become the script-visible API) for a script runtime.
type ModuleLoader func() any

// Registry holds the host modules available to scripts. It is populated by
// package init() calls, mirroring k6's module registry.
type Registry struct {
	mu      sync.Mutex
	modules map[string]ModuleLoader
}

// NewRegistry returns an empty module registry.
func NewRegistry() *Registry {
	return &Registry{modules: map[string]ModuleLoader{}}
}

// Register adds a host module under the given name (e.g. "ezx/env").
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

// ScriptEngine is the contract a script runtime adapter implements.
type ScriptEngine interface {
	// RunFile loads and executes the JavaScript file at path.
	RunFile(ctx context.Context, path string) error
	// RunString loads and executes JavaScript source from a string.
	RunString(ctx context.Context, source string) error
}
