package js

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/orchestrator"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/runtime"
)

// runRealChainBinding runs a JS chain.run against a real orchestrator backed by
// real system.ProcessRepository handles (so file-backed logs actually write to
// disk), returning any error surfaced to the script.
func runRealChainBinding(t *testing.T, src string) error {
	t.Helper()
	router := echo.New()
	log := system.NewLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	factory := func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node, nil)
	}
	orch := orchestrator.NewService(factory, log, nil)

	reg := runtime.NewRegistry()
	registerTestHostModule(reg, ctx, log, factory, orch, router)
	engine := NewEngine(reg)
	return engine.RunString(ctx, src)
}

// TestChainBindingFileLogWritesRotatedFiles verifies a node with
// log: { stdout: "file", filePath, maxBytes, maxBackups } writes rotated files
// on disk through the real JS runtime (tiny maxBytes forces rotation).
func TestChainBindingFileLogWritesRotatedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	src := `
		const { chain } = require("ezx");
		chain.run({
			nodes: [{
				name: "logger",
				process: {
					binaryPath: "/bin/sh",
					arguments: ["-c", "for i in 1 2 3 4 5; do echo '01234567890123456789'; done"],
					log: {
						stdout: "file",
						filePath: "` + path + `",
						maxBytes: 50,
						maxBackups: 2,
					},
				},
			}],
		});
	`
	if err := runRealChainBinding(t, src); err != nil {
		t.Fatalf("RunString: %v", err)
	}

	// 5 lines × 21 bytes = 105 bytes; maxBytes=50 forces rotation.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active log file %q missing: %v", path, err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated backup %q missing: %v", path+".1", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(b), "01234567890123456789") {
		t.Fatalf("active file = %q, want it to contain log lines", b)
	}
}

// TestChainBindingFileLogValidationError verifies a file destination without a
// filePath surfaces a validation error to the script.
func TestChainBindingFileLogValidationError(t *testing.T) {
	src := `
		const { chain } = require("ezx");
		chain.run({
			nodes: [{
				name: "logger",
				process: {
					binaryPath: "/bin/sh",
					arguments: ["-c", "true"],
					log: { stdout: "file" },
				},
			}],
		});
	`
	err := runRealChainBinding(t, src)
	if err == nil {
		t.Fatal("file dest without filePath should error, got nil")
	}
	if !strings.Contains(err.Error(), `requires filePath`) {
		t.Fatalf("RunString error = %q, want it to mention requires filePath", err)
	}
}
