package script

import (
	"context"

	"github.com/supanadit/ezx/domain"
)

// ChainRunner is the local Port for running a process chain (R10). It is
// satisfied structurally by *orchestrator.Service; delivery never imports the
// concrete service type.
type ChainRunner interface {
	// Run executes the chain, supervising its full dependency tree.
	Run(ctx context.Context, chain domain.ProcessChain) error
}

// ChainModule exposes ezx.chain: run(chain) runs a declarative process tree via
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

// Run executes the given ProcessChain, supervising its full dependency tree.
func (m *ChainModule) Run(chain domain.ProcessChain) error {
	return m.svc.Run(m.ctx, chain)
}
