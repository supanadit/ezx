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

## How it works

You write a JavaScript entrypoint script that speaks the `require("ezx")` host
API, then run it:

```sh
ezx bootstrap examples/bootstrap/demo.js
```

`ezx` loads the script against its Go-hosted JS engine (`goja`), and the script
declaratively describes everything: which files to provision, how to build CLI
arguments from env, which processes to spawn, how they depend on each other,
and how to supervise them.

The core abstraction is the **process dependency graph** — a `ProcessNode` with
explicit `dependsOn` edges to other nodes (the canonical flat `nodes` form).
The legacy recursive `roots`/`children` tree is fully supported and desugared
into the same graph at bind time:

- A node starts after all of its dependencies have *started*.
- **Per-edge wait modes** via `dependsOnEdges`: each edge independently chooses
  `started` (default), `ready`, or `exit`. The legacy
  `dependsOn: ["a"]` + `needParentReady: true` is sugar for
  `dependsOnEdges: [{ name: "a", waitFor: "ready" }]` and keeps working.
- Sibling / independent nodes start in parallel; **multi-root chains start
  concurrently** (each root with no dependencies starts together).
- When a dependency exits permanently, its running dependents drain and its
  not-yet-started dependents are skipped.
- Any node can carry a `restart` policy, a `health` server, a `scheduler`
  (cron), `forwardSignals`, a `probe`, and graceful `shutdown` config.

### Dependency-graph edge semantics

| Event on node X                          | Dependents of X                                                    | Chain                                   |
| ---------------------------------------- | ------------------------------------------------------------------ | --------------------------------------- |
| X started                                | dependents whose edge waits `started` may start                    | —                                       |
| X ready (probe/callback)                 | dependents whose edge waits `ready` may start                      | —                                       |
| X exits, restarts                        | dependents unaffected (X still alive)                              | —                                       |
| X exits 0 permanently                    | dependents whose edge waits `exit` may start (oneshot success gate)| —                                       |
| X exits permanently (code 0 or failed)   | running dependents drain; not-yet-started dependents are skipped   | ends when no nodes remain               |
| X fails fatally (non-`Optional`)         | —                                                                  | fail fast: cancel all, return X's error |
| X fails (`Optional`)                     | dependents of X are skipped (with a warning)                       | continues                               |
| context cancelled (SIGTERM / script end) | all running nodes drain concurrently                               | `run` returns after drain               |

Per-edge wait modes close the last s6-overlay gap: a node can wait for one dep
to be `ready`, another to merely `start`, and a oneshot to `exit 0` — all on
the same node.

### Two modes — the part s6-overlay can't match

Every node selects how its main process is launched:

- **Mode A — `exec: true`**: ezx does the setup (env templating, file
  provisioning, spawning support processes), then `syscall.Exec`s the app. The
  app **becomes PID 1** and handles its own signals natively. Zero supervision
  overhead, native signal behaviour — but no restart/healthcheck safety net.
  Mutually exclusive with `health`. An exec node may depend on **oneshot**
  nodes (the "init DAG → exec main" pattern): it fires only after all its
  oneshot deps exit 0 and no other long-running node is still supervised.
- **Mode B — supervised (default)**: ezx **stays PID 1**. Each node gets
  `restart`, `health`/readiness HTTP, `scheduler`, `forwardSignals`, `probe`,
  `shutdown`, and dependency edges via `dependsOn`/`dependsOnEdges` (or the
  legacy `children` tree form). ezx reaps zombies and forwards signals.

s6-overlay never lets your app be PID 1 — its `/init` is always PID 1. ezx
lets you choose: hand PID 1 to your app for native signals, or keep ezx as PID
1 for supervision, health, scheduling, and dependency ordering.

### Oneshot services & "init DAG → exec main"

A **oneshot** node (`oneshot: true`) runs its process to completion instead of
supervising it as a long-running service. It is the declarative form of the
imperative `process.run` one-shot, composed into the dependency graph:

