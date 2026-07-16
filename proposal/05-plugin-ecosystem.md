# Proposal 05 — Plugin Ecosystem (v2.0)

## Goal

Turn ezx from a monolithic binary into a small kernel that orchestrates
extensions. Vendors and users can add new services, probes, renderers,
reconciler actions, and schedulers without forking ezx. This is a breaking
change: the extension interface becomes the primary contract.

## In scope

### Lifecycle phases

Documented phases with extension hooks:

1. **LOAD** — config validation, secret resolution.
2. **SETUP** — source resolution, build.
3. **RENDER** — file rendering, env transformation.
4. **RUNTIME INIT** — process launch, readiness, reconciler.
5. **RUNTIME** — health checks, scheduled jobs, background tasks.
6. **SHUTDOWN** — drain, cleanup.

### Extension interface

- `domain.Extension` interface.
- Phase-specific sub-interfaces discovered via type assertion:
  - `ReadinessProber`
  - `HealthChecker`
  - `Scheduler`
  - `EnvTransformer`
  - `SourceResolver`
  - `ReconcilerAction`
  - `APIRegistrar`
  - `BackgroundTask`
  - `ShutdownHook`
  - `ConfigValidator`

### Plugin models

- **Sidecar** — fork+exec binary, gRPC over Unix domain socket. Any language.
- **Shared library (`.so`)** — `plugin.Open()` with C ABI exports.
  C/C++/Rust/Zig/D/Go capable.
- **Built-in** — compiled into the ezx binary (first-party).

### Discovery

- Scan `plugin_dirs` from YAML or `EZX_EXTENSION` env var.
- Self-description via `--ezx-describe` (sidecar) or `EzxPluginDescribe`
  (`.so`).
- Checksum verification at load time.
- Optional per-plugin config in YAML; no `plugins:` list required.

### Schema extensions

- Plugins declare JSON Schema fragments.
- Loader merges built-in + plugin schemas before validation.
- Plugin-owned fields pass through `Config.RawExtensions` to plugin hooks.
- Scope: top-level keys, extend existing kinds. New `kind:` values deferred.

### Marketplace foundation

- HTTP index format at `https://marketplace.ezx.dev/v1/index.yaml`.
- `ezx plugin search` and `ezx plugin install` commands.
- Checksum verification.
- Signing deferred to v2.x.

## Out of scope

- Signed marketplace / cosign (v2.x).
- Plugin sandboxing beyond process isolation.
- Hot reload of plugins.
- Plugin web UI.

## Deliverables

1. `plugin/` use-case package: discovery, descriptor parsing, lifecycle
   dispatch.
2. `domain/lifecycle.go` and `domain/extension_hooks.go`.
3. `internal/repository/plugin/sidecar/` with gRPC client + process manager.
4. `internal/repository/plugin/sharedlib/` with `.so` loader + C ABI shims.
5. `internal/repository/marketplace/` client.
6. Refactor `internal/repository/builtin/` as first-party compiled plugins.
7. `ezx plugin search|install|list` subcommands.
8. Schema extension support in `internal/loader/`.

## Acceptance criteria

- A sidecar plugin written in Go implements a custom readiness probe and is
  discovered at runtime.
- A `.so` plugin written in Rust or C implements a reconciler action and is
  loaded without crashing ezx.
- `ezx plugin install my-probe` downloads, verifies, and installs a plugin.
- A plugin declaring a new top-level YAML key passes validation and receives
  its owned config in hooks.
- A plugin failure does not crash ezx; the plugin is skipped and logged.

## Depends on

- [Proposal 01 — Foundation Core](./01-foundation-core.md)
- [Proposal 02 — Runtime Extensions](./02-runtime-extensions.md) so the hooks
  have something to extend.
- [Proposal 03 — Build System](./03-build-system.md) for source-strategy
  extension points.
- [Proposal 04 — Security, Observability & Operations](./04-security-observability-operations.md)
  for secret resolver and operational API extension points.

## Open questions

- Should built-in plugins ship as `.so` files or stay statically compiled?
- Should plugin gRPC use TLS on localhost?
- Should the marketplace be centralized or support private indexes?

## Risks

- `.so` plugins in Go require the same Go version as ezx. Rust/C/C++ `.so`
  plugins do not.
- A plugin panic in `.so` mode crashes ezx. Sidecar mode is safer.
- Schema extension conflicts between plugins need deterministic merge rules.
