package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/labstack/echo/v4"
	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/health"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/internal/rest"
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

	// The process-wide reaper (PID 1 init role): installs the subreaper and
	// reaps zombies. Started immediately (before fx's OnStart runs the CLI) so
	// spawned processes never hang waiting on an unstarted reaper.
	reaper := system.NewReaper(func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[reaper] "+format+"\n", args...)
	})
	if err := reaper.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "failed to start reaper:", err)
		os.Exit(1)
	}
	defer reaper.Stop()

	app := fx.New(
		fx.NopLogger,
		fx.Supply(rootCmd),
		fx.Provide(
			fx.Annotate(system.NewLogger, fx.As(new(logger.Logger))),
			func() *system.Reaper { return reaper },
			func() *echo.Echo { return echo.New() },
			fx.Annotate(health.NewService, fx.As(new(domain.HealthService))),
			provideProcessFactory,
			provideScriptFactory,
			orchestrator.NewService,
			script.NewRegistry,
			fx.Annotate(system.NewScriptEngine, fx.As(new(script.ScriptEngine))),
			scriptmodules.NewEzxModule,
		),
		fx.Invoke(
			terminal.NewBootstrapHandler,
			registerRootCmd,
			rest.NewHealthHandler,
			startHealthServer,
		),
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

// startHealthServer starts the process-wide health HTTP server when
// EZX_HEALTH_ADDR is set.
func startHealthServer(e *echo.Echo, lc fx.Lifecycle) {
	addr := os.Getenv("EZX_HEALTH_ADDR")
	if addr == "" {
		return
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go func() {
				if err := e.Start(addr); err != nil && err.Error() != "http: Server closed" {
					fmt.Fprintln(os.Stderr, "health server:", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return e.Close()
		},
	})
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
func provideProcessFactory(reaper *system.Reaper) orchestrator.ProcessFactory {
	return func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node, reaper)
	}
}

// provideScriptFactory provides the same per-node process factory typed for the
// script host module, whose ProcessFactory is a distinct named type.
func provideScriptFactory(reaper *system.Reaper) scriptmodules.ProcessFactory {
	return func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node, reaper)
	}
}
