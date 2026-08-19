package scriptmodules

import (
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/orchestrator"
)

// EzxModule is the aggregate module exposed to scripts as require("ezx"). It
// bundles the individual host modules under one namespace:
//
//	const { env, editor, process, log, chain } = require("ezx");
type EzxModule struct {
	Env     *EnvModule
	Editor  *EditorModule
	Process *ProcessModule
	Log     *LogModule
	Chain   *ChainModule
}

// NewEzxModule builds the aggregate ezx module from the given dependencies.
func NewEzxModule(
	log logger.Logger,
	factory ProcessFactory,
	orch *orchestrator.Service,
) *EzxModule {
	return &EzxModule{
		Env:     NewEnvModule(),
		Editor:  NewEditorModule(),
		Process: NewProcessModule(factory),
		Log:     NewLogModule(log),
		Chain:   NewChainModule(orch),
	}
}
