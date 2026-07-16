# EZX Proposal Roadmap

This directory splits the full design in `DESIGN.md` into sensible, time-ordered
deliverables. Each proposal is a standalone implementation plan for one phase.

`DESIGN.md` remains the single source of truth for design rationale and all
possible features. These proposals only decide **when** each design section ships
and **what must be true** before the phase is considered complete.

## Proposals

| # | Proposal | Version | Core idea | DESIGN.md sections |
| --- | --- | --- | --- | --- |
| 01 | [Foundation Core](./01-foundation-core.md) | v0.1 | Service-agnostic container orchestrator with basic telemetry. | 1, 2 (two-stage/env/validate), 3, 4 (schema/probes), 6, 7, 8, 9, 10, 17 (basic Prometheus), 19, 20 (basic structured logging) |
| 02 | [Runtime Extensions](./02-runtime-extensions.md) | v0.2 | Scheduler, reconciler, wildcard config, conflict detection, per-process health. | 2 (scheduler/reconciler/wildcard), 12, 13 |
| 03 | [Build System](./03-build-system.md) | v0.3 | Declarative sources, registry, per-step caching, dependency isolation. | 4.6, 5.4, 15, 16 |
| 04 | [Security, Observability & Operations](./04-security-observability-operations.md) | v0.4 | Security model, OTel/eBPF, advanced logging, operational API, env scoping. | 4.4, 14, 17.12 (OTel), 18, 20 (advanced), 21, 22 |
| — | **Stable release** | **v1.0** | All of the above, polished, documented, tested. | — |
| 05 | [Plugin Ecosystem](./05-plugin-ecosystem.md) | v2.0 | Extension interface, discovery, sidecar/`.so` plugins, schema extensions, marketplace. | 11 |
| 06 | [PostgreSQL Extension](./06-postgresql-extension.md) | v2.1 | First real service extension: PostgreSQL + PgBouncer as a compiled plugin. | 2 (health/formats), 5 |

## Version semantics

| Version | Meaning |
| --- | --- |
| v0.x | Unstable, incremental builds toward v1.0. Breaking changes allowed. |
| v1.0 | First stable release. All mandatory core features complete, documented, tested. |
| v1.x | Stable with backward-compatible additions. |
| v2.0 | Breaking change: plugin ecosystem added. Core remains stable; extension interface is the new contract. |
| v2.x | Stable with backward-compatible additions on top of v2.0. |

## Ordering rules

1. **Foundation before domain knowledge.** The core binary must run any process
   tree before it knows about PostgreSQL.
2. **Runtime before build optimization.** We must be able to run a container
   before we optimize how it is built.
3. **Security and observability are mandatory.** They ship before v1.0, not after.
4. **Extension interface before service extensions.** The extension contract must
   exist before PostgreSQL is shipped as a plugin.
5. **v1.0 = stable and functional.** Everything mandatory for a production-ready
   orchestrator ships before v1.0. v2.x is the "rest": marketplace, plugins, and
   service-specific extensions.

## Reading order

1. Read `DESIGN.md` for the full design rationale.
2. Read this roadmap for the delivery sequence.
3. Read each proposal in order when starting that phase.
