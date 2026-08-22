# ezx

**Deterministic, scriptable container entrypoint engine** built in Go.

`ezx` replaces fragile bash `entrypoint.sh` trees (sed/awk config rewriting,
`sleep`-based readiness polling, `pgrep`-polling supervision) with a single
binary driven by a declarative JavaScript script. It spawns and supervises
dependency trees of processes, converts environment variables into config
files and CLI arguments, strips secrets before spawning children, and handles
PID-1 init duties (zombie reaping, signal forwarding, graceful drain) —
all without a line of shell.

## Why ezx?

Docker images conventionally ship a bash entrypoint. That works, but it is
brittle, unreadable, and unportable:

- Config is rewritten with `sed -i` against multiline template strings.
- Readiness is approximated with `sleep 5` instead of real probes.
- Supervision is done with `pgrep` polling loops instead of a proper tree.
- Secrets leak into children because env isn't filtered.

`ezx` is the answer: a single static binary plus a small declarative script.
The `examples/bootstrap/supanadit/postgresql/` tree is a faithful 1:1
behavioural port of the production PostgreSQL container entrypoint, expressed
in a few hundred lines of readable JS instead of a wall of bash.

## How it works

You write a JavaScript entrypoint script that speaks the `require("ezx")` host
API, then run it:

```sh
ezx bootstrap examples/bootstrap/supanadit/postgresql/main.js
```

`ezx` loads the script against its Go-hosted JS engine (`goja`), and the script
declaratively describes everything: which files to provision, how to build CLI
arguments from env, which processes to spawn, how they depend on each other,
and how to supervise them.

The core abstraction is the **process dependency tree** — a `ProcessNode` with
unlimited nested `children`:

- Children start after their parent.
- A child with `needParentReady: true` waits until the parent's readiness probe
  passes.
- Siblings start in parallel.
- Any node can carry a `restart` policy, a `health` server, a `scheduler`
  (cron), `forwardSignals`, a `probe`, and graceful `shutdown` config.

### Two modes — the part s6-overlay can't match

Every node selects how its main process is launched:

- **Mode A — `exec: true`**: ezx does the setup (env templating, file
  provisioning, spawning support processes), then `syscall.Exec`s the app. The
  app **becomes PID 1** and handles its own signals natively. Zero supervision
  overhead, native signal behaviour — but no restart/healthcheck safety net.
  Must be a leaf node and is mutually exclusive with `health`.
- **Mode B — supervised (default)**: ezx **stays PID 1**. Each node gets
  `restart`, `health`/readiness HTTP, `scheduler`, `forwardSignals`, `probe`,
  `shutdown`, and a dependency tree via `children`. ezx reaps zombies and
  forwards signals.

s6-overlay never lets your app be PID 1 — its `/init` is always PID 1. ezx
lets you choose: hand PID 1 to your app for native signals, or keep ezx as PID
1 for supervision, health, scheduling, and dependency ordering.

### The env-driven deterministic model

The other differentiator vs s6-overlay (which has no env→config, env→args, env
filtering, or declarative file provisioning — you write shell scripts for all
that):

- **File provisioning** — `{ type: "set-property", fromEnvPattern:
"^POSTGRESQL_CONFIG_(.+)$", nameTransform: "lower", ... }` turns any matching
  env var into a config line automatically. Ops include `replace`, `append`,
  `ensure`, `set-property`, `insert`, `block`, with `createOnly` and
  `when: { name, value }` conditionals.
- **env→args** — `argOperations` build CLI flags from env: if-set values,
  if-truthy bare flags, comma/space list splits, pattern enumeration with name
  transforms, and custom callbacks.
- **Env filtering** — `filterEnv` / `filterEnvPattern` strips matching vars
  before spawning a child, replacing the shell's `env -u SECRET_*` trick.
  Critical for tools like pgBackRest that break if `PGBACKREST_*` leaks into a
  child.
- **Readiness gating** — `needParentReady` + a `probe` replaces the
  `$PARENT_PID` polling hack.
- **Scheduler nodes** — cron-driven processes with a `gate` (e.g. only run
  backups when the primary role is healthy), `initialDelay`, and `minInterval`.
