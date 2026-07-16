## Context

`ezx` replaces the existing `container-scripts` bash tree. Today the scripts
implement process orchestration, file rendering, env transformation, and
service-specific behavior (PostgreSQL) in shell, with state communicated
through sidecar files like `.pg_hba_env_entries`. The result is hard to
test, hard to reason about, and hard to extend.

`DESIGN.md` is the full design reference. `proposal/01-foundation-core.md`
defines the v0.1 milestone: a service-agnostic orchestrator that runs a
process DAG from a two-stage YAML, validates env vars, renders templates,
emits Prometheus metrics, and shuts down gracefully.

The ezx Go code already has a clean architecture skeleton (mirroring
`phpv`): `domain/` types, `internal/repository/{system,memory}/` I/O
layers, and use-case packages (`process/`, `license/`, `sbom/`). What it
lacks is the wiring layer: a `go.uber.org/fx` graph in `app/main.go`, a
shutdown manager, a CLI built on `spf13/cobra`, and the new use cases
(`orchestrator/`, `healthcheck/`, `telemetry/`, `setup/`, `loader/`,
`renderer/`, `probe/`, `process_io/`).

This change ships the v0.1 foundation from `DESIGN.md` §3, §4, §6, §7, §8,
§9, §10, §17 (basic), §19, and §20 (basic), using the same composition
pattern that `phpv` already uses successfully.

## Goals / Non-Goals

**Goals:**

- Ship a binary that runs a process DAG from `ezx.runtime.yaml` and
  `ezx.setup.yaml`.
- Validate YAML + env schema strictly; reject unknown env vars.
- Render template files with env substitution and atomic writes.
- Run build steps serially from `setup.steps[]`.
- Emit structured JSON logs and Prometheus metrics on port 9090.
- Perform graceful reverse-DAG drain on `SIGTERM` / `SIGINT` within a
  configurable timeout.
- Provide a minimal example that builds and runs end-to-end.
- Mirror the `phpv` package layout and fx wiring so anyone familiar with
  `phpv` can read `ezx` immediately.
- Keep the core free of any service-specific knowledge (no PostgreSQL).

**Non-Goals:**

- Scheduler, reconciler, wildcard config, conflict detection
  (v0.2 — `proposal/02-runtime-extensions.md`).
- Source strategies, registry, per-step Dockerfile caching
  (v0.3 — `proposal/03-build-system.md`).
- Security hardening beyond sane defaults, OpenTelemetry, eBPF, operational
  API, env scoping (v0.4 — `proposal/04-security-observability-operations.md`).
- Plugin system, marketplace (v2.0 — `proposal/05-plugin-ecosystem.md`).
- PostgreSQL, PgBouncer, pgBackRest (v2.1 — `proposal/06-postgresql-extension.md`).

## Architectural shape

The package layout follows `phpv` exactly:

```
app/                              # main.go: fx wiring + cobra root cmd
domain/                           # pure data types, no behavior
internal/
  appctx/                         # AppContext{Ctx context.Context}
  shutdown/                       # signal-driven manager, returns ctx
  terminal/                       # cobra command handlers (CLI layer)
  repository/
    system/                       # real I/O (process, file, probe, network)
    memory/                       # in-memory I/O (tests, dry-run)
orchestrator/                     # use case: DAG executor
healthcheck/                      # use case: global health
telemetry/                        # use case: logs + Prometheus
setup/                            # use case: serial build steps
loader/                           # use case: YAML + env validation
renderer/                         # use case: template rendering
```

Rules:

1. **`domain/`** contains only data types and interface contracts. No
   imports from `internal/`, `orchestrator/`, or any use-case package.
2. **Use cases** depend on `domain/` and on `internal/repository/...`
   interfaces. They MUST NOT import each other.
3. **`internal/repository/`** contains concrete I/O implementations bound
   to use-case interfaces via `fx.Annotate(..., fx.As(new(Interface)))`.
4. **`internal/terminal/`** is the CLI layer. Each Handler is a struct
   that takes the use-case services it needs and registers cobra
   subcommands on the root command.
5. **`app/main.go`** builds the fx graph, supplies constants and the
   `AppContext`, invokes the root command, and triggers shutdown on
   signal.

## Decisions

### 1. Use `go.uber.org/fx` for composition (matching `phpv`)

`phpv/app/phpv.go` already demonstrates the exact pattern we want:

- `shutdown.New(...)` creates a context tied to `SIGINT`/`SIGTERM`/
  `SIGHUP`.
- `fx.Supply(appctx.AppContext{Ctx: shutdownCtx})` propagates the
  context.
- `fx.Annotate(concrete, fx.As(new(Interface)))` binds concrete
  implementations to interface contracts.
- `fx.Invoke(RegisterRootCmd)` runs the root cobra command in
  `OnStart`.
- `app.Start(ctx)` blocks until shutdown, then `app.Stop(ctx)` cleans
  up.

**Alternatives considered:** plain `main()` with manual wiring, `samber/do`,
`google/wire`. fx wins because it is the same tool `phpv` already uses,
supports graceful shutdown out of the box, and makes the dependency
graph explicit and reviewable.

### 2. CLI via `spf13/cobra`, handler-per-group pattern

The CLI is built with `spf13/cobra` (same as `phpv`). Each command group
lives in its own file under `internal/terminal/` and is a struct that
takes the use-case services it needs:

```go
type RunHandler struct {
    orchestratorSvc *orchestrator.Service
    telemetrySvc    *telemetry.Service
    ac              appctx.AppContext
}

func NewRunHandler(
    rootCmd *cobra.Command,
    ac appctx.AppContext,
    orchestratorSvc *orchestrator.Service,
    telemetrySvc *telemetry.Service,
) {
    h := &RunHandler{ac: ac, orchestratorSvc: orchestratorSvc, telemetrySvc: telemetrySvc}
    rootCmd.AddCommand(h.runCmd())
}
```

