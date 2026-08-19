package scriptmodules

import (
	"context"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/orchestrator"
)

// ChainModule exposes ezx.chain: run(chain) runs a declarative process tree via
// the orchestrator supervisor. It is the optional high-level helper — scripts
// may use it instead of manually orchestrating with process.spawn.
type ChainModule struct {
	orch *orchestrator.Service
}

// NewChainModule returns a ChainModule backed by the given orchestrator.
func NewChainModule(orch *orchestrator.Service) *ChainModule {
	return &ChainModule{orch: orch}
}

// Run executes the given ProcessChain, supervising its full dependency tree.
func (m *ChainModule) Run(chain domain.ProcessChain) error {
	return m.orch.Run(context.Background(), chain)
}
