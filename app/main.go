package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/labstack/echo/v4"
	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/health"
	jsengine "github.com/supanadit/ezx/internal/repository/js"
	"github.com/supanadit/ezx/internal/repository/logrotate"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/internal/rest"
	"github.com/supanadit/ezx/internal/script"
	"github.com/supanadit/ezx/internal/terminal"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/orchestrator"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/rotator"
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
	if err := reaper.Start(reaperCtx()); err != nil {
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
			system.NewAdapter,
			func() *system.Reaper { return reaper },
			func() *echo.Echo { return echo.New() },
			fx.Annotate(health.NewService, fx.As(new(domain.HealthService)), fx.As(new(rest.HealthService))),
			provideProcessFactory,
			provideScriptFactory,
			provideRotatorFactory,
			provideOrchestratorDeps,
			orchestrator.NewService,
			runtime.NewRegistry,
			// Scripting-language adapter: swap jsengine for a lua engine (and
			// its fx.As target stays runtime.Engine) to change languages.
			fx.Annotate(jsengine.NewEngine, fx.As(new(runtime.Engine))),
			fx.Annotate(runtime.NewService, fx.As(new(terminal.ScriptRunner))),
		),
		fx.Invoke(
			registerHostModules,
			wireLogRotator,
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

	if err := app.Start(startCtx(ctx)); err != nil {
		var ee *domain.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := app.Stop(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// reaperCtx returns the context the zombie reaper runs under. It MUST NOT be
// the signal context: the reaper's wait loop stops reaping when its context is
// cancelled, so binding it to SIGTERM disabled reaping at the exact moment
// shutdown needs it. The supervised child's exit then went unreaped (a
// persistent zombie), proc.Done() never fired, and every drain waited out its
// full timeout while the app had already exited. Reaping must outlive the
// signal — teardown is reaper.Stop, which main defers.
func reaperCtx() context.Context { return context.Background() }

// startCtx returns the context fx.Start runs under. fx aborts Start the moment
// that context is done, WITHOUT waiting for the OnStart hooks — and the CLI
// lives in one of those hooks (registerRootCmd → rootCmd.Execute, which blocks
// for the whole supervised chain). Handing fx the SIGTERM-derived context
// therefore made a signal abort startup instantly: main reached os.Exit while
// the chain was still draining, killing ezx and its children before the
// configured drain timeout could be honored. The signal context is still
// injected into scripts (fx.Provide above), so it still drives the chain's own
// cancellation and graceful drain; only fx's startup abort is disabled.
// WithoutCancel keeps fx.Start cancellable by its own hook errors while
// detaching it from the signal.
func startCtx(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// startHealthServer starts the process-wide health HTTP server. It listens
// ONLY when EZX_HEALTH_ADDR is set: a container entrypoint that silently bound
// an unexpected port (the old default, :8080) both clashed with the app's own
// listener and could not be probed from outside without publishing a port.
// With no EZX_HEALTH_ADDR the shared router still serves ezx.api routes
// in-process (tests, embedded use) but nothing is exposed on the network.
//
// A bind failure is fatal: silently running without the health endpoints, or
// without the script's user-defined routes, is worse than failing fast.
func startHealthServer(e *echo.Echo, lc fx.Lifecycle) {
	// Echo's ASCII banner belongs to a dev server, not a container entrypoint.
	e.HideBanner = true
	e.HidePort = true

	addr := os.Getenv("EZX_HEALTH_ADDR")
	if addr == "" {
		return
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("health server: listen %s: %w (set EZX_HEALTH_ADDR to a free address)", addr, err)
			}
			fmt.Fprintf(os.Stderr, "health server: listening on %s\n", ln.Addr())
			e.Listener = ln
			go func() {
				if err := e.Start(""); err != nil && err != http.ErrServerClosed {
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

// provideRotatorFactory provides the per-node app-owned log rotator used by the
// orchestrator for nodes that set logRotate.
func provideRotatorFactory(log logger.Logger) rotator.Factory {
	return func() rotator.Rotator { return logrotate.New(log) }
}

// provideOrchestratorDeps satisfies the supervisor's driven-side ports with the
// single OS adapter. Every port is the same concrete adapter, wired here (the
// composition root) as the only place that knows the concrete type.
func provideOrchestratorDeps(a *system.Adapter) orchestrator.Deps {
	return orchestrator.Deps{
		Files:   a,
		Args:    a,
		Exec:    a,
		Probes:  a,
		Signals: a,
		Cron:    a,
	}
}

// wireLogRotator installs the rotation factory on the supervisor. It is a
// setter (not a constructor parameter) so existing construction sites are
// unaffected when a node does not use logRotate.
func wireLogRotator(s *orchestrator.Service, f rotator.Factory) {
	s.SetRotator(f)
}

// registerHostModules wires the aggregate ezx host module into the script
// registry. It is pure composition: delivery types + module Ports only.
func registerHostModules(
	ctx context.Context,
	log logger.Logger,
	factory script.ProcessFactory,
	orch *orchestrator.Service,
	ready domain.HealthService,
	adapter *system.Adapter,
	e *echo.Echo,
	reg *runtime.Registry,
) {
	reg.Register("ezx", func(b runtime.Binder) any {
		return script.NewEzxModule(script.Deps{
			Ctx:       ctx,
			Log:       log,
			Proc:      factory,
			Execer:    adapter,
			Editors:   adapter,
			Probes:    adapter,
			Chain:     orch,
			Ready:     ready,
			Sched:     orch,
			Routes:    e,
			Callbacks: b.Invoker(),
			Gate:      b.Gate(),
		})
	})
}
