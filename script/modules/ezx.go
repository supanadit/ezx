package scriptmodules

import (
	"context"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/orchestrator"
)

// EzxModule is the aggregate module exposed to scripts as require("ezx"). It
// bundles the individual host modules under one namespace:
//
//	const { env, editor, process, log, chain, fs } = require("ezx");
type EzxModule struct {
	Env     *EnvModule     `goja:"env"`
	Editor  *EditorModule  `goja:"editor"`
	Process *ProcessModule `goja:"process"`
	Log     *LogModule     `goja:"log"`
	Chain   *ChainModule   `goja:"chain"`
	FS      *FSModule      `goja:"fs"`
	Health  *HealthModule  `goja:"health"`
	Probe   *ProbeModule   `goja:"probe"`
}

// NewEzxModule builds the aggregate ezx module from the given dependencies.
// The context is the app-level cancellation context for the running script; it
// is threaded into the process and chain modules so SIGTERM can interrupt
// spawned processes and the orchestrator.
func NewEzxModule(
	ctx context.Context,
	log logger.Logger,
	factory ProcessFactory,
	orch *orchestrator.Service,
	health domain.HealthService,
) *EzxModule {
	return &EzxModule{
		Env:     NewEnvModule(),
		Editor:  NewEditorModule(),
		Process: NewProcessModule(ctx, factory),
		Log:     NewLogModule(log),
		Chain:   NewChainModule(ctx, orch),
		FS:      NewFSModule(),
		Health:  NewHealthModule(health),
		Probe:   NewProbeModule(ctx),
	}
}
