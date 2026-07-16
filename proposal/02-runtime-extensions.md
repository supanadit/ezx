# Proposal 02 — Runtime Extensions (v0.2)

## Goal

Add the runtime features that make ezx usable for stateful production
services: scheduled jobs, state reconciliation, wildcard env→config mapping,
per-process health checks with severity, and configuration conflict
detection. These are first-party capabilities compiled into the core.

## In scope

### Scheduler

- In-process cron using `github.com/robfig/cron/v3`.
- Per-process `scheduler:` block tied to process lifecycle.
- Explicit two-flag gate pattern (`ENABLED` + `AUTO_ENABLE`).
- Per-job gates with `enabledWhen:`.
- `timeoutSeconds`, `startDelaySeconds`, `onFailure` policy.

### Reconciler

- `Reconciler` use-case package.
- Reconciliation modes:
  - `frozen` — apply only on first boot.
  - `reconcile` — compare env to live state on every boot, apply if different.
  - `reconcile-with-force` — requires `_FORCE=true` sibling env var.
- Cache in `${EZX_STATE_DIR}/.ezx/applied/<entry>.json`.
- Live state queries in `internal/repository/system/live.go`.
- Escape hatches: `EZX_FORCE_RECONCILE`, `EZX_FORCE_INIT`, `EZX_DRY_RUN`.

### Wildcard env mapping

- `internal/wildcard/` package.
- Four patterns:
  1. Single env var → one config key.
  2. Prefix wildcard → auto-derived keys.
  3. Indexed prefix → ordered list (e.g. `PG_HBA_ADD_*`).
  4. Section prefix → section key/value updates (e.g. `PGBOUNCER_CONFIG_*`).
- `keyTransform` / `valueTransform` utilities.

### Managed config blocks

- Renderers support `managedBlock:` with markers.
- On re-render, strip the old block and append the new one idempotently.
- No more sidecar state files like `.pg_hba_env_entries`.

### Per-process health checks

- Move health checks from global `healthcheck.checks` to
  `processChain.roots[].healthchecks[]`.
- `severity: critical | warning`.
- `optional: true` processes whose failures do not fail container health.

### Conflict detection

- `ezx validate` detects:
  - Conflicts (errors)
  - Collisions (errors)
  - Ambiguities (warnings)
  - Declarations (warnings)
- Strict mode flag.

## Out of scope

- PostgreSQL-specific probes/actions (these become an extension in v2.1).
- PgBouncer, Patroni, pgBackRest.
- Plugin / extension discovery system.
- Marketplace.
- Build caching / per-step Dockerfile emission.
- Operational API.
- OpenTelemetry, eBPF.

## Deliverables

1. `scheduler/` use-case package.
2. `reconciler/` use-case package + `internal/repository/system/live.go`,
   `actions.go`, `cache.go`.
3. `internal/wildcard/` package.
4. Extend `internal/renderer/formats/` with `postgres_hba.go`.
5. Move health checks to per-process nodes with severity.
6. Add `internal/validate/` conflict detection.
7. Update `examples/docker/minimal/` to demonstrate scheduler and reconciler
   using generic commands.

## Acceptance criteria

- A scheduled job runs at the configured cron expression and exits cleanly on
  shutdown.
- A reconciler entry updates a file on restart when the env var changes.
- `PG_HBA_ADD_1` and `PG_HBA_ADD_2` become ordered lines in a managed block,
  and re-rendering is idempotent.
- A child process marked `optional: true` whose health check is `warning` does
  not fail the container health.
- `ezx validate` reports a conflict when two env vars map to the same config
  key.

## Depends on

- [Proposal 01 — Foundation Core](./01-foundation-core.md)

## Open questions

- Should the reconciler cache be per-process or global?
- Should scheduler jobs run as a specific OS user (`runAs`)?
- Should strict mode be the default for conflict detection?

## Risks

- Reconciler live queries require a connection to a stateful service during
  startup. The reconciler must run after the readiness probe passes.
- Wildcard rules can collide with explicit mappings; the conflict detector
  must catch this at validate time.
