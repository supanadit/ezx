package scriptmodules

import (
	"context"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/orchestrator"
)

// ChainModule exposes ezx.chain: run(chain) runs a declarative process tree via
// the orchestrator supervisor. It is the optional high-level helper — scripts
// may use it instead of manually orchestrating with process.spawn. It carries
// the script's cancellation context so a running chain is drained when the app
// shuts down (e.g. SIGTERM).
type ChainModule struct {
	ctx  context.Context
	orch *orchestrator.Service
}

// NewChainModule returns a ChainModule backed by the given orchestrator,
// cancelling the running chain when ctx is cancelled.
func NewChainModule(ctx context.Context, orch *orchestrator.Service) *ChainModule {
	return &ChainModule{ctx: ctx, orch: orch}
}

// Run executes the given ProcessChain, supervising its full dependency tree.
func (m *ChainModule) Run(chain domain.ProcessChain) error {
	return m.orch.Run(m.ctx, chain)
}
