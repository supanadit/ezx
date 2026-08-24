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
	jsengine "github.com/supanadit/ezx/internal/repository/js"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/internal/rest"
	"github.com/supanadit/ezx/internal/script"
	"github.com/supanadit/ezx/internal/terminal"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/orchestrator"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/runtime"
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
		// Provider (not Supply): fx keys dependencies by the declared return
		// type, so this registers as context.Context; Supply would register
		// signal.NotifyContext's unexported *signal.signalCtx instead.
		fx.Provide(func() context.Context { return ctx }),
		fx.Provide(
			fx.Annotate(system.NewLogger, fx.As(new(logger.Logger))),
			func() *system.Reaper { return reaper },
			func() *echo.Echo { return echo.New() },
			fx.Annotate(health.NewService, fx.As(new(domain.HealthService))),
			provideProcessFactory,
			provideScriptFactory,
			orchestrator.NewService,
			runtime.NewRegistry,
			// Scripting-language adapter: swap jsengine for a lua engine (and
			// its fx.As target stays runtime.Engine) to change languages.
			fx.Annotate(jsengine.NewEngine, fx.As(new(runtime.Engine))),
			fx.Annotate(runtime.NewService, fx.As(new(terminal.ScriptRunner))),
		),
		fx.Invoke(
			registerHostModules,
			terminal.NewBootstrapHandler,
			rest.NewHealthHandler,
			// Start the health server BEFORE running the root command: the
			// bootstrap script blocks in chain.run, so a later OnStart would
			// never fire and the api routes (backup triggers, sync-reload,
			// role-change poller) would never be served.
			startHealthServer,
			registerRootCmd,
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

// provideScriptFactory provides the same per-node process factory typed for
// the script delivery, whose ProcessFactory is a distinct named type.
func provideScriptFactory(reaper *system.Reaper) script.ProcessFactory {
	return func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node, reaper)
	}
}

// registerHostModules wires the aggregate ezx host module into the script
// registry. It is pure composition: delivery types + module Ports only.
func registerHostModules(
	ctx context.Context,
	log logger.Logger,
	factory script.ProcessFactory,
	orch *orchestrator.Service,
	ready domain.HealthService,
	e *echo.Echo,
	reg *runtime.Registry,
) {
	reg.Register("ezx", func(b runtime.Binder) any {
		return script.NewEzxModule(script.Deps{
			Ctx:       ctx,
			Log:       log,
			Proc:      factory,
			Chain:     orch,
			Ready:     ready,
			Sched:     orch,
			Routes:    e,
			Callbacks: b.Invoker(),
		})
	})
}