- **Exit 0 = success gate.** A oneshot signals `started`+`ready` only when it
  exits 0, so its dependents start only after it finishes successfully. A
  non-zero exit fails the node (fatal unless `optional`), skipping its
  dependents.
- **Retryable.** `restart` on a oneshot retries it on failure up to
  `MaxRetries` (s6-rc oneshot semantics).
- **Mutually exclusive** with `exec`, `scheduler`, `health`, and
  `needParentReady` (a oneshot is "ready" only when it exits 0) — validated
  fail-fast at chain validation.

The headline pattern is **"init DAG → exec main"**: a chain of init steps
(initdb, stanza-create, migrations) run to completion, then the main long-running
app becomes PID 1. The exec node may now depend on oneshot nodes — it fires only
after all its oneshot deps exit 0 and no other long-running node is still
supervised:

```js
const { chain } = require("ezx");
chain.run({
  nodes: [
    { name: "initdb", oneshot: true, process: { binaryPath: "/usr/local/bin/initdb", arguments: [...] } },
    { name: "stanza-create", oneshot: true, dependsOn: ["initdb"], process: { binaryPath: "/usr/bin/pgbackrest", arguments: ["stanza-create", ...] } },
    { name: "postgres", exec: true, dependsOn: ["initdb", "stanza-create"], process: { binaryPath: "/usr/local/bin/postgres", arguments: ["-D", "/var/lib/postgresql/data"] } },
  ],
});
```

Try it: `./ezx bootstrap examples/bootstrap/oneshot-init.js`.

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

### Per-service logging with rotation

Each supervised process can route its stdout/stderr to its own **file-backed log
destination with size-based rotation** — closing the per-service supervised
logging gap vs s6-overlay. Set `log.stdout`/`log.stderr` to `"file"` and point
`filePath` at the target:

```js
process: {
  binaryPath: "/usr/bin/pgbouncer",
  arguments: ["/etc/pgbouncer/pgbouncer.ini"],
  log: {
    stdout: "file",                 // route stdout to a rotating file
    filePath: "/var/log/ezx/pgbouncer.log",
    maxBytes: 5 * 1024 * 1024,      // rotate at 5 MiB (default 10 MiB)
    maxBackups: 2,                  // keep .log + .1 + .2 (default 3)
    stderr: "stderr",               // stderr still goes to container stderr
  },
},
```

- **Rotation** is size-based: when a write would push the active file past
  `maxBytes`, it is closed, shifted to `.1` (newest), older backups shift down,
  and backups beyond `maxBackups` are dropped. `maxBackups < 0` keeps backups
  indefinitely. Rotation is size-based only; there is no time-based rotation or
  compression.
- **Append on open** — a restarted node appends to the current file; rotation is
  purely size-driven.
- **Shared path** — when both `stdout` and `stderr` are `"file"` with the same
  `filePath`, they share one writer so interleaving stays coherent and rotation
  happens once. Different paths get independent writers.
- **Validation** — a `"file"` destination requires a non-empty `filePath`
  (fail-fast at chain validation, naming the node).

Try it: `./ezx bootstrap examples/bootstrap/log-rotation.js`.

## The `require("ezx")` host API

The aggregate module exposed to scripts:

| Namespace   | Purpose                                                                       |
| ----------- | ----------------------------------------------------------------------------- |
| `env`       | Read env with defaults, `isTruthy`, pattern enumeration                       |
| `editor`    | Imperative file editing (`open`, `upsert`, `replace`, `append`, `remove`)     |
| `fs`        | `mkdirAll`, `chmod`, `chownRecursive`, `readDir`, `exists`, `remove`, `umask` |
| `process`   | Low-level spawn / signal / wait / exec (privilege drop via `user`/`group`)    |
| `chain`     | High-level declarative process graph (`chain.run({ nodes: [...] })`) |
| `config`    | Declarative config builder (`key=value`, tables, INI, fluent builder)         |
| `yaml`      | Deterministic YAML serialization                                              |
| `scheduler` | Cron-driven scheduled processes with gates                                    |
| `health`    | Health/readiness HTTP server (`/readyz`, `/livez`, `/healthz`) on `EZX_HEALTH_ADDR` (default `:8080`) |
| `probe`     | exec / tcp / http readiness probes                                            |
| `api`       | User-defined routes on the shared health server (e.g. manual backup trigger)  |
| `shell`     | Single-quote escaping for embedding values into shell commands                |
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

