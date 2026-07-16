# Proposal 06 — PostgreSQL Extension (v2.1)

## Goal

Prove the plugin ecosystem works end-to-end by shipping the first real
service extension: a PostgreSQL + PgBouncer container image built entirely
from compiled plugins. This is the “just download ezx, install the PostgreSQL
plugin, write a YAML” milestone.

## In scope

### PostgreSQL plugin (shipped as a compiled sidecar or `.so`)

- Readiness probes:
  - `postgres` — `pg_isready` + `SELECT 1` over unix socket.
  - `pgbouncer` — admin console `SHOW POOLS;`.
- File renderers:
  - `postgres_ini` — `postgresql.conf` from env vars.
  - `ini` — `pgbouncer.ini`.
  - `postgres_hba` — `pg_hba.conf` with managed blocks.
- Reconciler actions:
  - `set_role_password` — `ALTER ROLE ... WITH PASSWORD`.
  - `create_database` — `CREATE DATABASE IF NOT EXISTS`.
  - `create_extension` — `CREATE EXTENSION IF NOT EXISTS`.
- Scheduler jobs:
  - pgBackRest backup scheduler (full, diff, incr).
  - Per-job gates with `enabledWhen:`.

### Runtime YAML for PostgreSQL

- `envSchema` declaring the public env var contract:
  - `PGDATA`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`
  - `PGBOUNCER_LISTEN_PORT`, `PGBOUNCER_AUTH_TYPE`, `PGBOUNCER_POOL_MODE`,
    `PGBOUNCER_MAX_CLIENT_CONN`, `PGBOUNCER_DEFAULT_POOL_SIZE`
  - `POSTGRESQL_SHARED_BUFFERS`, `POSTGRESQL_MAX_CONNECTIONS`,
    `POSTGRESQL_TIMEZONE`
- `processChain`:
  - root: `postgres`
  - child: `pgbouncer` with `needParentReady: true`
  - optional child: `pgbackrest` with `optional: true`
- `files`:
  - `/etc/pgbouncer/pgbouncer.ini` rendered from `envSchema`.
  - `${PGDATA}/postgresql.conf` rendered with `postgres_ini`.
  - `${PGDATA}/pg_hba.conf` with managed block for `PG_HBA_ADD_*`.
- `healthcheck`:
  - postgres `SELECT 1` (critical)
  - pgbouncer `SHOW POOLS;` (critical)
  - pgbackrest `info` (warning)

### Setup YAML for PostgreSQL

- Build postgresql from source with `autotools` source strategy.
- Build pgbouncer from source with `autotools` source strategy.
- Build pgbackrest from source with `autotools` source strategy.
- `cleanup` step that removes build tools.

### Example

- `examples/docker/postgresql/`:
  - `ezx.setup.yaml`
  - `ezx.runtime.yaml`
  - `Dockerfile` (two-stage)
  - `plugin_config:` referencing the PostgreSQL plugin.

### Registry resolvers (shipped with the plugin)

- `postgresql` — `https://ftp.postgresql.org/pub/source/v{VERSION}/...`
- `pgbouncer` — `https://www.pgbouncer.org/downloads/files/{VERSION}/...`
- `pgbackrest` — `https://github.com/pgbackrest/pgbackrest/archive/release/{VERSION}.tar.gz`

## Out of scope

- Patroni, pg_cron, pg_stat_monitor, Citus, etc.
- HA / replication / streaming.
- Setup-phase secrets (private mirror credentials).
- Plugin marketplace listing (the plugin is distributed manually or via
  marketplace, but marketplace itself ships in v2.0).

## Deliverables

1. PostgreSQL plugin implementing `ReadinessProber`, `HealthChecker`,
   `ReconcilerAction`, `Scheduler`, and `FileRenderer` interfaces.
2. Registry resolvers for postgresql, pgbouncer, pgbackrest.
3. `examples/docker/postgresql/` with setup + runtime YAMLs and Dockerfile.
4. CI job that builds the image and starts a container.
5. Documentation: “How to use the PostgreSQL extension.”

## Acceptance criteria

- `ezx plugin install postgresql-extension` installs the plugin.
- `docker build -f examples/docker/postgresql/Dockerfile -t ezx-postgresql:0.1.0 .`
  succeeds.
- `docker run -e POSTGRES_PASSWORD=foo -p 5432:5432 ezx-postgresql:0.1.0`
  starts postgres, pgbouncer, and pgbackrest.
- `psql -h localhost -p 5432 -U postgres -c 'SELECT 1'` succeeds.
- `ezx healthcheck` returns 0.
- Changing `POSTGRES_PASSWORD` and restarting the container updates the
  password without re-init.
- `PG_HBA_ADD_1=host ... md5` becomes an ordered line in `pg_hba.conf`.
- `docker stop` drains pgbouncer before postgres.
- pgBackRest backup runs on schedule and does not fail the container health
  when it fails.

## Depends on

- [Proposal 05 — Plugin Ecosystem](./05-plugin-ecosystem.md) for the extension
  interface and discovery.

## Open questions

- Should the PostgreSQL plugin ship as a sidecar binary or a `.so`?
- Should the plugin include pgBackRest, or should pgBackRest be a separate
  plugin?
- Should the example use Debian packages or compile from source for faster
  first builds?

## Risks

- PostgreSQL specifics leaking into the core. The plugin must not import any
  `internal/` package from ezx; it only depends on `domain/` interfaces.
- Build time is long. The example Dockerfile should use BuildKit cache mounts.
- The plugin must be versioned independently from ezx. A PostgreSQL plugin
  compiled for ezx v2.0 must work with ezx v2.1.
