# Proposal 01 — Foundation Core (v0.1)

## Goal

Ship the smallest executable `ezx` system that is genuinely useful: a
service-agnostic container process orchestrator with env-driven config,
telemetry, health checks, and graceful shutdown.

The core must not know about PostgreSQL, PgBouncer, Kafka, or any other
service. It only knows how to run processes in a DAG, wait for readiness,
check health, render templates, and stop cleanly.

## In scope

### Domain model

- `domain.Config`, `domain.Stage`, `domain.Service`
- `domain.ReadinessProbe` (abstract: `Type` + `Config map[string]any`)
- `domain.Template`, `domain.FileSpec`
- `domain.ShutdownPlan`, `domain.HealthCheck`

### Use-case packages

- `orchestrator/` — runtime DAG executor:
  - start roots in parallel,
  - wait for readiness probes,
  - start `needParentReady` children,
  - reverse-DAG drain on shutdown.
- `healthcheck/` — single global health check invoked by Docker `HEALTHCHECK`.
- `telemetry/` — structured JSON logs and Prometheus metrics:
  - `ezx_process_up`
  - `ezx_process_ready`
  - `ezx_healthcheck_ok`
  - `ezx_shutdown_duration_seconds`
- `setup/` — serial build-time step runner for `ezx.setup.yaml`.

### Infrastructure

- `internal/appctx/`, `internal/shutdown/`, `internal/terminal/`
- `internal/loader/` — Viper → typed `domain.Config` with strict validation.
- `internal/renderer/` — `text/template` + values/env rendering.
- `internal/repository/system/`:
  - `process.go` refactored to pure I/O: `Start`, `Stop`, `Wait`, `Signal`.
  - `probe.go` with three probe types: `tcp`, `exec`, `http`.
  - `file.go` for atomic writes, chown, chmod.

### CLI commands

- `ezx setup --config ezx.setup.yaml`
- `ezx run --config ezx.runtime.yaml`
- `ezx healthcheck --config ezx.runtime.yaml`
- `ezx validate --config <file> --env-file <file>`

### Example

- `examples/docker/minimal/` — a tiny multi-process container (e.g. an echo
  server + sidecar) that exercises the core without PostgreSQL.

## Out of scope

- PostgreSQL-specific probes, renderers, or actions.
- PgBouncer, Patroni, pgBackRest.
- Scheduler, reconciler, wildcard config mapping.
- Per-process `optional` or `severity` health flags.
- Plugin / extension system.
- Marketplace.
- Per-step Dockerfile emission or caching.
- OpenTelemetry, eBPF, operational API.
- Advanced security hardening beyond sane defaults.

## Deliverables

1. Domain types extended with config, readiness, template, signal, and
   healthcheck types.
2. `app/main.go` wired with `go.uber.org/fx`.
3. `internal/loader/` validates YAML and env schema.
4. `orchestrator/service.go` runs a DAG with readiness probes.
5. `healthcheck/service.go` runs the configured check and exits 0/1.
6. `telemetry/` exposes logs and Prometheus metrics.
7. `setup/service.go` runs serial build steps.
8. `examples/docker/minimal/` builds and runs end-to-end.

## Acceptance criteria

- `go test ./...` passes.
- The minimal example image builds and starts.
- `docker run -e LISTEN_PORT=8080 minimal:0.1.0` starts the service.
- `ezx healthcheck` returns 0 when the service is ready.
- `docker stop` triggers graceful reverse-DAG shutdown within timeout.
- `curl localhost:9090/metrics` returns the documented metrics.
- Unknown env vars are rejected by `ezx validate`.

## Depends on

Nothing. This is the first phase.

## Open questions

- Default metrics port: `9090` or configurable via `EZX_METRICS_PORT`?
- Should `setup.steps[]` support parallel execution where the DAG allows in
  v1, or keep it strictly serial?
- Should `process.environment` be supported in v1, or only container env
  inheritance?

## Risks

- Over-engineering the DAG. Keep it a tree/forest, not a full graph, for v1.
- Letting PostgreSQL specifics leak into the core. Review every PR against the
  “service-agnostic” rule.
