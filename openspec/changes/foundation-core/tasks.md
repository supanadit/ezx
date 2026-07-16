## 1. Domain types and infrastructure

- [ ] 1.1 Extend `domain/` with `Config`, `Stage`, `Service`, `ReadinessProbe`, `Template`, `FileSpec`, `ShutdownPlan`, `HealthCheck` types
- [ ] 1.2 Add `internal/appctx/appctx.go` with `AppContext{Ctx context.Context}` (mirror `phpv`)
- [ ] 1.3 Add `internal/shutdown/shutdown.go` with `Manager` that returns a signal-driven `context.Context` and `Wait()` blocks until signal (mirror `phpv`)
- [ ] 1.4 Add `internal/terminal/` for cobra command handlers (CLI layer, mirror `phpv`)

## 2. fx wiring layer

- [ ] 2.1 Add `go.uber.org/fx`, `go.uber.org/zap`, `go.uber.org/dig`, `github.com/spf13/cobra` to `go.mod`
- [ ] 2.2 Rewrite `app/main.go` to match `phpv` pattern:
  - `shutdown.New(...)` creates the context
  - `fx.Supply(Version)` and `fx.Supply(appctx.AppContext{Ctx: shutdownCtx})`
  - `fx.Annotate(concrete, fx.As(new(Interface)))` for each repository
  - `fx.Invoke(RegisterRootCmd)` runs the root cobra command in `OnStart`
  - `app.Start(ctx)` blocks, `app.Stop(ctx)` cleans up on signal
- [ ] 2.3 Add a `NewRootCmd()` factory returning `*cobra.Command` (mirror `phpv`)
- [ ] 2.4 Add a `RegisterRootCmd(rootCmd, lc)` that wires `OnStart: rootCmd.Execute()` (mirror `phpv`)

## 3. config-loader capability

- [ ] 3.1 Implement `loader/` use case with Viper-based YAML reader
- [ ] 3.2 Implement strict env schema validation: reject unknown env vars
- [ ] 3.3 Add `internal/terminal/validate.go` handler with `ezx validate --config <file>` subcommand
- [ ] 3.4 Add loader tests covering: valid YAML, missing file, unknown env var, missing required key

## 4. process-orchestrator capability

- [ ] 4.1 Implement `internal/repository/system/process_io.go`: `Start`, `Stop`, `Wait`, `Signal` as pure I/O behind a `ProcessIORepository` interface
- [ ] 4.2 Implement `internal/repository/system/probe.go` with three types: `tcp`, `exec`, `http` behind a `ProbeRepository` interface
- [ ] 4.3 Add `orchestrator/` use case: start roots in parallel via fx-injected `ProcessIORepository`
- [ ] 4.4 Add readiness gating: start children only after parent's probe passes
- [ ] 4.5 Add reverse-DAG drain on `SIGTERM`/`SIGINT` (subscribe to `appctx.AppContext.Ctx.Done()`)
- [ ] 4.6 Add `shutdownTimeoutSeconds` config with `SIGKILL` escalation
- [ ] 4.7 Add `internal/terminal/run.go` handler with `ezx run --config <file>` subcommand
- [ ] 4.8 Add orchestrator tests: single root, two roots, child gating, graceful drain, forced shutdown

## 5. health-check capability

- [ ] 5.1 Implement `healthcheck/` use case: run configured checks in sequence
- [ ] 5.2 Add `internal/terminal/healthcheck.go` handler with `ezx healthcheck --config <file>` subcommand
- [ ] 5.3 Map health checks to readiness probe types
- [ ] 5.4 Exit 0 if all pass, 1 if any fail
- [ ] 5.5 Add healthcheck tests: pass, fail, multiple checks

## 6. telemetry capability

- [ ] 6.1 Implement `telemetry/` use case with `go.uber.org/zap` JSON handler
- [ ] 6.2 Add `EZX_LOG_LEVEL` env var support
- [ ] 6.3 Add `prometheus/client_golang` to `go.mod`
- [ ] 6.4 Implement `ezx_process_up`, `ezx_process_ready`, `ezx_healthcheck_ok`, `ezx_shutdown_duration_seconds` metrics
- [ ] 6.5 Expose `/metrics` on port 9090 (configurable via `telemetry.port`)
- [ ] 6.6 Support `telemetry.enabled: false` to disable
- [ ] 6.7 Add telemetry tests: log format, metric exposure, port config

## 7. template-rendering capability

- [ ] 7.1 Implement `renderer/` use case with `text/template`
- [ ] 7.2 Support `{{ .Env.NAME }}` env substitution
- [ ] 7.3 Implement atomic writes: temp file + fsync + rename
- [ ] 7.4 Honor declared file `mode`
- [ ] 7.5 Add `internal/repository/system/file.go` for `FileRepository` interface (atomic write, chown, chmod)
- [ ] 7.6 Add renderer tests: env substitution, missing env, atomic write, mode application

## 8. build-step capability

- [ ] 8.1 Implement `setup/` use case: run `setup.steps[]` serially via `sh -c`
- [ ] 8.2 Add `internal/terminal/setup.go` handler with `ezx setup --config <file>` subcommand
- [ ] 8.3 Inherit container env without filtering
- [ ] 8.4 Abort on first non-zero exit
- [ ] 8.5 Add setup tests: ordered run, failure aborts, env inheritance

## 9. Minimal example

- [ ] 9.1 Create `examples/docker/minimal/` with `ezx.setup.yaml`, `ezx.runtime.yaml`, `Dockerfile`
- [ ] 9.2 Implement a tiny echo server in Python
- [ ] 9.3 Add a log tailer sidecar with `needParentReady: true`
- [ ] 9.4 Declare a TCP readiness probe on the echo server
- [ ] 9.5 Declare a TCP health check
- [ ] 9.6 Add `HEALTHCHECK CMD` in the Dockerfile
- [ ] 9.7 Verify the image builds and runs end-to-end
- [ ] 9.8 Verify `docker stop` triggers graceful shutdown within timeout
- [ ] 9.9 Verify `curl localhost:9090/metrics` returns documented metrics

## 10. Documentation and CI

- [ ] 10.1 Document `examples/docker/minimal/` as the first reference image
- [ ] 10.2 Add a README section explaining the env-only contract
- [ ] 10.3 Add CI job: `go test ./...`
- [ ] 10.4 Add CI job: build the minimal example image
- [ ] 10.5 Add CI job: run the minimal example container and curl `/metrics`
