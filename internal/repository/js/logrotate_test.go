package js

import (
	"context"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/orchestrator"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/runtime"
)

// TestChainBindingLogRotate verifies a node's logRotate object binds from JS
// into domain.LogRotateConfig, including the nested files and oversized structs
// and the per-file overrides.
func TestChainBindingLogRotate(t *testing.T) {
	router := echo.New()
	log := system.NewLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var captured domain.ProcessNode
	factory := func(node domain.ProcessNode) process.ProcessRepository {
		captured = node
		return system.NewProcessRepository(node, nil)
	}
	orch := orchestrator.NewService(factory, log, nil, testOrchestratorDeps())
	reg := runtime.NewRegistry()
	registerTestHostModule(reg, ctx, log, factory, orch, router)

	src := `
		const { chain } = require("ezx");
		chain.run({
			execDefault: false,
			nodes: [{
				name: "app",
				process: { binaryPath: "/bin/sh", arguments: ["-c", "true"] },
				logRotate: {
					compress: true,
					maxBackups: 7,
					signal: "USR1",
					exclude: ["*.gz", "*.1"],
					files: [
						{ include: "/var/log/app/*.log", maxBytes: 1073741824 },
						{ include: "/var/log/app/error.log", maxBytes: 104857600, maxBackups: 14, signal: "HUP" }
					],
					oversized: { maxBytes: 5368709120, keepTailBytes: 209715200, compress: true }
				}
			}]
		});
	`
	if err := NewEngine(reg).RunString(ctx, src); err != nil {
		t.Fatalf("RunString: %v", err)
	}

	lr := captured.LogRotate
	if lr == nil {
		t.Fatalf("logRotate did not bind")
	}
	if !lr.Compress || lr.MaxBackups != 7 || lr.Signal != "USR1" {
		t.Fatalf("group fields = %+v, want compress=true maxBackups=7 signal=USR1", lr)
	}
	if len(lr.Exclude) != 2 || lr.Exclude[1] != "*.1" {
		t.Fatalf("exclude = %v, want [*.gz *.1]", lr.Exclude)
	}
	if len(lr.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(lr.Files))
	}
	if lr.Files[0].Include != "/var/log/app/*.log" || lr.Files[0].MaxBytes != 1073741824 {
		t.Fatalf("files[0] = %+v", lr.Files[0])
	}
	if lr.Files[1].MaxBackups != 14 || lr.Files[1].Signal != "HUP" {
		t.Fatalf("files[1] = %+v, want maxBackups=14 signal=HUP", lr.Files[1])
	}
	if lr.Oversized == nil || lr.Oversized.MaxBytes != 5368709120 || lr.Oversized.KeepTailBytes != 209715200 || !lr.Oversized.Compress {
		t.Fatalf("oversized = %+v", lr.Oversized)
	}
}

// TestChainBindingLogRotateInvalid verifies a logRotate validation error is
// surfaced to the script.
func TestChainBindingLogRotateInvalid(t *testing.T) {
	router := echo.New()
	log := system.NewLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	factory := func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node, nil)
	}
	orch := orchestrator.NewService(factory, log, nil, testOrchestratorDeps())
	reg := runtime.NewRegistry()
	registerTestHostModule(reg, ctx, log, factory, orch, router)

	src := `
		const { chain } = require("ezx");
		chain.run({
			execDefault: false,
			nodes: [{
				name: "app",
				process: { binaryPath: "/bin/sh", arguments: ["-c", "true"] },
				logRotate: { files: [{ include: "/var/log/*.log" }] }
			}]
		});
	`
	err := NewEngine(reg).RunString(ctx, src)
	if err == nil {
		t.Fatal("signal reopen without a signal should error, got nil")
	}
}
