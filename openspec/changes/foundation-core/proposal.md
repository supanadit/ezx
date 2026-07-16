## Why

The current `container-scripts` bash tree has grown into an unmaintainable
matrix of `pg_hba_env_entries`, sidecar files, and implicit ordering between
scripts. We need a small, stable binary that runs a process DAG from a single
source of truth (two-stage YAML) with env-driven config, telemetry, and
graceful shutdown — **before** we touch any service-specific knowledge like
PostgreSQL. This is the v0.1 milestone that everything else builds on.

The composition pattern mirrors the existing `phpv` project: a
`go.uber.org/fx` graph in `app/main.go`, a shutdown manager that produces
a `context.Context`, an `internal/appctx` package that propagates it, a
`spf13/cobra` CLI built handler-per-group in `internal/terminal/`, and
clean architecture with `domain/` types, `internal/repository/{system,memory}/`
I/O layers, and use-case packages. Anyone familiar with `phpv` can read
`ezx` immediately.

## What Changes

- New `ezx` binary that loads two YAML files (`ezx.setup.yaml`,
  `ezx.runtime.yaml`) and runs the configured process DAG.
- New `domain` types for config, stages, processes, readiness probes,
  templates, and shutdown plans.
- New `orchestrator/` use case that starts roots in parallel, waits for
  readiness, walks `needParentReady` children, and performs a reverse-DAG
  drain on shutdown.
- New `healthcheck/` use case that exits 0/1 from a single configurable
  check (the container's `HEALTHCHECK` entrypoint).
- New `telemetry/` package with structured JSON logs and Prometheus metrics
  (`ezx_process_up`, `ezx_process_ready`, `ezx_healthcheck_ok`,
  `ezx_shutdown_duration_seconds`).
- New `setup/` use case that runs build-time steps serially.
- New `internal/loader/` with strict YAML + env schema validation (Viper →
  typed `domain.Config`).
- New `internal/renderer/` for `text/template` rendering with env and values
  substitution.
- New `internal/repository/system/` with pure I/O: process start/stop/signal,
  atomic file writes, three probe types (`tcp`, `exec`, `http`).
- New CLI commands: `ezx setup`, `ezx run`, `ezx healthcheck`, `ezx validate`.
- New minimal example at `examples/docker/minimal/`.

## Capabilities

### New Capabilities

- `config-loader`: YAML and env schema validation; env-only contract;
  `ezx validate` rejects unknown env vars.
- `process-orchestrator`: DAG execution with parallel roots, readiness gates,
  reverse-DAG drain on shutdown.
- `health-check`: per-process health checks; single global check command for
  the container `HEALTHCHECK`.
- `telemetry`: structured JSON logs and Prometheus metrics on
  `localhost:9090/metrics`.
- `template-rendering`: file rendering with `text/template`, env substitution,
  atomic writes.
- `build-step`: serial build-time step runner for `setup.steps[]`.

### Modified Capabilities

None — this is the first phase. No existing specs to modify.

## Impact

- New packages under `domain/`, `orchestrator/`, `healthcheck/`, `telemetry/`,
  `setup/`, `internal/loader/`, `internal/renderer/`,
  `internal/repository/system/`, `internal/appctx/`, `internal/shutdown/`,
  `internal/terminal/`.
- `app/main.go` is wired with `go.uber.org/fx`.
- New dependency: `go.uber.org/fx`, `github.com/spf13/viper` (or
  `gopkg.in/yaml.v3` + a custom loader — to be decided in design).
- New example: `examples/docker/minimal/` is the first dockerized reference
  image that exercises the core without PostgreSQL.
- No existing public APIs to break; `domain/`, `process/`, `license/`,
  `sbom/` services stay in place but are not yet wired through the loader.