- **Declarative config builder** — `config.build` renders `key=value` (e.g.
  `postgresql.conf`), aligned table/matrix (`pg_hba.conf`), and INI sections
  (`pgbackrest.conf`) from structured objects, plus a fluent `config.builder()`
  with `setIf` / `setFlag` for conditional configs.
- **YAML builder** — `yaml.build` serializes a JS object to YAML
  deterministically (used for `patroni.yml`), no template-string concatenation.

## The `require("ezx")` host API

The aggregate module exposed to scripts:

| Namespace   | Purpose                                                                       |
| ----------- | ----------------------------------------------------------------------------- |
| `env`       | Read env with defaults, `isTruthy`, pattern enumeration                       |
| `editor`    | Imperative file editing (`open`, `upsert`, `replace`, `append`, `remove`)     |
| `fs`        | `mkdirAll`, `chmod`, `chownRecursive`, `readDir`, `exists`, `remove`, `umask` |
| `process`   | Low-level spawn / signal / wait / exec (privilege drop via `user`/`group`)    |
| `chain`     | High-level declarative process tree (`chain.run({ roots: [...] })`)           |
| `config`    | Declarative config builder (`key=value`, tables, INI, fluent builder)         |
| `yaml`      | Deterministic YAML serialization                                              |
| `scheduler` | Cron-driven scheduled processes with gates                                    |
| `health`    | Health/readiness HTTP server (`/readyz`) on `EZX_HEALTH_ADDR`                 |
| `probe`     | exec / tcp / http readiness probes                                            |
| `api`       | User-defined routes on the shared health server (e.g. manual backup trigger)  |
| `log`       | Structured logging                                                            |

## Quick start

```sh
# From the repo root
go build -o ezx ./app

# Demo script — read env, provision files, run a tiny process chain
./ezx bootstrap examples/bootstrap/demo.js

# Config builder demo — renders postgresql.conf, pg_hba.conf, pgbackrest.conf, INI
./ezx bootstrap examples/bootstrap/config-builder.js

# Declarative env-to-CLI-argument building
PROMETHEUS_ENABLE_WEB_LIFECYCLE=true \
THANOS_QUERY_STORE_ADDRESSES=store1:10901,store2:10901 \
./ezx bootstrap examples/bootstrap/arg-building.js

# Process dependency tree with parent-readiness gating
./ezx bootstrap examples/bootstrap/process-tree.js

# Restart policy + graceful shutdown config
./ezx bootstrap examples/bootstrap/restart.js

# Env secret-filtering before spawning a child
MINIO_ROOT_PASSWORD=supersecret \
./ezx bootstrap examples/bootstrap/env-filtering.js

# Faithful port of the official PostgreSQL docker-entrypoint.sh
./ezx bootstrap examples/bootstrap/postgres.js
```

### PostgreSQL container example

`examples/bootstrap/postgres.js` is a faithful, self-contained port of the
official PostgreSQL `docker-entrypoint.sh` (env setup, first-run detection,
initdb with privilege drop, `pg_hba.conf` editing, temp-server + init scripts,
then `process.exec` so postgres becomes PID 1). Run it against a real postgres
image:

```sh
docker run --rm -e POSTGRES_PASSWORD=secret \
  -v "$PWD/ezx:/usr/local/bin/ezx" \
  -v "$PWD/examples:/examples" postgres:16 \
  ezx bootstrap /examples/bootstrap/postgres.js
```

> Note: `examples/bootstrap/supanadit/postgresql/` is a separate port of the
> PostgreSQL entrypoint from the `containers` project
> (`https://github.com/supanadit/containers`), with pgbouncer/pgpool/pgBackRest/
> Patroni/sshd. It lives here as a working reference for that project, not as a
> standalone ezx example.

## Status

`experimental` / work-in-progress. The tree compiles and the test suite passes
on the `experimental` branch, but the API is still moving. Known gaps vs a full
s6-overlay-style supervisor: a general dependency _graph_ (vs the current
tree/chain), per-service supervised logging with rotation, container exit-code
propagation (the container should exit with the main process's code, not
0/130), and a tarball distribution model. The design goal is to take s6's
semantics (supervision, restart policy, dependency ordering, exit-code
propagation) while keeping ezx's model (JS + env-driven determinism) — a
differentiated product, not a clone.

## License

[Apache License 2.0](LICENSE) — © 2026 Supan Adit Pratama.
