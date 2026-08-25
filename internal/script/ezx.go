package script

import (
	"context"

	"github.com/labstack/echo/v4"

	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/runtime"
)

// Deps bundles the services the aggregate ezx host module exposes to scripts.
// All fields are language-neutral Ports declared by this package (or by the
// owning modules); no scripting-engine type appears here, so the same
// delivery code serves goja today and lua or other engines tomorrow.
type Deps struct {
	// Ctx is the app-level cancellation context for the running script; it
	// interrupts spawned processes and running chains on SIGTERM.
	Ctx context.Context
	// Log is the structured logger exposed as ezx.log.
	Log logger.Logger
	// Proc spawns per-node process handles for ezx.process.
	Proc ProcessFactory
	// Chain runs declarative process trees for ezx.chain (may be nil).
	Chain ChainRunner
	// Ready flips the readiness state surfaced by /readyz (may be nil).
	Ready Readiness
	// Sched triggers and observes scheduled nodes (may be nil).
	Sched SchedulerControl
	// Routes is the shared HTTP router for ezx.api (nil = no server).
	Routes *echo.Echo
	// Callbacks invokes script-provided functions for ezx.api handlers
	// (nil = engine without callback support).
	Callbacks runtime.Invoker
}

// EzxModule is the aggregate module exposed to scripts as require("ezx"). It
// bundles the individual host modules under one namespace:
//
//	const { env, editor, process, log, chain, fs } = require("ezx");
type EzxModule struct {
	Env       *EnvModule       `goja:"env"`
	Editor    *EditorModule    `goja:"editor"`
	Process   *ProcessModule   `goja:"process"`
	Log       *LogModule       `goja:"log"`
	Chain     *ChainModule     `goja:"chain"`
	FS        *FSModule        `goja:"fs"`
	Health    *HealthModule    `goja:"health"`
	Probe     *ProbeModule     `goja:"probe"`
	Scheduler *SchedulerModule `goja:"scheduler"`
	API       *ApiModule       `goja:"api"`
	YAML      *YamlModule      `goja:"yaml"`
	Config    *ConfigModule    `goja:"config"`
	Shell     *ShellModule     `goja:"shell"`
}

// NewEzxModule builds the aggregate ezx module from the given dependencies.
func NewEzxModule(d Deps) *EzxModule {
	return &EzxModule{
		Env:       NewEnvModule(),
		Editor:    NewEditorModule(),
		Process:   NewProcessModule(d.Ctx, d.Proc, d.Callbacks),
		Log:       NewLogModule(d.Log),
		Chain:     NewChainModule(d.Ctx, d.Chain),
		FS:        NewFSModule(),
		Health:    NewHealthModule(d.Ready),
		Probe:     NewProbeModule(d.Ctx),
		Scheduler: NewSchedulerModule(d.Sched, d.Callbacks),
		API:       NewApiModule(d.Routes, d.Callbacks),
		YAML:      NewYamlModule(),
		Config:    NewConfigModule(),
		Shell:     NewShellModule(),
	}
}
