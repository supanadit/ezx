package terminal

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// ScriptRunner is the local Port for executing a user entrypoint script
// (R10). It is satisfied structurally by *runtime.Service; this handler never
// imports a concrete engine or scripting technology.
type ScriptRunner interface {
	// RunFile loads and executes the script at path.
	RunFile(ctx context.Context, path string) error
}

// BootstrapHandler wires the ezx bootstrap subcommand to the injected script
// runner. It does protocol marshaling only: argument parsing and console
// output. All orchestration happens inside the script via host modules.
type BootstrapHandler struct {
	runner ScriptRunner
}

// NewBootstrapHandler constructs the handler, registers the bootstrap
// subcommand on rootCmd, and is invoked by the DI container.
func NewBootstrapHandler(rootCmd *cobra.Command, runner ScriptRunner) {
	h := &BootstrapHandler{runner: runner}
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

// run executes the user script. The command context carries the app-level
// cancellation signal.
func (h *BootstrapHandler) run(cmd *cobra.Command, args []string) error {
	path := args[0]
	fmt.Printf("🚀 EZX bootstrapping %s\n", path)
	if err := h.runner.RunFile(cmd.Context(), path); err != nil {
		fmt.Println("❌ EZX failed:", err)
		return err
	}
	fmt.Println("✅ EZX bootstrap complete")
	return nil
}
