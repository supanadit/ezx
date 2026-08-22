package system

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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

// RunFile loads and executes the JavaScript file at path. The file is
// compiled with its absolute path as the program name so relative
// require("./x.js") calls from the entry script resolve against its directory
// (goja_nodejs/require requires the initial script name to be absolute).
func (s *ScriptEngine) RunFile(ctx context.Context, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read script %q: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return s.runSource(ctx, abs, string(src))
}

// RunString loads and executes JavaScript source from a string. Imports of
// registered host modules resolve via the goja_nodejs require registry. The
// source name is empty, so relative require() from a RunString entry resolves
// against the current directory; use RunFile for multi-file scripts.
func (s *ScriptEngine) RunString(ctx context.Context, source string) error {
	return s.runSource(ctx, "", source)
}

// runSource executes the given source on a fresh goja runtime with the given
// program name, registering the host modules and wiring context cancellation.
func (s *ScriptEngine) runSource(ctx context.Context, name, source string) error {
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
			mod.Set("exports", l(rt))
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