# Dependency graph via the legacy `roots`/`children` tree form, with
# parent-readiness gating
./ezx bootstrap examples/bootstrap/process-tree.js

# Dependency graph: fan-in + shared dependencies (flat `nodes` / `dependsOn`)
./ezx bootstrap examples/bootstrap/dag.js

# Dependency graph: per-edge wait modes (`dependsOnEdges`: started/ready/exit)
./ezx bootstrap examples/bootstrap/edge-wait.js

# Restart policy + graceful shutdown config
./ezx bootstrap examples/bootstrap/restart.js

# Env secret-filtering before spawning a child
MINIO_ROOT_PASSWORD=supersecret \
./ezx bootstrap examples/bootstrap/env-filtering.js

# Full script-visible API surface — advanced editor block ops, remaining env/fs
# helpers, health readiness flip, api put/delete verbs (needs EZX_HEALTH_ADDR)
EZX_HEALTH_ADDR=:8080 \
./ezx bootstrap examples/bootstrap/api-surface.js

# Per-service file-backed logging with size-based rotation
./ezx bootstrap examples/bootstrap/log-rotation.js

# Port of the official PostgreSQL docker-entrypoint.sh
./ezx bootstrap examples/bootstrap/postgres.js

# Oneshot services + "init DAG → exec main" (declarative init steps → exec)
./ezx bootstrap examples/bootstrap/oneshot-init.js
```

### PostgreSQL container example

`examples/bootstrap/postgres.js` is a self-contained port of the official
PostgreSQL `docker-entrypoint.sh` (env setup, first-run detection, initdb with
privilege drop, `pg_hba.conf` editing, temp-server + init scripts, then
`process.exec` so postgres becomes PID 1). Run it against a real postgres
image:

```sh
docker run --rm -e POSTGRES_PASSWORD=secret \
  -v "$PWD/ezx:/usr/local/bin/ezx" \
  -v "$PWD/examples:/examples" postgres:16 \
  ezx bootstrap /examples/bootstrap/postgres.js
```

## Status

Pre-1.0, in active use. The tree compiles and the test suite passes on `main`.
The `require("ezx")` surface is additive-only (see below), so existing scripts
keep working across releases even before 1.0.

### API stability policy

As of **0.3.0**, the `require("ezx")` module surface is **additive-only**:

- Existing modules and method names are frozen — enforced mechanically by
  `TestApiSurface` (a rename or removal fails CI, not users).
- Additions (new modules, new methods, new node fields) are allowed at any time.
- If a breaking change ever becomes unavoidable, it ships as
  `require("ezx/v2")` with the prior surface preserved under `ezx/v1` — never
  as an in-place rename.
- 1.0 declares the 0.x surface final.

No `ezx/alpha` / `ezx/beta` modules are registered: an alias only protects
users if surfaces can diverge, and parallel facades pay maintenance cost before
there are users to protect. The compat test is the freeze.

Remaining gaps vs a full s6-overlay-style supervisor:

There are none — ezx has full s6-overlay parity on the supervision model:
dependency graph, per-edge wait modes, oneshots, init-DAG → exec, logging
rotation, exit-code propagation, restart, health, and scheduling — plus the
env-driven determinism s6 lacks.

Distribution is a single static binary + checksums on GitHub Releases, and is
installable via `go install github.com/supanadit/ezx/app@<version>`. The
release pipeline builds the binaries and checksums on every semver tag.

The design goal is to take s6's semantics (supervision, restart policy,
dependency ordering, exit-code propagation) while keeping ezx's model (JS +
env-driven determinism) — a differentiated product, not a clone.

## License

[Apache License 2.0](LICENSE) — © 2026 Supan Adit Pratama.
