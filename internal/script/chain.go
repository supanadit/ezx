package script

import (
	"context"

	"github.com/supanadit/ezx/domain"
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
type ChainModule struct {
	ctx  context.Context
	svc  ChainRunner
}

// NewChainModule returns a ChainModule backed by the given runner,
// cancelling the running chain when ctx is cancelled.
func NewChainModule(ctx context.Context, svc ChainRunner) *ChainModule {
	return &ChainModule{ctx: ctx, svc: svc}
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
	return m.svc.Run(m.ctx, norm)
}
