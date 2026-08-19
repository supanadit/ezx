package system

import (
	"context"
	"fmt"
	"os"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/require"
	"github.com/supanadit/ezx/script"
)

// ScriptEngine implements the script.ScriptEngine Port using goja. It runs
// user-supplied CommonJS JavaScript against host modules registered in the
// provided script.Registry (e.g. require("ezx/env")).
type ScriptEngine struct {
	registry *script.Registry
}

// NewScriptEngine returns a ScriptEngine backed by the given module registry.
func NewScriptEngine(registry *script.Registry) *ScriptEngine {
	return &ScriptEngine{registry: registry}
}

// RunFile loads and executes the JavaScript file at path.
func (s *ScriptEngine) RunFile(ctx context.Context, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read script %q: %w", path, err)
	}
	return s.RunString(ctx, string(src))
}

// RunString loads and executes JavaScript source from a string. Imports of
// registered host modules resolve via the goja_nodejs require registry.
func (s *ScriptEngine) RunString(ctx context.Context, source string) error {
	vm := goja.New()
	vm.SetFieldNameMapper(newFieldNameMapper())

	// Set up the require registry so require("ezx/...") resolves to the
	// registered host modules.
	registry := require.NewRegistry(require.WithGlobalFolders())
	registry.Enable(vm)
	for name, loader := range s.registry.Modules() {
		n := name
		l := loader
		registry.RegisterNativeModule(n, func(rt *goja.Runtime, mod *goja.Object) {
			mod.Set("exports", l())
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

	if _, err := vm.RunString(source); err != nil {
		return fmt.Errorf("run script: %w", err)
	}
	return nil
}
