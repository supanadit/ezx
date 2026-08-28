package js

import (
	"context"
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

// runChainBinding runs a JS source (typically a chain.run call) against a real
// orchestrator backed by recording fakes, and returns the recorded start order
// plus any error surfaced to the script. Processes exit immediately, so the
// chain completes on its own.
func runChainBinding(t *testing.T, src string) (order []string, runErr error) {
	t.Helper()
	startC := make(chan string, 16)
	router := echo.New()
	log := system.NewLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	factory := func(node domain.ProcessNode) process.ProcessRepository {
		return &e2eFakeProc{name: node.Name, startC: startC, done: make(chan struct{})}
	}
	orch := orchestrator.NewService(factory, log, nil)

	reg := runtime.NewRegistry()
	registerTestHostModule(reg, ctx, log, factory, orch, router)
	engine := NewEngine(reg)

	runErr = engine.RunString(ctx, src)

	// Drain whatever started, preserving order.
	for {
		select {
		case n := <-startC:
			order = append(order, n)
		default:
			return order, runErr
		}
	}
}

// TestChainBindingFlatNodes verifies the new flat `nodes` form with dependsOn
// edges drives the DAG: a starts before its dependent b.
func TestChainBindingFlatNodes(t *testing.T) {
	order, err := runChainBinding(t, `
		const { chain } = require("ezx");
		chain.run({
			nodes: [
				{ name: "a", process: { binaryPath: "/bin/true" } },
				{ name: "b", dependsOn: ["a"], process: { binaryPath: "/bin/true" } },
			],
		});
	`)
	if err != nil {
		t.Fatalf("RunString: %v", err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("flat nodes start order = %v, want [a b]", order)
	}
}

// TestChainBindingChildrenDesugar verifies the legacy `roots`/`children` tree
// form still works (desugared into the flat DAG) with identical ordering.
func TestChainBindingChildrenDesugar(t *testing.T) {
	order, err := runChainBinding(t, `
		const { chain } = require("ezx");
		chain.run({
			roots: [{
				name: "a",
				process: { binaryPath: "/bin/true" },
				children: [{
					name: "b",
					needParentReady: true,
					process: { binaryPath: "/bin/true" },
				}],
			}],
		});
	`)
	if err != nil {
		t.Fatalf("RunString: %v", err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("children desugar start order = %v, want [a b]", order)
	}
}

// TestChainBindingMixedFormRejected verifies a node that sets both children and
// dependsOn is rejected and the error surfaces to the script.
func TestChainBindingMixedFormRejected(t *testing.T) {
	_, err := runChainBinding(t, `
		const { chain } = require("ezx");
		chain.run({
			roots: [{
				name: "a",
				dependsOn: ["z"],
				children: [{ name: "c", process: { binaryPath: "/bin/true" } }],
			}],
		});
	`)
	if err == nil {
		t.Fatal("mixed children+dependsOn form should error, got nil")
	}
	if !strings.Contains(err.Error(), "both Children and DependsOn") {
		t.Fatalf("RunString error = %q, want mixed-form rejection", err)
	}
}

// TestChainBindingUnknownDepError verifies an unknown dependsOn name surfaces
// as an error to the script.
func TestChainBindingUnknownDepError(t *testing.T) {
	_, err := runChainBinding(t, `
		const { chain } = require("ezx");
		chain.run({
			nodes: [
				{ name: "a", process: { binaryPath: "/bin/true" } },
				{ name: "b", dependsOn: ["nope"], process: { binaryPath: "/bin/true" } },
			],
		});
	`)
	if err == nil {
		t.Fatal("unknown dependency should error, got nil")
	}
	if !strings.Contains(err.Error(), `depends on unknown node "nope"`) {
		t.Fatalf("RunString error = %q, want unknown-dep error", err)
	}
}

// TestChainBindingOneshot verifies the additive `oneshot: true` node field
// end-to-end through the real runtime: a oneshot runs to completion (exit 0)
// and only then does its dependent start.
func TestChainBindingOneshot(t *testing.T) {
	order, err := runChainBinding(t, `
		const { chain } = require("ezx");
		chain.run({
			nodes: [
				{ name: "initdb", oneshot: true, process: { binaryPath: "/bin/true" } },
				{ name: "app", dependsOn: ["initdb"], process: { binaryPath: "/bin/true" } },
			],
		});
	`)
	if err != nil {
		t.Fatalf("RunString: %v", err)
	}
	if len(order) != 2 || order[0] != "initdb" || order[1] != "app" {
		t.Fatalf("oneshot start order = %v, want [initdb app]", order)
	}
}

// TestChainBindingOneshotInitDAGExec verifies the "init DAG → exec main"
// pattern through the real runtime: oneshot deps run to completion, then the
// exec node fires. The exec node's binary does not exist, so repository.Exec
// fails (surfacing an error) instead of replacing the test process — but the
// oneshots must have run first.
func TestChainBindingOneshotInitDAGExec(t *testing.T) {
	order, err := runChainBinding(t, `
		const { chain } = require("ezx");
		chain.run({
			nodes: [
				{ name: "initdb", oneshot: true, process: { binaryPath: "/bin/true" } },
				{ name: "stanza", oneshot: true, dependsOn: ["initdb"], process: { binaryPath: "/bin/true" } },
				{ name: "postgres", exec: true, dependsOn: ["initdb", "stanza"], process: { binaryPath: "/nonexistent/ezx-exec-test" } },
			],
		});
	`)
	if err == nil {
		t.Fatal("expected exec failure for non-existent binary, got nil")
	}
	if len(order) != 2 || order[0] != "initdb" || order[1] != "stanza" {
		t.Fatalf("oneshot start order = %v, want [initdb stanza]", order)
	}
}

// TestChainBindingDependsOnEdgesExit verifies the additive `dependsOnEdges`
// field end-to-end through the real runtime: a dependent that waits for a
// oneshot to `exit` starts only after the oneshot exits 0.
func TestChainBindingDependsOnEdgesExit(t *testing.T) {
	order, err := runChainBinding(t, `
		const { chain } = require("ezx");
		chain.run({
			nodes: [
				{ name: "initdb", oneshot: true, process: { binaryPath: "/bin/true" } },
				{ name: "app", dependsOnEdges: [{ name: "initdb", waitFor: "exit" }], process: { binaryPath: "/bin/true" } },
			],
		});
	`)
	if err != nil {
		t.Fatalf("RunString: %v", err)
	}
	if len(order) != 2 || order[0] != "initdb" || order[1] != "app" {
		t.Fatalf("dependsOnEdges exit order = %v, want [initdb app]", order)
	}
}

// TestChainBindingDependsOnEdgesMixed verifies per-edge wait modes bind through
// the runtime: b (waits for a to `started`) and c (waits for a to `ready`) both
// start after a.
func TestChainBindingDependsOnEdgesMixed(t *testing.T) {
	order, err := runChainBinding(t, `
		const { chain } = require("ezx");
		chain.run({
			nodes: [
				{ name: "a", process: { binaryPath: "/bin/true" } },
				{ name: "b", dependsOnEdges: [{ name: "a", waitFor: "started" }], process: { binaryPath: "/bin/true" } },
				{ name: "c", dependsOnEdges: [{ name: "a", waitFor: "ready" }], process: { binaryPath: "/bin/true" } },
			],
		});
	`)
	if err != nil {
		t.Fatalf("RunString: %v", err)
	}
	if len(order) != 3 || order[0] != "a" {
		t.Fatalf("dependsOnEdges mixed order = %v, want a first with [b c] following", order)
	}
	seen := map[string]bool{}
	for _, n := range order[1:] {
		seen[n] = true
	}
	if !seen["b"] || !seen["c"] {
		t.Fatalf("dependsOnEdges mixed order = %v, want both b and c present", order)
	}
}

// TestChainBindingNeedParentReadyLegacy verifies the backward-compat form
// `dependsOn: ["a"]` + `needParentReady: true` still works end-to-end.
func TestChainBindingNeedParentReadyLegacy(t *testing.T) {
	order, err := runChainBinding(t, `
		const { chain } = require("ezx");
		chain.run({
			nodes: [
				{ name: "a", process: { binaryPath: "/bin/true" } },
				{ name: "b", dependsOn: ["a"], needParentReady: true, process: { binaryPath: "/bin/true" } },
			],
		});
	`)
	if err != nil {
		t.Fatalf("RunString: %v", err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("legacy needParentReady order = %v, want [a b]", order)
	}
}
