# Proposal 04 — Security, Observability & Operations (v0.4)

## Goal

Harden ezx for production and provide the operational tooling needed to
migrate fully from `container-scripts`. This phase pulls together the
security model, advanced observability, logging, operational API, and env var
scoping from `DESIGN.md`.

## In scope

### Security model

- Threat model and security principles.
- Process security:
  - PID 1 responsibilities.
  - Privilege dropping.
  - `no_new_privs`.
  - Capability dropping.
  - Resource limits (`rlimits`).
- Filesystem security:
  - Read-only root filesystem.
  - `noexec` on data directories.
  - Secure file permissions.
  - Atomic writes.
- Secret management:
  - Secret resolution chain: env → file → plugin → vault (via plugin).
  - Secret redaction in logs.
  - Memory zeroing (best effort).
  - Secret rotation.
- Network security:
  - Bind to localhost only by default.
  - TLS for plugin gRPC.
  - mTLS for marketplace.
- Plugin security:
  - Plugin signing (cosign/Sigstore).
  - Plugin sandboxing (seccomp, AppArmor, namespaces).
  - Plugin capabilities model.
- Setup phase security:
  - Source checksum verification.
  - No arbitrary script execution unless explicitly enabled.

### Observability

- OpenTelemetry traces and metrics bridge.
- Push model for metrics.
- Health endpoint (`/healthz`, `/readyz`).
- Plugin custom metrics.

### eBPF

- Event-driven process monitoring.
- File access auditing.
- Network connection tracking.
- Syscall filtering.
- Use `cilium/ebpf` library.

### Logging

- Structured JSON logs.
- Log levels, multi-destination, hot reload.
- Log redaction for secrets.
- Log rotation.
- Plugin log forwarding.

### Operational API

- HTTP API for runtime control:
  - process list, start, stop, restart
  - config reload
  - health/status
  - env var updates (live changes, v2.x)
- Hot reload of config without container restart.
- Plugin API extensions.

### Environment variable scoping

- Global env vars.
- Process-scoped env vars.
- `env_file` per process.
- Env var filtering: allow/deny globs.
- Secret env var filtering enforcement.
- Docker Compose migration mapping.

## Out of scope

- New core runtime primitives (those ship in v0.1–v0.3).
- Plugin distribution models beyond marketplace + manual install.
- PostgreSQL-specific features (v2.1).

## Deliverables

1. Security documentation and threat model.
2. Runtime security defaults: privilege drop, capabilities, `no_new_privs`.
3. Secret resolver chain with plugin extension point.
4. OpenTelemetry tracer + metrics bridge.
5. eBPF probe package (optional build tag for environments that support it).
6. Logging package with hot reload and redaction.
7. Operational API server with documented endpoints.
8. Env var scoping and filtering implementation.
9. Migration guide from Docker Compose and `container-scripts`.

## Acceptance criteria

- ezx runs as a non-root user when configured to do so.
- Secrets do not appear in logs or process dumps.
- OpenTelemetry traces cover the full container lifecycle.
- `curl localhost:8080/healthz` returns the orchestrator health.
- A process-scoped env var is not visible to sibling processes.
- `docker stop` triggers a clean shutdown even with seccomp enabled.

## Depends on

- [Proposal 01 — Foundation Core](./01-foundation-core.md)
- [Proposal 02 — Runtime Extensions](./02-runtime-extensions.md)
- [Proposal 03 — Build System](./03-build-system.md)

## Open questions

- Should eBPF be an optional compile-time feature or always built?
- Should the operational API require authentication?
- Should env var scoping be strict by default or opt-in?

## Risks

- Security defaults can break existing images. Ship behind flags first, then
  enable by default after migration guidance.
- eBPF requires modern kernels. Fallback behavior must degrade gracefully.
- Operational API increases attack surface; it must be off by default or
  authenticated.
