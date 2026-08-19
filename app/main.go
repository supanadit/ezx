package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/internal/terminal"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/orchestrator"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/script"
	scriptmodules "github.com/supanadit/ezx/script/modules"
)

func main() {
	// Derive a context cancelled by SIGINT/SIGTERM. It is attached to the root
	// cobra command; cancelling it interrupts a running bootstrap script.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rootCmd := terminal.NewRootCmd()
	rootCmd.SetContext(ctx)

	app := fx.New(
		fx.NopLogger,
		fx.Supply(rootCmd),
		fx.Provide(
			fx.Annotate(system.NewLogger, fx.As(new(logger.Logger))),
			provideProcessFactory,
			provideScriptFactory,
			orchestrator.NewService,
			script.NewRegistry,
			fx.Annotate(system.NewScriptEngine, fx.As(new(script.ScriptEngine))),
			scriptmodules.NewEzxModule,
		),
		fx.Invoke(terminal.NewBootstrapHandler, registerRootCmd),
	)

	if err := app.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := app.Stop(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// registerRootCmd runs the root command once the fx app starts.
func registerRootCmd(rootCmd *cobra.Command, lc fx.Lifecycle) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return rootCmd.Execute()
		},
	})
}

// provideProcessFactory provides the per-node process factory used by the
// orchestrator.
func provideProcessFactory() orchestrator.ProcessFactory {
	return func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node)
	}
}

// provideScriptFactory provides the same per-node process factory typed for the
// script host module, whose ProcessFactory is a distinct named type.
func provideScriptFactory() scriptmodules.ProcessFactory {
	return func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node)
	}
}
