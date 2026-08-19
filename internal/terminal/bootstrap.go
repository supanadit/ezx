package terminal

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/orchestrator"
	"github.com/supanadit/ezx/script"
	scriptmodules "github.com/supanadit/ezx/script/modules"
)

// BootstrapHandler wires the ezx bootstrap subcommand to the injected
// Services. It constructs the aggregate host module and runs the user script
// against the script engine, delegating all process orchestration to the
// injected Services.
type BootstrapHandler struct {
	proc     scriptmodules.ProcessFactory
	orch     *orchestrator.Service
	registry *script.Registry
	engine   script.ScriptEngine
	log      logger.Logger
	health   domain.HealthService
}

// NewBootstrapHandler constructs the handler, registers the bootstrap
// subcommand on rootCmd, and is invoked by the DI container.
func NewBootstrapHandler(
	rootCmd *cobra.Command,
	proc scriptmodules.ProcessFactory,
	orch *orchestrator.Service,
	registry *script.Registry,
	engine script.ScriptEngine,
	log logger.Logger,
	health domain.HealthService,
) {
	h := &BootstrapHandler{
		proc:     proc,
		orch:     orch,
		registry: registry,
		engine:   engine,
		log:      log,
		health:   health,
	}
	rootCmd.AddCommand(h.bootstrapCmd())
}

// bootstrapCmd returns the ezx bootstrap <script.js> subcommand.
func (h *BootstrapHandler) bootstrapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap <script.js>",
		Short: "Run a JavaScript bootstrap script",
		Long: `Run a user-supplied JavaScript entrypoint script against the ezx
host API (require("ezx")). The script can provision files, build arguments,
spawn processes, and drive the orchestrator.`,
		Args: cobra.ExactArgs(1),
		RunE: h.run,
	}
}

// run loads the module registry, builds the aggregate ezx host module, and
// executes the script against the engine. The command context carries the
// app-level cancellation signal.
func (h *BootstrapHandler) run(cmd *cobra.Command, args []string) error {
	path := args[0]

	h.registry.Register("ezx", func() any {
		return scriptmodules.NewEzxModule(cmd.Context(), h.log, h.proc, h.orch, h.health)
	})

	fmt.Printf("🚀 EZX bootstrapping %s\n", path)
	if err := h.engine.RunFile(cmd.Context(), path); err != nil {
		fmt.Println("❌ EZX failed:", err)
		return err
	}
	fmt.Println("✅ EZX bootstrap complete")
	return nil
}