**Alternatives considered:** a single `cli.go` with all commands, or
`urfave/cli`. The handler-per-group pattern matches `phpv` and keeps
each file focused on one command family.

### 3. Viper for YAML loading, custom env schema validation

We use `github.com/spf13/viper` to read YAML files and apply env
overrides, but we do **not** trust Viper's `AutomaticEnv()` because it
silently accepts unknown keys. Instead, we read the `envSchema` block
from the YAML, build a map of allowed env var names, and reject any env
var that is not in the schema. This is the "100% env-only contract"
from `DESIGN.md` §2.

**Alternatives considered:** `gopkg.in/yaml.v3` + a hand-rolled env
merger, `caarlos0/env`, `kelseyhightower/envconfig`. Viper gives us
file watching and consistent precedence rules; the custom env schema
check gives us strictness.

### 4. Three readiness probe types: `tcp`, `exec`, `http`

The abstract `ReadinessProbe` type carries `Type` + `Config
map[string]any`. The probe implementations live in
`internal/repository/system/probe.go`. PostgreSQL-specific probes
(`pg_isready`, `SHOW POOLS`) ship later as a plugin.

**Alternatives considered:** only `exec`, only `http`. `tcp` covers
common cases cheaply; `exec` covers custom readiness scripts; `http`
covers HTTP services. The three are the minimum useful set.

### 5. Reverse-DAG drain on shutdown

When `ezx` receives `SIGTERM` or `SIGINT`, the orchestrator stops
processes in reverse topological order (children first, parents last)
with a configurable timeout (`shutdownTimeoutSeconds`, default 30s).
After the timeout, `SIGKILL` is sent.

**Alternatives considered:** stop all in parallel, stop in declaration
order, only send `SIGTERM` (no escalation). Reverse-DAG order matches
service dependency direction; the `SIGKILL` escalation prevents hangs.

### 6. Prometheus metrics on port 9090, optional

The metrics endpoint is started on `localhost:9090/metrics` by default.
It can be disabled by setting `telemetry.enabled: false` in the runtime
YAML. The port is configurable via `telemetry.port`.

**Alternatives considered:** always on, expose on a different port.
Always on forces every image to expose port 9090; exposing on a
different port breaks the Prometheus convention.

### 7. Structured JSON logs via `go.uber.org/zap`

We use `go.uber.org/zap` for structured JSON logging, matching
`phpv`'s logging choice. Log level is configurable via `EZX_LOG_LEVEL`
(`debug`, `info`, `warn`, `error`). Hot reload of log config ships in
v0.4.

**Alternatives considered:** `log/slog` (stdlib), `rs/zerolog`. We
choose zap because it matches `phpv` and is already battle-tested in
that codebase. The fx integration is well-known (e.g.
`go.uber.org/fx/zap`).

### 8. Single global healthcheck command

`ezx healthcheck` is a single binary invocation (not a long-running
process). It runs all configured health checks in sequence, returns
exit code 0 if all pass and 1 if any fail. Docker's `HEALTHCHECK` calls
this command on its schedule.

**Alternatives considered:** a long-running healthcheck sidecar. The
single-command model is simpler, matches Docker's `HEALTHCHECK` design,
and avoids a second process to manage.

### 9. Minimal example: an echo server + sidecar

The `examples/docker/minimal/` example runs an echo server (parent)
and a log tailer sidecar (child with `needParentReady: true`). It uses
`nc` as the readiness probe (`tcp` type). It exercises every core
feature (parallel roots, readiness, child gating, env-only config,
metrics) without any service-specific knowledge.

**Alternatives considered:** a real HTTP server (e.g. Caddy). The
echo server is a 10-line Python script that has no external
dependencies and exercises the readiness probe correctly.

## Risks / Trade-offs

- **fx learning curve** → document the fx graph in `app/main.go`
  comments; reuse `phpv`'s pattern as the reference.
- **Viper is heavy** → profile startup; if it is slow, switch to
  `gopkg.in/yaml.v3` + a small custom merger.
- **Reverse-DAG drain can hang** → enforce `shutdownTimeoutSeconds`
  with `SIGKILL` escalation; log the timeout clearly.
- **Metrics port 9090 conflicts** → make it configurable; document the
  default in the example.
- **Process reaper in containers** → if PID 1 does not reap zombies,
  the container can fill its PID table. We will test this with `tini`
  and document the requirement.
- **Mixing two clean-architecture styles** → if we deviate from
  `phpv`'s shape in subtle ways (e.g. different interface naming,
  different repository pattern), readers will be confused. Review every
  PR against `phpv`'s pattern.

## Migration Plan

This is the v0.1 phase. There is nothing to migrate from yet.

1. Ship the binary.
2. Build the minimal example.
3. Document `examples/docker/minimal/` as the first reference image.
4. Later phases (v0.2–v2.1) build on top of this foundation.

## Open Questions

- Should `setup.steps[]` support parallel execution where the DAG
  allows in v1, or keep it strictly serial? (Carried from
  `proposal/01`.)
- Should `process.environment` be supported in v1, or only container
  env inheritance? (Carried from `proposal/01`.)
- Default metrics port: 9090 or configurable via `EZX_METRICS_PORT`?
  (Decided: configurable via `telemetry.port` in YAML, with 9090
  default.)
- Should `ezx validate` be a separate command or a flag on `ezx run`?
  (Decided: separate command, so validation can run in CI without
  starting the orchestrator.)
