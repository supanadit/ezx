package script

import (
	"context"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/runtime"
)

// ChainRunner is the local Port for running a process chain (R10). It is
// satisfied structurally by *orchestrator.Service; delivery never imports the
// concrete service type.
type ChainRunner interface {
	// Run executes the chain, supervising its full dependency graph.
	Run(ctx context.Context, chain domain.ProcessChain) error
}

// ChainModule exposes ezx.chain: run(chain) runs a declarative process graph via
// the orchestrator supervisor. It is the optional high-level helper — scripts
// may use it instead of manually orchestrating with process.spawn. It carries
// the script's cancellation context so a running chain is drained when the app
// shuts down (e.g. SIGTERM).
//
// chain.run is the main "park window": it blocks for as long as the chain is
// supervised, which is exactly when node lifecycle callbacks (onStart,
// onReady, onExit, readinessFunc) and scheduler ticks fire. It therefore parks
// the scripting engine for its whole duration, so those callbacks are admitted
// to the single-threaded VM while the script is suspended instead of racing a
// live script frame.
type ChainModule struct {
	ctx  context.Context
	svc  ChainRunner
	gate runtime.Gate
}

// NewChainModule returns a ChainModule backed by the given runner,
// cancelling the running chain when ctx is cancelled. gate is the scripting
// engine's callback gate; it may be nil when the engine cannot call back.
func NewChainModule(ctx context.Context, svc ChainRunner, gate runtime.Gate) *ChainModule {
	return &ChainModule{ctx: ctx, svc: svc, gate: gate}
}

// Run executes the given ProcessChain, supervising its full dependency graph.
// It first normalizes the chain (desugaring the legacy Roots/Children tree into
// the canonical flat Nodes/DependsOn form) and validates it, so the
// orchestrator always receives a valid flat DAG and can focus purely on
// coordination. Validation errors (cycles, unknown deps, exec restrictions, …)
// surface to the script.
func (m *ChainModule) Run(chain domain.ProcessChain) error {
	norm, err := chain.Normalized()
	if err != nil {
		return err
	}
	if err := domain.ValidateChain(norm); err != nil {
		return err
	}
	if m.gate != nil {
		m.gate.Park()
		defer m.gate.Unpark()
		// Route the chain's script callbacks through the engine's admission
		// control, so a single-threaded VM is entered only while this call is
		// parked (the whole supervised duration below).
		norm.Gate = m.gate.Do
	}
	return m.svc.Run(m.ctx, norm)
}
