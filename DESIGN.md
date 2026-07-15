# EZX Design — Replacing `container-scripts` with a Go Binary

> Status: **proposal** — not yet implemented. The two YAML files shown at the end
> are the target schema; everything else is the design rationale + migration plan.

## 1. The problem in one paragraph

`container-scripts` is a tree of ~250 bash files orchestrating Docker images
(postgres, kafka, mariadb, …). Two layers are stacked into one image:

- **setup layer** — runs once at build time, compiles extensions, copies binaries
  (the `setup.sh` + `setup/scripts/0X-install-*.sh` chain).
- **runtime layer** — runs every container start, initdb's the cluster, starts
  postgres / patroni / pgbouncer, watches readiness, hot-reloads config, drains
  on SIGTERM (the `entrypoint.d/entrypoint.sh` + `scripts/{init,runtime,misc,utils}/*`).

The runtime layer is where things break. The current `startup.sh` does
`su -c "pg_ctl ... start" postgres &; sleep 2; pgrep -f pgbouncer; if missing
exit 1`. The 2-second sleep and the pgrep polling are the kind of fragile
coordination that the user wants to replace with deterministic, typed checks.
`NeedParentReady` in `domain.ProcessNode` is the right primitive — the only
thing missing is a real **readiness probe** instead of "started", and a real
**dependency graph executor** with retries/timeouts.

The goal of `ezx` is therefore:

> One Go binary. A user writes two short YAML files (or one file with two
> documents), puts the binary in their Dockerfile, and gets a deterministic
> setup + runtime orchestrator that replaces the entire bash tree.

## 2. Why split setup from runtime

The setup phase needs privileges and tools that must **not** be in the
final image (compilers, headers, `apt`, sometimes `make` of extensions like
`pg_cron`). It also bakes in secrets the user does not want in the running
container (a private package key, a pgBackRest stanza-create token, etc.).

### Two files, not one

**Decision: two files.** The Dockerfile copies `ezx.setup.yaml` into the
build stage and `ezx.runtime.yaml` into the runtime stage. The setup file
**literally never reaches the final image** — different `COPY` lines in
different stages. The setup file can contain build-time secrets, internal
build commands, references to private package mirrors, anything; none of
it leaks.

The binary picks the mode from the YAML's top-level `stage:` field:

```yaml
apiVersion: ezx/v1
stage: setup        # or: runtime
```

No env var guessing. The file is the source of truth.

### What "sandbox" means here

The first ezx-managed image is a **bare-bones postgresql** with no
extensions — core postgres, optionally pgbouncer. This keeps the setup
stage fast, the image small, and the attack surface small. Extension
images (citus, pg_cron, pg_stat_monitor, …) are future work and would
be different `ezx.setup.yaml` files compiled into different image tags
(`postgresql-citus:1.0`, etc.) that **all use the same ezx binary**.

### The 100% env-only contract

The runtime container supports every config knob via environment
variables. The user never mounts a `postgresql.conf`, `pgbouncer.ini`,
or `patroni.yml`. **The bash scripts do this today via `sed -i` on
template files** — and that's exactly what's fragile. The replacement
is the `runtime.envSchema:` block in the YAML, which:

1. Declares every accepted env var, its type, its default, and whether
   it's a secret.
2. Lets `text/template` (not `sed`) render the config files, with
   `{{ .Env.PGBOUNCER_LISTEN_PORT }}` checked at load time.
3. Lets `ezx validate` reject typos like `PGBOUNCER_LISTEN_POR` before
   the container even starts.
4. Lets `ezx doctor` print the full schema (`--show-env-schema`).

The **public contract** of the image is the env var list. The template
files are private to the image maintainer (you, in the Dockerfile) —
users never see them.

### The wildcard patterns: the real user-facing API

Looking at `container-scripts`, there are **four distinct wildcard
styles** in use today. The ezx YAML must support all four
declaratively, with the idempotency the bash versions try (and fail)
to provide:

**1. Single dedicated env var → one config setting.**
The simple case. `POSTGRESQL_SHARED_BUFFERS=256MB` becomes
`shared_buffers = 256MB` in `postgresql.conf`. Already covered by
`envSchema` above. This is the explicit, type-checked path.

**2. Prefix wildcard → settings with auto-derived key.**
The bash version: `for var in $(compgen -A variable | grep '^POSTGRESQL_CONFIG_')`.
Every env var matching the prefix becomes a key=value, the suffix
becomes the key (often lowercased). The Go replacement:

```yaml
runtime:
  files:
    - destination: ${PGDATA}/postgresql.conf
      format: postgres_ini          # declares the syntax of the file
      fromEnv:
        # explicit names: one-by-one
        POSTGRESQL_SHARED_BUFFERS:  shared_buffers
        POSTGRESQL_MAX_CONNECTIONS: max_connections
        # OR: a prefix wildcard — suffix becomes the key, lowercased
        prefix: POSTGRESQL_CONFIG_
        keyTransform: lower         # lower | upper | snake | kebab
        valueTransform: quote       # quote | none | bool | int
        # OR: both (the explicit names win, the wildcard fills the rest)
```

The renderer walks the env, picks the matches, applies the
transforms, merges them into the file. `ezx validate` catches
typos (env name doesn't match a known mapping), rejects unknown
prefixes, and prints a clear error at load time.

**3. Indexed prefix → a list of entries in a section.**
The "a bit hacky" one. `PG_HBA_ADD_1=host ... md5`,
`PG_HBA_ADD_2=host ... md5`, etc. — the bash sorts the indices
numerically and writes them as lines in `pg_hba.conf`, tracking
previously-written entries in a state file (`.pg_hba_env_entries`)
so it can re-apply on restart without duplicating. The Go
replacement:

```yaml
runtime:
  files:
    - destination: ${PGDATA}/pg_hba.conf
      format: postgres_hba
      section: env_added            # marker comment, optional
      fromEnv:
        prefix: PG_HBA_ADD_
        listSort: numeric           # numeric | lexicographic | insertion
        listItem:                   # how to render one entry
          appendAs: line            # append as a raw line
        managedBlock:               # the bash's state-file trick, in Go
          marker: "# >>> ezx:pg_hba_add >>>"
          endMarker: "# <<< ezx:pg_hba_add <<<"
          onStart: remove            # remove old block first; idempotent
```

The Go renderer:

1. Reads the current file.
2. Strips the previous managed block (the markers make this trivial).
3. Builds the new block from the current env, sorted, rendered
   as one line each.
4. Writes it back atomically. **No more `.pg_hba_env_entries` sidecar
   file** — the markers in the file itself are the source of truth.

**4. Section prefix → append a key=value block to a config file.**
The `PGBOUNCER_CONFIG_*` pattern (from `startup.sh`):
`PGBOUNCER_CONFIG_MAX_CLIENT_CONN=200` →
`max_client_conn = 200` appended to `pgbouncer.ini` if the key
doesn't exist, replaced if it does. The Go replacement:

```yaml
runtime:
  files:
    - destination: /etc/pgbouncer/pgbouncer.ini
      format: ini
      fromEnv:
        prefix: PGBOUNCER_CONFIG_
        keyTransform: lower
        valueTransform: none
        policy: replace_or_append    # replace_or_append | append_only | upsert
```

The renderer keeps the file's existing structure (sections, comments,
known keys) and surgically replaces/inserts the new keys. This is
the `sed` loop in the bash version, but as a proper parser.

### Why this matters: `ezx validate` is now actually useful

With the wildcards declared, the user can run:

```bash
ezx validate --config ezx.runtime.yaml --env-file .env
```

And get:

```
✓ POSTGRES_USER matches schema (default: postgres)
✓ POSTGRES_PASSWORD is a declared secret
✓ PGBOUNCER_LISTEN_PORT: 6432 (int, default OK)
✓ PGBOUNCER_AUTH_TYPE: scram-sha-256 (enum OK)
✗ PGBOUNCER_LISTEN_POR: not a known env var (typo? did you mean PGBOUNCER_LISTEN_PORT?)
  Hint: declared env vars are: PGBOUNCER_LISTEN_ADDR, PGBOUNCER_LISTEN_PORT, ...
✓ PG_HBA_ADD_1: 1 entry
✓ PG_HBA_ADD_2: 1 entry
✓ POSTGRESQL_CONFIG_*: 3 wildcards matched (shared_buffers, max_connections, work_mem)
```

This is the **single biggest win** of the ezx rewrite. The bash
scripts can't do this — they only fail at runtime, on the first
connection attempt, with a generic error.

### Health: per-process, severity-aware, optional-friendly

`healthcheck.sh` today has the right **checks** but the wrong
**model**. It runs `comprehensive_health_check`, which is "all of
them, and any failure means unhealthy." That's why
`pgbackrest` (or patroni, or any auxiliary tool) being down can
take the whole container unhealthy even when postgresql + pgbouncer
are fine. The Go replacement must support three things the bash
version doesn't:

1. **Per-process ownership of checks.** A check belongs to a
   specific `ProcessNode` (postgres, pgbouncer, pgbackrest, etc.),
   not to "the container." This way the postgresql + pgbouncer
   container can be reported as healthy even when pgbackrest is
   down or hasn't run yet.

2. **Severity per check.** A check is `critical` (fails the
   container health) or `warning` (logged, doesn't fail).
   pgbackrest's backup-completed check is `warning`. postgres's
   `pg_isready` is `critical`. This is the same model Prometheus
   uses for alert severity, applied to Docker healthchecks.

3. **Optional / soft processes.** A `ProcessNode` can be marked
   `optional: true` — if it crashes, the orchestrator logs a
   warning and continues; the container stays healthy as long as
   its critical checks pass. The shutdown plan still tries to
   stop the optional process gracefully (so it can flush state),
   but its exit code is not used to set the container's exit code.

The YAML shape:

```yaml
runtime:
  processChain:
    roots:
      - name: postgresql
        optional: false                    # default: false
        process: { ... }
        readinessProbe: { ... }
        healthchecks:                      # checks scoped to THIS process
          - name: pg-ready
            type: postgres
            user: "{{ .Env.POSTGRES_USER }}"
            severity: critical             # critical | warning
          - name: select-1
            type: postgres-query
            query: "SELECT 1"
            severity: critical
          - name: replication-lag
            type: postgres-query
            query: "SELECT 1 FROM pg_stat_replication LIMIT 1"
            severity: warning              # not fatal
            timeoutSeconds: 5
        children:
          - name: pgbouncer
            process: { ... }
            healthchecks:
              - name: pgb-admin
                type: pgbouncer
                show: pools
                severity: critical
          - name: pgbackrest               # backup system
            optional: true                 # crashes are non-fatal
            process: { ... }
            healthchecks:
              - name: last-backup-age
                type: exec
                command: ["/usr/bin/pgbackrest", "info", "--stanza=default"]
                severity: warning          # never fails container
                intervalSeconds: 300       # check every 5 min
```

The `HEALTHCHECK` block in the **Dockerfile** is now generated by
ezx, not written by the user:

```dockerfile
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 \
    CMD ezx healthcheck --config /etc/ezx/ezx.yaml
```

`ezx healthcheck` is a tiny subcommand. It runs all checks with
`severity: critical` on all `optional: false` processes, in
parallel, applies the timeout/interval logic, and exits with the
standard Docker codes:

- **0** = all critical checks pass
- **1** = at least one critical check failed (container unhealthy)

Warning-level checks never affect the exit code, but they do
appear in `docker inspect` JSON output via a `/var/run/ezx/health`
file the orchestrator maintains.

### The aggregator: container health = AND of critical checks

The healthchecker service keeps a small in-memory table:

```
┌──────────────────┬─────────────┬──────────────────┬──────────┐
│ process          │ severity    │ last status      │ last run │
├──────────────────┼─────────────┼──────────────────┼──────────┤
│ postgresql       │ critical    │ ok @ 12:01:03    │ 17s ago  │
│ postgresql       │ warning     │ ok @ 12:00:59    │ 21s ago  │
│ pgbouncer        │ critical    │ ok @ 12:01:01    │ 19s ago  │
│ pgbackrest       │ warning     │ failed @ 12:00:55│ 25s ago  │
└──────────────────┴─────────────┴──────────────────┴──────────┘
Container health: HEALTHY (1 warning ignored: pgbackrest)
```

The container is **healthy iff** all `critical` checks on
non-optional processes are currently passing. Optional processes
that have crashed are reported but not counted. The user can run
`ezx status` to get the same view, or hit a future HTTP endpoint
for k8s liveness/readiness probes.

### Why this fixes the existing problem

Today, if `pgbackrest info` returns non-zero (no successful
backup yet, repo unreachable, etc.), the bash healthcheck exits 1
and Docker marks the container unhealthy. Kubernetes then takes
the pod out of rotation even though postgres is fine. The Go
version: `pgbackrest` is `optional: true` and its check is
`severity: warning`. Container stays healthy. Kubernetes keeps
the pod in rotation. The user gets a clear log line:
`pgbackrest: warning, not fatal` and a `ezx status` command
that shows the real picture.

### Schedules: in-process cron, gated by explicit flags

`backup-scheduler.sh` is a 250-line bash `while true; sleep 60`
loop that polls cron expressions, dedups per-minute, watches
the parent PID, and skips non-primary nodes. **This is exactly
the kind of thing bash cannot do reliably.** A `sleep 60` loop
in bash is the textbook fragile pattern: it drifts, it has no
real cancellation, the cron parser is regex-on-strings, the
parent-PID check is `kill -0` polling.

The Go replacement: a typed `scheduler/` package
wrapping `github.com/robfig/cron/v3`. The whole scheduler
lives in-process inside the ezx binary, started and stopped
with the parent context, with no external `cron` daemon, no
`/etc/cron.d/*` files, no `crond` running in the container.

The key design principle: **gating by explicit flags, not
defaults.** Today in `container-scripts`, two env vars are
involved: `PGBACKREST_ENABLE` (turn the backup system on) and
`PGBACKREST_AUTO_ENABLE` (turn the auto-scheduler on). They are
**independent flags** and both must be set `true` for the
scheduler to run. This is a deliberate developer-experience
choice: no magic, no implicit "backup is on so auto-backup is
on," no surprise side effects. The Go version preserves this
1:1 — the YAML makes the relationship explicit and validated
at load time:

```yaml
runtime:
  processChain:
    roots:
      - name: postgresql
        process: { ... }
        # No scheduler here — postgres itself is not a job.
        children:
          - name: pgbackrest
            optional: true
            process:
              binaryPath: /usr/local/bin/pgbackrest
              arguments: ["--config=/etc/pgbackrest.conf", "--stanza=default"]
            scheduler:                  # ← a per-process scheduler
              # Gate: scheduler runs only if BOTH flags are explicitly true.
              # The binary fails fast at load if both are not set, with
              # a clear "did you forget PGBACKREST_AUTO_ENABLE=true?" hint.
              enabledWhen:              # all conditions must be true
                - env: PGBACKREST_ENABLE
                  equals: "true"
                - env: PGBACKREST_AUTO_ENABLE
                  equals: "true"
              # If you don't want a flag-gate (e.g. for tests), use:
              #   alwaysOn: true
              jobs:
                - name: full-backup
                  schedule: "0 2 * * *"            # standard 5-field cron
                  timezone: "{{ .Env.PGBACKREST_AUTO_TIMEZONE | default \"UTC\" }}"
                  runAs: postgres
                  command: ["/usr/bin/pgbackrest", "backup", "--type=full"]
                  timeoutSeconds: 7200              # 2h, kills stuck backups
                  onFailure:                        # the bash had `|| true`
                    logLevel: warn                  # never fails the container
                    continue: true                  # keep the schedule alive
                - name: incr-backup
                  schedule: "*/15 * * * *"
                  startDelaySeconds: 120            # the bash's FIRST_INCR_DELAY
                  runAs: postgres
                  command: ["/usr/bin/pgbackrest", "backup", "--type=incr"]
                  onFailure: { logLevel: warn, continue: true }
```

The two flags are **required** for the scheduler to enable.
`ezx validate` checks both at load time and refuses to start
the runtime if either is missing or not exactly `"true"` — the
explicit-gate model. No `"true"|"yes"|"on"|"1"` magic, no
lenient parsing. If the user wants auto-backup, they type
`PGBACKREST_ENABLE=true` AND `PGBACKREST_AUTO_ENABLE=true` and
both must be present. Same DX as bash, but validated and
unambiguous.

#### Per-job gates: `PGBACKREST_AUTO_INCR=false`

The top-level `enabledWhen:` turns the **whole scheduler** on
or off. But what if the user wants `PGBACKREST_AUTO=true` and
**only the full backup**, not the incrementals? Today the bash
scheduler can't do that — all three jobs are active as long as
the top-level switch is on. The Go version supports **per-job
gates** with the same primitive:

```yaml
scheduler:
  enabledWhen:                     # gate the whole scheduler
    - { env: PGBACKREST_ENABLE,     equals: "true" }
    - { env: PGBACKREST_AUTO_ENABLE, equals: "true" }
  jobs:
    - name: full-backup
      schedule: "0 2 * * *"
      # Per-job gate. If absent, the job is always active when
      # the scheduler is on. If present, ALL conditions must be true.
      enabledWhen:
        - { env: PGBACKREST_AUTO_FULL, equals: "true" }   # default: true
      command: ["/usr/bin/pgbackrest", "backup", "--type=full"]

    - name: diff-backup
      schedule: "20 2,8,14,20 * * *"
      enabledWhen:
        - { env: PGBACKREST_AUTO_DIFF, equals: "true" }   # default: true
      command: ["/usr/bin/pgbackrest", "backup", "--type=diff"]

    - name: incr-backup
      schedule: "*/15 * * * *"
      enabledWhen:
        - { env: PGBACKREST_AUTO_INCR, equals: "true" }   # default: true
      command: ["/usr/bin/pgbackrest", "backup", "--type=incr"]
```

Now the user can:

```bash
# Full only, no diff, no incr
PGBACKREST_ENABLE=true PGBACKREST_AUTO_ENABLE=true \
  PGBACKREST_AUTO_DIFF=false PGBACKREST_AUTO_INCR=false \
  postgres-sandbox

# All on (the default — all three AUTO_* flags = true)
PGBACKREST_ENABLE=true PGBACKREST_AUTO_ENABLE=true postgres-sandbox

# Scheduler on, but no jobs run (rare but valid)
PGBACKREST_ENABLE=true PGBACKREST_AUTO_ENABLE=true \
  PGBACKREST_AUTO_FULL=false PGBACKREST_AUTO_DIFF=false PGBACKREST_AUTO_INCR=false \
  postgres-sandbox
```

`ezx validate` shows this clearly at load time:

```
✓ scheduler.enabledWhen: PGBACKREST_ENABLE = "true"
✓ scheduler.enabledWhen: PGBACKREST_AUTO_ENABLE = "true"
✓ job full-backup: enabledWhen satisfied (PGBACKREST_AUTO_FULL = "true")
✗ job diff-backup:  enabledWhen NOT satisfied (PGBACKREST_AUTO_DIFF = "false")
  Job is registered but will not run.
✓ job incr-backup:  enabledWhen satisfied (PGBACKREST_AUTO_INCR = "true")
```

A disabled job is **not an error** — it's a valid runtime state
("I want the scheduler active but I don't want diffs to run").
The user gets a clear log line when the scheduler starts:

```
[scheduler] pgbackrest: 1 jobs scheduled, 1 job disabled
[scheduler] pgbackrest: jobs active: full-backup, incr-backup
[scheduler] pgbackrest: jobs disabled: diff-backup (PGBACKREST_AUTO_DIFF=false)
```

#### Gate defaulting: when the env var is not set

The strict `equals: "true"` model raises a question: what if
the user doesn't set `PGBACKREST_AUTO_INCR` at all? Two
sensible answers:

- **A) Treat unset as `false` (deny by default).** Strict, safe.
  The user has to explicitly set every `AUTO_*` flag they want
  on. Bash's current behavior is essentially this (the cron
  matches → the job runs → no per-job toggle exists).
- **B) Treat unset as `true` (allow by default).** Mirrors
  bash's current behavior ("all three jobs active by default
  when the scheduler is on"). A user who wants to skip one
  has to explicitly set `=false`. Less typing for the common
  case ("I just want all three on").

Recommendation: **B (allow by default)** for `AUTO_*` flags,
because the common case is "turn everything on." This matches
the existing bash UX: with `PGBACKREST_AUTO_ENABLE=true` you
get all three. To skip one, you say `=false`. The YAML makes
this explicit:

```yaml
- name: incr-backup
  schedule: "*/15 * * * *"
  enabledWhen:
    - env: PGBACKREST_AUTO_INCR
      equals: "true"
      default: true              # ← allow if unset; only "false" turns it off
```

`ezx validate` reports it accurately:

```
✓ job incr-backup: enabledWhen (PGBACKREST_AUTO_INCR unset → defaulting to "true")
```

A user who wants the **stricter** model (deny by default) sets
`default: false`. The image maintainer picks the default per
job at YAML-authoring time; the user only deals with the env
vars.

#### Gate as a tiny expression language

A `Gate` is a list of `Condition`s; all must be true. Each
condition is one of:

- `env: NAME, equals: "VALUE"` — strict string match against an env var
- `env: NAME, default: "VALUE"` — what to use if the env var is unset
- `env: NAME, notEquals: "VALUE"` — inverse (e.g. `notEquals: "false"` to mean "any value other than false")

That's it. **No `&&`/`||` chains, no regex, no arithmetic.**
The reason: gates are read by humans debugging production at
3am. Simplicity wins. If a job needs complex logic, the
author should split it into two jobs or move the logic into
the command itself.

#### What this kills from bash

Today in `backup-scheduler.sh` the only per-job control is
the cron expression itself (`PGBACKREST_AUTO_FULL_CRON`). To
"disable" a job, the user has to set its cron to a value that
never matches (e.g. `"0 0 31 2 *"`) — ugly hack. The Go
version replaces this with a first-class `enabledWhen:` gate
that is checked explicitly, validated at load time, and
reported in the health view.

### Why in-process, not /etc/cron.d

The MariaDB image today writes `/etc/cron.d/mariadb-backup`
and runs `cron` in the container. This has two problems:

1. **Two scheduling systems.** Postgres uses a custom bash
   loop, MariaDB uses `cron`. Different behavior, different
   bugs, different log formats. ezx has one scheduling
   primitive.
2. **External dependency.** `cron` must be installed, the
   `/etc/cron.d` file must be writable, the daemon must be
   started. With the in-process approach, the only thing
   needed is the Go binary — already in the image.

`robfig/cron/v3` parses standard 5-field cron expressions
(`*`, `*/15`, `1-5`, `1,3,5`, `1-10/2`) with timezones,
has accurate scheduling (no drift), supports per-job
timeouts, and integrates with `context.Context` for
graceful shutdown. The bash script's `cron_field_matches`
function is ~100 lines; the Go library does it in one line.

### Lifecycle and shutdown

The scheduler is **tied to the parent process's lifecycle**:

- When the parent (postgresql) process starts, the scheduler
  starts (after `startDelaySeconds`).
- When the parent stops (SIGTERM, crash, restart), the
  scheduler is cancelled via `context.Context` and any
  in-flight job is sent SIGTERM with a grace period
  (`timeoutSeconds`).
- Job output is streamed to the orchestrator's log (and
  optionally to a file like `/var/log/pgbackrest-auto.log`
  via the standard `logr` interface).
- The scheduler's process-level health is the AND of its
  jobs' last results: if `full-backup` last succeeded
  within its expected window, scheduler is healthy. If
  not, it's degraded (warning, not critical, because
  `pgbackrest` is `optional: true`).

### What this replaces

- `docker/postgresql/entrypoint.d/scripts/runtime/backup-scheduler.sh`
  → `scheduler/` + the `scheduler:` block in the YAML
- `docker/mariadb/entrypoint.d/scripts/init/04-backup.sh`'s
  `echo "$schedule root ... > /etc/cron.d/mariadb-backup` →
  same `scheduler/` + a `command:` line in the YAML
- The bash `cron_field_matches` regex parser →
  `robfig/cron/v3`

Both postgres and mariadb end up using the same scheduling
primitive, with the same log format, the same lifecycle, the
same shutdown semantics. **One scheduler, one place to fix
bugs.**

### Reconciliation: env-driven state that survives across boots

The classic one-time-vs-every-time problem: you start a
postgresql container with `POSTGRES_PASSWORD=foo`, it works. A
month later you change the env to `POSTGRES_PASSWORD=bar`,
restart, and **nothing happens** — the cluster was already
initialized, the password is still `foo`. You're locked out of
your own container.

Today's bash code in `02-database.sh` is the textbook
unintentional freeze:

```bash
# 02-database.sh (today)
if [ "$cluster_exists" = true ]; then
    log_info "PostgreSQL cluster already exists, skipping initialization"
    return 0       # ← set_postgres_password never runs again
fi
```

There's no way to update the password without `docker exec
psql -c "ALTER USER ..."`. That's the bug. The Go version
needs a proper answer.

#### The three modes

Every env-driven state change in ezx declares one of three
**reconciliation modes**:

| Mode | When it runs | When it doesn't | Use case |
| ------ | -------------- | ----------------- | ---------- |
| `frozen` | only on first boot (cluster doesn't exist yet) | on every subsequent boot | the bash default, but explicit. For things that **must not change** after init: replication user, `pg_hba` skeleton, the initial empty database. |
| `reconcile` | every boot — compare env to live state, apply if different | only if env == state | passwords, configuration tweaks (`max_connections`), any knob the user might want to retune without a re-init. |
| `reconcile-with-force` | like `reconcile`, but a `_FORCE=true` sibling env var bypasses the "skip if already applied" optimization (e.g. when the live-state cache is missing or corrupted) | — | the same password flow but with a manual escape hatch when the reconciler gets confused. |

The semantics are uniform across every reconcilable knob. The
postgresql example:

```yaml
runtime:
  processChain:
    roots:
      - name: postgresql
        # These are "reconcile" — applied on every boot, applied
        # only if they differ from the live value.
        reconcile:
          - env: POSTGRESQL_PASSWORD     # user can change this
            apply: set_role_password     # typed action
            target:
              role: "{{ .Env.POSTGRES_USER | default \"postgres\" }}"
            mode: reconcile              # default if omitted
          - env: POSTGRESQL_MAX_CONNECTIONS
            apply: set_postgres_setting  # postgresql.conf knob
            target: { setting: max_connections }
            mode: reconcile
          - env: POSTGRESQL_SHARED_BUFFERS
            apply: set_postgres_setting
            target: { setting: shared_buffers }
            mode: reconcile

        # These are "frozen" — applied only on first boot.
        # Changing the env does nothing, on purpose.
        initOnly:
          - env: POSTGRESQL_REPLICATION_USER
            apply: create_role
            target: { role: replicator, replication: true }
            mode: frozen
          - env: POSTGRESQL_REPLICATION_PASSWORD
            apply: set_role_password
            target: { role: replicator }
            mode: frozen
            requireForce: true    # must set POSTGRESQL_FORCE_INIT=true to change
```

The user's scenario now works:

```bash
# Day 1
POSTGRESQL_USER=admin POSTGRESQL_PASSWORD=foo docker compose up -d
# → cluster initialized, role 'admin' created with password 'foo'

# Day 30, password needs to change
POSTGRESQL_USER=admin POSTGRESQL_PASSWORD=bar docker compose up -d
# → reconciler: live role 'admin' has password 'foo' ≠ env 'bar'
#   → ALTER ROLE admin WITH PASSWORD 'bar'
#   → log line: "reconciled: postgresql.password for role 'admin'"

# Day 30, frozen knob changes by accident
POSTGRESQL_REPLICATION_USER=newrepl docker compose up -d
# → reconciler: initOnly entry, mode=frozen → skipped
#   → log line: "skipped: postgresql.replication_user (mode=frozen, set POSTGRESQL_FORCE_INIT=true to override)"
```

#### The "applied state" cache

How does the reconciler know what the **last applied value** was?
Two layers, both with files:

1. **Per-process applied state** in
   `${PGDATA}/.ezx/applied/<step-name>.json` — the reconciler
   writes `{envVar: "POSTGRESQL_PASSWORD", value: "foo",
   appliedAt: "2025-...", fingerprint: "sha256:..."}`. On
   startup it reads this and compares to current env.
2. **Live state query** for "what's actually in postgres right
   now" — `SELECT rolname, rolpassword FROM pg_authid WHERE
   rolname = $1`. The reconciler uses the **live query** as the
   source of truth, not the cached file, because the live value
   can drift (manual `ALTER ROLE`, restore from backup,
   replication).

So the comparison is **env vs live**, not env vs cache. The
cache exists only as a hint to skip the live query when the env
value matches the last applied value (a small optimization —
saves a `psql` round trip on every boot when nothing changed).

```
for each reconcile entry:
    live = query_postgres(target)         # SELECT rolpassword ...
    if env == live:
        log "skipped: no change"
        return
    apply(env)                            # ALTER ROLE ...
    cache_write(entry)                    # for the next boot's optimization
```

#### The "force" escape hatch

Three escape hatches, all explicit:

| Env var | Effect |
| --------- | -------- |
| `EZX_FORCE_RECONCILE=true` | Re-apply every `reconcile` entry, ignoring the live query result. Useful when the live state is broken and you want to force the env's view. |
| `EZX_FORCE_INIT=true` | Re-apply every `frozen` entry. **Destructive** — wipes a replication user and recreates it. ezx logs a loud warning when this is set. |
| `EZX_DRY_RUN=true` | Don't apply anything. Print what would be applied. The standard "show me what you'd do" flag. |

`ezx validate` reports the mode of every reconcilable entry at
load time:

```
✓ reconcile: postgresql.password (mode=reconcile)
✓ reconcile: postgresql.max_connections (mode=reconcile)
⚠ initOnly:  postgresql.replication_user (mode=frozen)
  Change requires EZX_FORCE_INIT=true (destructive).
```

#### Per-process scoping

Reconciliation is **scoped to a process node** (like
healthchecks). The password is set on `postgresql`; the
backup schedule is reconciled on `pgbackrest`. Each process
has its own `.ezx/applied/` directory and its own set of
entries. This means the postgresql container can be
reconciled (password change) while a sibling container's
frozen state stays untouched.

#### What this kills from bash

- The `cluster_exists=true → return 0` short-circuit that
  silently swallows every "apply on every boot" use case
- The "you can't change a password without `docker exec`"
  support burden
- The 5 different `set_postgres_password`-style functions
  scattered across `01-directories.sh`, `02-database.sh`,
  `03-config.sh`, `05-sshd.sh`, etc. (each handling its own
  subset of the same problem with subtly different rules)

#### Where this lives in the code

The `reconcile:` block is a per-process node property, like
`healthchecks:` and `scheduler:`. The orchestration lives in
`reconciler/` (use case layer), which defines:

- A `Reconciler` interface: `Reconcile(ctx, entry, live, env) error`
- A `LiveStateQuerier` interface: `GetRolePassword(role)`, `GetSetting(key)`, etc.
- An `AppliedStateCache` interface: read/write `.ezx/applied/*.json` files

The **implementations** of these interfaces live in `internal/repository/system/`:

- `internal/repository/system/actions.go` — one file per action type:
  - `set_role_password.go` — `ALTER ROLE ... WITH PASSWORD ...`
  - `set_postgres_setting.go` — `ALTER SYSTEM SET ...` + reload
  - `create_role.go`
  - `create_database.go`
  - `run_sql.go` — escape hatch for arbitrary SQL
- `internal/repository/system/live.go` — `LiveStateQuerier` that wraps the
  connection pool and exposes typed queries
- `internal/repository/system/cache.go` — `AppliedStateCache` that writes/reads
  the `.ezx/applied/*.json` files

This separation means `reconciler/` never imports a SQL driver or a file I/O
package directly — it depends only on interfaces. The concrete implementations
are injected via fx in `app/main.go`.

A `reconcile:` entry looks exactly like a `healthcheck:`
entry from the user's perspective — same scoping, same
severity model (`warning` for non-critical reconciliations,
`critical` for ones whose failure should fail the container
start). It's the same pattern, just applied to "make this
true" instead of "check that this is true."

#### One subtle gotcha: the env-vs-live race

On a fresh container, **live state doesn't exist yet** (no
cluster). The reconciler must run **after** `initdb`. The
dependency is implicit: `reconcile` is part of the
orchestrator's start sequence for a process node, which
runs after that node's readiness probe passes. So:

```
1. start postgresql process
2. wait for readiness probe (pg_isready)
3. run reconcile entries (live state now exists)
4. declare node healthy
5. start children with needParentReady=true
```

If a reconcile entry fails (e.g. wrong password syntax), the
node is reported unhealthy and the orchestrator refuses to
start children. The user sees the error in `ezx status` and
the logs, not as a mysterious "container won't start" later.

### Where the formats live

The format-specific renderers (`postgres_ini`, `postgres_hba`, `ini`,
`properties`, `yaml`, `toml`, `json`, `env`, `conf`) belong in
`internal/renderer/formats/`. Each is a small package with one
function: `Render(file *File, env Env) error`. Adding a new file
format (e.g. `nginx.conf` if we ever need it) is one new file, no
bash required.

## 3. Clean architecture (mirroring `phpv`)

`phpv` has the pattern this project should follow: `app/` wires the
dependency graph with `go.uber.org/fx`, `domain/` is pure data, the
top-level feature packages (`process/`, `license/`, `sbom/`, …
alongside the new ones we add) own business rules and define
**interfaces** for their dependencies, and
`internal/repository/{memory,disk,system}/` holds the **implementations**
of those interfaces. `ezx` is currently missing most of the feature
packages and the `fx` wiring; that's the first thing to add.

> **Key clean architecture rule**: A top-level package (use case) defines
> its own `Repository` interface. The implementation lives in
> `internal/repository/`. The use case never imports the implementation
> directly — only the interface. This is the Dependency Inversion
> Principle, and it's how phpv keeps every layer testable.

```
ezx/
├── app/                              # fx wiring + cobra root
│   └── main.go
├── cmd/                              # sub-commands (one binary, many entry points)
│   ├── setup/main.go                 # `ezx setup` (build-time phase)
│   ├── run/main.go                   # `ezx run`   (runtime phase)
│   ├── validate/main.go              # `ezx validate` (lint the YAML)
│   └── doctor/main.go                # `ezx doctor` (probe without exec)
├── domain/                           # PURE — no I/O, no exec, no framework imports
│   ├── bootstrapper.go               # already exists
│   ├── process.go                    # already exists
│   ├── process_chain.go              # already exists
│   ├── workflow.go                   # already exists
│   ├── config.go                     # NEW — Config, Stage, Service types (pure data)
│   ├── readiness.go                  # NEW — ReadinessProbe (abstract: Type string + Config map)
│   ├── signal.go                     # NEW — ShutdownPlan (pure data)
│   ├── template.go                   # NEW — FileTemplate, TemplateVar (pure data)
│   ├── credentials.go                # NEW — EnvCredential, FileCredential (pure data)
│   ├── healthcheck.go                # NEW — HealthCheck (abstract: Type string + Config map)
│   ├── schedule.go                   # NEW — Schedule, Job (pure data; Gate logic lives in scheduler/ use case)
│   ├── reconcile.go                  # NEW — ReconcileEntry, ReconcileMode (pure data)
│   └── process_node.go               # NEW — Optional, Healthchecks, Scheduler, Reconcile fields
├── orchestrator/                     # USE CASE — runtime DAG executor (replaces startup.sh)
│   │                                 # Defines OrchestratorRepository interface
│   ├── service.go                    # DAG orchestration logic (start order, readiness, reverse-drain)
│   ├── service_test.go
├── healthcheck/                      # USE CASE — runtime healthcheck (replaces healthcheck.sh)
│   │                                 # Defines HealthcheckRepository interface
│   ├── service.go
│   └── service_test.go
├── reconciler/                       # USE CASE — env vs live state, three modes
│   │                                 # Defines ReconcilerRepository + LiveStateQuerier interfaces
│   ├── service.go                    # Reconciler interface + orchestration
│   └── service_test.go
├── scheduler/                        # USE CASE — cron-style in-process scheduler
│   │                                 # Defines SchedulerRepository interface
│   ├── service.go                    # wraps robfig/cron/v3 (the library is an implementation detail)
│   └── service_test.go
├── setup/                            # USE CASE — build-time DAG (replaces setup.sh)
│   │                                 # Defines SetupRepository + SourceResolver interfaces
│   ├── service.go                    # DAG runner
│   └── service_test.go
├── registry/                         # USE CASE — name@version → URL + checksum
│   │                                 # Defines RegistryRepository interface (mirrors phpv)
│   ├── service.go                    # thin Service that delegates to RegistryRepository
│   └── service_test.go
├── process/                          # USE CASE — already exists (defines ProcessNodeRepository interface)
│   ├── service.go                    # FIX: must store the injected ProcessNodeRepository
├── license/                          # USE CASE — already exists
│   └── service.go
├── sbom/                             # USE CASE — already exists
│   └── service.go
├── internal/                         # PRIVATE — Go's `internal/` rule; all infrastructure lives here
│   ├── appctx/                       # NEW — mirrors phpv; ctx, version, logger
│   │   └── appctx.go
│   ├── shutdown/                     # NEW — port of phpv's signal manager
│   │   └── shutdown.go
│   ├── terminal/                     # NEW — cobra sub-commands grouped here
│   │   ├── setup.go
│   │   ├── run.go
│   │   ├── validate.go
│   │   └── doctor.go
│   ├── loader/                       # INFRASTRUCTURE — load + validate YAML into domain.Config
│   │   ├── service.go
│   │   └── service_test.go
│   ├── renderer/                     # INFRASTRUCTURE — materialize config files from env + templates
│   │   ├── service.go
│   │   ├── service_test.go
│   │   └── formats/                  # one package per file format
│   │       ├── postgres_ini.go       # postgresql.conf
│   │       ├── postgres_hba.go       # pg_hba.conf (the indexed-list one)
│   │       ├── ini.go                # pgbouncer.ini, generic .ini
│   │       ├── properties.go         # java-style
│   │       ├── yaml.go               # patroni.yml-style
│   │       ├── toml.go
│   │       ├── json.go
│   │       ├── env.go                # write an env file
│   │       └── template.go           # raw text/template, last resort
│   ├── credentials/                  # INFRASTRUCTURE — resolve secrets from env / file
│   │   ├── service.go
│   │   └── service_test.go
│   ├── wildcard/                     # INFRASTRUCTURE — env var → config mapping
│   │   ├── service.go                # walks env, applies rules
│   │   ├── rule.go                   # Single | Prefix | IndexedPrefix
│   │   ├── transform.go              # keyTransform / valueTransform
│   │   └── service_test.go
│   └── repository/                   # the only place real I/O lives
│       ├── system/                   # already has process.go (refactor: pure I/O only)
│       │   ├── process.go            # REFACTOR: only Start/Stop/Wait/Signal — no orchestration
│       │   ├── probe.go              # NEW — TCP dial, exec check, pg_isready, etc.
│       │   ├── file.go               # NEW — atomic write, chown, chmod
│       │   ├── live.go               # NEW — LiveStateQuerier (SQL queries for reconciler)
│       │   └── actions.go            # NEW — Reconciler actions (ALTER ROLE, ALTER SYSTEM, etc.)
│       ├── memory/                   # already has license/sbom
│       ├── disk/                     # NEW — YamlRepository, TemplateRepository
│       │   ├── yaml.go
│       │   └── template.go
│       └── builtin/                  # NEW — registry resolvers (moved from registry/builtin/)
│           ├── go.go                 # go.dev/dl/go{VERSION}.linux-amd64.tar.gz
│           ├── node.go               # nodejs.org/dist/v{VERSION}/...
│           ├── postgresql.go         # ftp.postgresql.org/pub/source/...
│           ├── github.go             # GitHub releases API
│           ├── pypi.go               # PyPI JSON API
│           └── http.go               # explicit URL
├── examples/
│   └── docker/
│       ├── postgresql/
│       │   ├── Dockerfile            # TWO-stage; copy only the runtime yaml into runtime
│       │   ├── ezx.setup.yaml
│       │   ├── ezx.runtime.yaml
│       │   └── pgbouncer.ini.tmpl
│       ├── apache-kafka/
│       └── mariadb/
└── DESIGN.md
```

### Why this shape

- `domain/` is data only. Types are **abstract** — `ReadinessProbe` uses an
  opaque `Type string` + `Config map[string]any` rather than a closed enum
  with fields for every probe strategy. This keeps the domain stable when
  new probe types are added. The same applies to `HealthCheck`, `Gate`
  conditions, and all other domain types.
- The top-level packages (one per concern: `orchestrator/`, `healthcheck/`,
  `reconciler/`, `scheduler/`, `setup/`, `registry/`, …) match
  `phpv`'s `silo/`, `assembler/`, `shim/`, etc. exactly. Each one
  is a `Service` that **defines its own `Repository` interface** and
  depends only on that interface — never on the implementation. The
  wiring entry in `app/` binds interface to implementation via fx.
- `internal/repository/{system,disk,memory,builtin}` is the only place that does
  real I/O. `system` already has `ProcessNodeRepository`; we **refactor** it to
  contain only pure I/O (`Start`, `Stop`, `Wait`, `Signal`) and move the
  orchestration logic (DAG ordering, readiness probing, reverse-drain) to
  `orchestrator/service.go`.
- Infrastructure packages that are not I/O but still framework-adjacent
  (`loader/`, `renderer/`, `credentials/`, `wildcard/`) live under
  `internal/` so they cannot be imported by `domain/` or by external
  consumers. This enforces the Dependency Rule at the compiler level.
- The two existing service files (`process/service.go`,
  `license/service.go`, `sbom/service.go`) become part of the new feature
  set, not deleted — they are the right shape. **Fix**: `process/service.go`
  must actually store the injected `ProcessNodeRepository` (currently it
  returns `&Service{}` without saving the dependency).

### What the `ProcessNodeRepository` becomes

Right now `Execute` does:

```go
cmd.Start()                                  // "started"
for child with NeedParentReady: spawn child  // assumes parent is "ready"
cmd.Wait()
```

This mixes two concerns: **I/O** (starting/stopping processes) and
**orchestration** (deciding when to start children, probing readiness,
reverse-DAG drain). In clean architecture, these must be separated:

- `internal/repository/system/process.go` — **I/O only**: `Start(ctx)`,
  `Stop(ctx)`, `Wait(ctx)`, `Signal(sig)`. No knowledge of DAG ordering
  or readiness probes.
- `orchestrator/service.go` — **orchestration only**: reads the
  `ProcessChain` DAG, calls repository methods in the right order,
  waits for readiness probes, implements reverse-DAG drain on shutdown.

The new shape:

```go
// internal/repository/system/process.go — pure I/O
type ProcessNodeRepository struct {
    ProcessNode domain.ProcessNode
}

func (r *ProcessNodeRepository) Start(ctx context.Context) (*exec.Cmd, error) {
    // Just start the process. No children, no readiness.
}

func (r *ProcessNodeRepository) Stop(ctx context.Context, cmd *exec.Cmd) error {
    // Send SIGTERM, wait for graceful shutdown, then SIGKILL.
}

func (r *ProcessNodeRepository) Signal(cmd *exec.Cmd, sig os.Signal) error {
    // Send an arbitrary signal.
}
```

```go
// orchestrator/service.go — orchestration only
type OrchestratorRepository interface {
    Start(ctx context.Context, node domain.ProcessNode) (*exec.Cmd, error)
    Stop(ctx context.Context, cmd *exec.Cmd) error
    Signal(cmd *exec.Cmd, sig os.Signal) error
}

type Service struct {
    repo OrchestratorRepository
    probe ReadinessProber          // also an interface, implemented in internal/repository/system/probe.go
}

func (s *Service) Execute(ctx context.Context, chain domain.ProcessChain) error {
    // 1. start non-blocking children (don't need parent ready)
    // 2. start this process
    // 3. wait for this process readiness (via s.probe)
    // 4. start blocking children (need parent ready)
    // 5. wait for all children
    // 6. on SIGTERM: reverse-DAG drain
}
```

`ReadinessProbe` is an **interface** defined in `orchestrator/`:

```go
// orchestrator/service.go
type ReadinessProber interface {
    Probe(ctx context.Context, probe domain.ReadinessProbe) error
}
```

The concrete implementations (`tcp`, `exec`, `http`, `postgres`,
`pgbouncer`, etc.) live in `internal/repository/system/probe.go`.
The `domain.ReadinessProbe` type is abstract — it carries an opaque
`Type string` and a `Config map[string]any` that the infrastructure
layer interprets. This way, adding a new probe type never touches
the domain or the orchestrator use case.

## 4. The two-stage YAML — schema design

### 4.1 Top-level shape

```yaml
# ezx.setup.yaml — build-time only, never reaches the final image.
# This is the abstract shape; see §5 for a real example.

apiVersion: ezx/v1             # schema version
kind: Bootstrapper             # one of: Bootstrapper, ProcessChain, Service
metadata:
  name: postgresql-stack       # human-readable
  description: Postgres + PgBouncer + Patroni
  version: 1.0.0
  author: supanadit
  tags: [database, postgresql]

stage: setup                   # one of: setup | runtime. The file ships in
                               # only one of the two Dockerfile stages. The
                               # binary refuses to run a file whose stage
                               # does not match the requested operation.

# ────────────── setup phase (this file lives in the BUILD stage) ─────────
setup:
  steps:
    - name: install-apt-deps
      run: apt-get update && apt-get install -y --no-install-recommends build-essential ...
    - name: build-postgresql
      requires: [install-apt-deps]
      env:
        POSTGRESQL_VERSION: "16.4"
      run: |
        wget .../postgresql-${POSTGRESQL_VERSION}.tar.gz
        ./configure --prefix=/usr/local/pgsql
        make -j"$(nproc)" world
        make install-world
    - name: build-pgbouncer
      requires: [install-apt-deps]
      env: { PGBOUNCER_VERSION: "1.23.1" }
      run: ./configure --prefix=/usr/local/pgbouncer && make && make install
      # ... extensions, patroni, pgbackrest, etc.

# (the runtime file lives at ezx.runtime.yaml in the same directory;
#  see §5.2 for the real example.)
runtime:
  envSchema:                   # the public contract — what users can set
    - name: PGDATA
      default: /var/lib/postgresql/data
    - name: POSTGRES_USER
      default: postgres
    - name: POSTGRES_PASSWORD
      secret: true             # never logged, never written to template
    - name: PGBOUNCER_LISTEN_PORT
      type: int
      default: 6432
    - name: PGBOUNCER_AUTH_TYPE
      enum: [md5, scram-sha-256, trust, cert, password]
      default: md5
    - name: PGBOUNCER_POOL_MODE
      enum: [session, transaction, statement]
      default: transaction
    - name: PGBOUNCER_MAX_CLIENT_CONN
      type: int
      default: 100
    - name: PGBOUNCER_DEFAULT_POOL_SIZE
      type: int
      default: 20
    - name: POSTGRESQL_SHARED_BUFFERS
      default: 128MB
    - name: POSTGRESQL_MAX_CONNECTIONS
      type: int
      default: 100
    - name: POSTGRESQL_TIMEZONE
      default: UTC
    - name: POSTGRESQL_READY_TIMEOUT
      type: int
      default: 60

  bootstrapper:                 # RE-USES the existing domain.Bootstrapper
    name: postgresql-stack
    processChain:
      roots:
        - name: postgresql
          needParentReady: false
          process:
            binaryPath: /usr/local/pgsql/bin/postgres
            arguments: ["-D", "{{ .Env.PGDATA }}"]
            workingDir: /var/lib/postgresql
          children:
            - name: pgbouncer
              needParentReady: true
              process:
                binaryPath: /usr/local/pgbouncer/bin/pgbouncer
                arguments: ["-d", "/etc/pgbouncer/pgbouncer.ini"]
              readinessProbe:                  # NEW — typed
                type: pgbouncer                # see §4.3
                host: 127.0.0.1
                port: 6432
                timeoutSeconds: 30
                intervalSeconds: 1
            - name: patroni
              needParentReady: true
              process:
                binaryPath: /usr/local/bin/patroni
                arguments: ["/etc/patroni/patroni.yml"]
              readinessProbe:
                type: http
                url: http://127.0.0.1:8008/patroni
                expectedStatus: 200

  files:                       # rendered BEFORE processes start
    - destination: /etc/pgbouncer/pgbouncer.ini
      template: |
        [databases]
        * = host=127.0.0.1 port=5432

        [pgbouncer]
        listen_addr = {{ .Values.PgBouncer.ListenAddr }}
        listen_port = {{ .Values.PgBouncer.ListenPort }}
        # ...
      values:                  # structured, NOT a stringly-typed env dump
        PgBouncer:
          ListenAddr: "0.0.0.0"
          ListenPort: 6432
          AuthType: md5
          PoolMode: transaction
      permissions: "0600"
      owner: postgres:postgres

  env:                         # exports inside the runtime container
    PGDATA: /var/lib/postgresql/data
    POSTGRES_USER: postgres
    POSTGRES_PASSWORD:
      $secret: postgresql/password   # resolved at runtime, see §4.4
    POSTGRES_INITDB_ARGS: "--encoding=UTF-8 --locale=C"

  healthcheck:                 # mirrors Docker HEALTHCHECK
    # The Docker HEALTHCHECK line in the Dockerfile is GENERATED by
    # ezx from this block (see "Health" section). Checks themselves
    # are declared per-process under processChain; this block is just
    # the global timing + the Docker wrapper command.
    intervalSeconds: 30
    timeoutSeconds: 10
    startPeriodSeconds: 60
    retries: 3
    # The actual list of checks lives under each ProcessNode:
    #   processChain.roots[].healthchecks[]
    #   processChain.roots[].children[].healthchecks[]

  shutdown:                    # replaces the trap-based shutdown script
    timeoutSeconds: 30
    drainOrder:                # explicit reverse-DAG ordering
      - pgbouncer
      - patroni
      - postgresql
    signals: [SIGTERM, SIGINT] # what the orchestrator listens for
```

### 4.2 Why a typed `values:` block

The current bash code does `sed -i "s|\${PGBOUNCER_LISTEN_ADDR}|${PGBOUNCER_LISTEN_ADDR}|g"`
on a template that has `${PGBOUNCER_LISTEN_ADDR}` placeholders. This has at
least three problems:

1. The template string is implicitly a contract — change the placeholder
   name, the sed silently does nothing.
2. Every "if env is set, append" loop is bespoke per config file.
3. The list of available knobs is only known by reading the script.

The YAML fix:

- `values:` is a structured map (Go-side: `map[string]any` typed as the
  specific config struct for the service).
- The template is Go `text/template`, so the IDE/typechecker sees
  `{{ .Values.PgBouncer.ListenAddr }}` — typos fail at load time.
- Unknown keys are rejected by the loader.

This eliminates 100% of the `sed -i` fragility.

### 4.3 Readiness probe types

The `domain.ReadinessProbe` type is **abstract** — it carries an opaque
`Type string` and a `Config map[string]any`. The concrete probe
implementations live in `internal/repository/system/probe.go` and are
never referenced by name in the domain. This means adding a new probe
type (e.g. `redis`) never touches `domain/` or any use case package.

The known probe implementations (all in `internal/repository/system/probe.go`):

| `type:`       | What it actually does |
|---------------|------------------------|
| `tcp`         | `net.DialTimeout("tcp", host:port)` succeeds |
| `exec`        | Runs the configured command, exit 0 |
| `http`        | `GET url`, expected status code |
| `postgres`    | `pg_isready` then `SELECT 1` over the unix socket |
| `pgbouncer`   | Connects to admin console, runs `SHOW POOLS;` |
| `patroni`     | `GET /patroni` on REST API, JSON `state: running` |
| `kafka`       | `kafka-broker-api-versions --bootstrap-server` |
| `mariadb`     | `mariadb -e "SELECT 1"` |

The current `wait_for_postgresql_ready()` polls with `sleep 1` up to 30
attempts. Same idea, but typed, with exponential backoff, with cancel on
context — and crucially **the orchestrator does not move on to children
until the probe passes**, instead of `sleep 2` + `pgrep` (which is what
the current code does for pgbouncer).

### 4.4 Secret resolution

`POSTGRES_PASSWORD: { $secret: postgresql/password }` becomes a normal
**env var** in the runtime contract. The user provides it via `docker
run -e POSTGRES_PASSWORD=...` (or via Docker secret, K8s secret, etc.).
The `$secret:` wrapper is only for cases where the secret is NOT in
the user's env and the operator wants to pull it from a file:

```yaml
runtime:
  envSchema:
    - name: POSTGRES_PASSWORD
      secret: true
      resolveFrom:               # lookup order, first hit wins
        - env: POSTGRES_PASSWORD
        - file: /run/secrets/postgresql/password
```

For v1, **no vault backend**. Just env → file. (Decision Q2 from the
"open questions" section.)

### 4.5 What happens to the bash scripts

The `setup/scripts/*.sh` chain becomes `setup.steps[]` (now
declarative, see §4.6 "Sources"). The
`entrypoint.d/scripts/init/0X-*.sh` chain becomes
`runtime.files[]` + the implicit ordering enforced by
`processChain`. The `entrypoint.d/scripts/runtime/startup.sh`
becomes `orchestrator/service.go`. The
`entrypoint.d/scripts/runtime/healthcheck.sh` becomes
`healthcheck/service.go`. The `utils/*.sh` library becomes
`internal/credentials/`, `internal/renderer/`, and shared helpers in
`internal/`.

The bash files do not get deleted on day one — they can co-exist for
one release, with the Dockerfile using the new `ezx` binary for one
service (the user said: "build and run postgresql just download this
program") while the others stay on bash. That's the migration path.

## 5. The postgresql sandbox (target schema)

Two files ship in the same `examples/docker/postgresql/` directory:
`ezx.setup.yaml` (build stage only) and `ezx.runtime.yaml` (the only
file the runtime image contains). The runtime file uses `envSchema`
to declare the public env-var contract — the user only ever sets
env vars, never mounts config files.

### 5.1 `ezx.setup.yaml`

```yaml
apiVersion: ezx/v1
kind: Bootstrapper
metadata:
  name: postgresql-sandbox
  version: 1.0.0
  tags: [database, postgresql]
stage: setup                    # binary reads this and runs setup.steps[]

setup:
  steps:
    - name: apt-base
      run: |
        apt-get update
        apt-get install -y --no-install-recommends \
          build-essential libreadline-dev zlib1g-dev libssl-dev \
          ca-certificates

    - name: postgres
      requires: [apt-base]
      env: { POSTGRESQL_VERSION: "16.4" }
      run: |
        wget -qO- https://ftp.postgresql.org/pub/source/v${POSTGRESQL_VERSION}/postgresql-${POSTGRESQL_VERSION}.tar.gz | tar xz
        cd postgresql-${POSTGRESQL_VERSION}
        ./configure --prefix=/usr/local/pgsql
        make -j"$(nproc)" world
        make install-world

    - name: pgbouncer
      requires: [apt-base]
      env: { PGBOUNCER_VERSION: "1.23.1" }
      run: |
        wget -qO- https://www.pgbouncer.org/downloads/files/${PGBOUNCER_VERSION}/pgbouncer-${PGBOUNCER_VERSION}.tar.gz | tar xz
        cd pgbouncer-${PGBOUNCER_VERSION}
        ./configure --prefix=/usr/local/pgbouncer
        make -j"$(nproc)"
        make install

    - name: cleanup
      requires: [postgres, pgbouncer]
      run: |
        apt-get purge -y build-essential
        rm -rf /usr/src/postgresql* /usr/src/pgbouncer*
        apt-get autoremove -y
        rm -rf /var/lib/apt/lists/*
```

### 5.2 `ezx.runtime.yaml`

```yaml
apiVersion: ezx/v1
kind: Bootstrapper
metadata:
  name: postgresql-sandbox
  version: 1.0.0
  tags: [database, postgresql, pgbouncer]
stage: runtime                  # binary reads this and runs the orchestrator

runtime:
  # ── PUBLIC CONTRACT (this is what users set via -e) ──────────────────
  envSchema:
    - name: PGDATA
      default: /var/lib/postgresql/data
    - name: POSTGRES_USER
      default: postgres
    - name: POSTGRES_DB
      default: postgres
    - name: POSTGRES_PASSWORD
      secret: true
      resolveFrom: [{ env: POSTGRES_PASSWORD }, { file: /run/secrets/postgresql/password }]
    - name: POSTGRES_INITDB_ARGS
      default: "--encoding=UTF-8 --locale=C"
    - name: PGBOUNCER_LISTEN_ADDR
      default: 0.0.0.0
    - name: PGBOUNCER_LISTEN_PORT
      type: int
      default: 6432
    - name: PGBOUNCER_AUTH_TYPE
      enum: [md5, scram-sha-256, trust, cert, password]
      default: md5
    - name: PGBOUNCER_POOL_MODE
      enum: [session, transaction, statement]
      default: transaction
    - name: PGBOUNCER_MAX_CLIENT_CONN
      type: int
      default: 100
    - name: PGBOUNCER_DEFAULT_POOL_SIZE
      type: int
      default: 20
    - name: PGBOUNCER_ADMIN_USERS
      default: postgres
    - name: PGBOUNCER_STATS_USERS
      default: postgres
    - name: POSTGRESQL_SHARED_BUFFERS
      default: 128MB
    - name: POSTGRESQL_MAX_CONNECTIONS
      type: int
      default: 100
    - name: POSTGRESQL_TIMEZONE
      default: UTC
    - name: POSTGRESQL_READY_TIMEOUT
      type: int
      default: 60

  # ── PROCESS GRAPH ────────────────────────────────────────────────────
  bootstrapper:
    name: postgresql-sandbox
    processChain:
      roots:
        - name: postgresql
          needParentReady: false
          process:
            binaryPath: /usr/local/pgsql/bin/postgres
            arguments: ["-D", "{{ .Env.PGDATA }}"]
            workingDir: /var/lib/postgresql
          readinessProbe:
            type: postgres
            user: "{{ .Env.POSTGRES_USER }}"
            timeoutSeconds: "{{ .Env.POSTGRESQL_READY_TIMEOUT }}"
          children:
            - name: pgbouncer
              needParentReady: true
              process:
                binaryPath: /usr/local/pgbouncer/bin/pgbouncer
                arguments: ["-d", "/etc/pgbouncer/pgbouncer.ini"]
              readinessProbe:
                type: pgbouncer
                host: 127.0.0.1
                port: "{{ .Env.PGBOUNCER_LISTEN_PORT }}"

  # ── INTERNAL TEMPLATES (private to the image, users never see) ───────
  # The user only ever sets env vars. The YAML's `fromEnv` rules turn
  # their env into the actual config file contents. This is the part
  # that the bash scripts do with sed/awk — the Go version is typed,
  # idempotent, and prints a clear error on typos.
  files:

    # 1. pgbouncer.ini — explicit, simple values from envSchema
    - destination: /etc/pgbouncer/pgbouncer.ini
      format: ini
      sections:
        databases:
          "*": "host=127.0.0.1 port=5432"
        pgbouncer:
          listen_addr:     "{{ .Env.PGBOUNCER_LISTEN_ADDR }}"
          listen_port:     "{{ .Env.PGBOUNCER_LISTEN_PORT }}"
          auth_type:       "{{ .Env.PGBOUNCER_AUTH_TYPE }}"
          pool_mode:       "{{ .Env.PGBOUNCER_POOL_MODE }}"
          max_client_conn:    "{{ .Env.PGBOUNCER_MAX_CLIENT_CONN }}"
          default_pool_size:  "{{ .Env.PGBOUNCER_DEFAULT_POOL_SIZE }}"
          admin_users:     "{{ .Env.PGBOUNCER_ADMIN_USERS }}"
          stats_users:     "{{ .Env.PGBOUNCER_STATS_USERS }}"
          logfile:         "/var/log/pgbouncer/pgbouncer.log"
          pidfile:         "/var/run/pgbouncer/pgbouncer.pid"
          user:            "postgres"
      permissions: "0600"
      owner: postgres:postgres

    # 2. postgresql.conf — the prefix-wildcard case
    #    POSTGRESQL_SHARED_BUFFERS, POSTGRESQL_MAX_CONNECTIONS, etc.
    #    come from envSchema. The POSTGRESQL_CONFIG_* wildcard covers
    #    any other knob the user wants to set without us hard-coding it.
    - destination: ${PGDATA}/postgresql.conf
      format: postgres_ini
      fromEnv:
        # explicit single-name → key mappings
        mappings:
          POSTGRESQL_SHARED_BUFFERS:        shared_buffers
          POSTGRESQL_MAX_CONNECTIONS:       max_connections
          POSTGRESQL_WORK_MEM:              work_mem
          POSTGRESQL_MAINTENANCE_WORK_MEM:  maintenance_work_mem
          POSTGRESQL_LISTEN_ADDRESSES:      listen_addresses
          POSTGRESQL_LOG_STATEMENT:         log_statement
          POSTGRESQL_LOG_DURATION:          log_duration
          POSTGRESQL_TIMEZONE:              timezone
          POSTGRESQL_UNIX_SOCKET_DIRECTORIES: unix_socket_directories
        # wildcard: any POSTGRESQL_CONFIG_<X>=Y becomes X = Y
        wildcards:
          - prefix: POSTGRESQL_CONFIG_
            keyTransform: lower
            valueTransform: auto    # 'quoted string' if contains non-alnum, else bare
        # policy when re-rendering an existing file
        policy: replace_or_append

    # 3. pg_hba.conf — the indexed-prefix case (the "a bit hacky" one)
    #    PG_HBA_ADD_1=..., PG_HBA_ADD_2=... become ordered lines.
    - destination: ${PGDATA}/pg_hba.conf
      format: postgres_hba
      managedBlock:
        marker: "# >>> ezx:pg_hba_add >>>"
        endMarker: "# <<< ezx:pg_hba_add <<<"
        onStart: remove
      fromEnv:
        wildcards:
          - prefix: PG_HBA_ADD_
            listSort: numeric        # sort by trailing index
            renderAs: line           # one env var = one line in the block

  healthcheck:
    intervalSeconds: 30
    timeoutSeconds: 10
    startPeriodSeconds: 60
    retries: 3
    checks:
      - type: postgres
        user: "{{ .Env.POSTGRES_USER }}"
      - type: pgbouncer
        port: "{{ .Env.PGBOUNCER_LISTEN_PORT }}"

  shutdown:
    timeoutSeconds: 30
    drainOrder: [pgbouncer, postgresql]
    signals: [SIGTERM, SIGINT]
```

### 5.3 The matching Dockerfile

```dockerfile
# syntax=docker/dockerfile:1.7
FROM debian:bookworm AS setup
ARG EZX_VERSION=0.1.0
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl
ADD https://github.com/supanadit/ezx/releases/download/v${EZX_VERSION}/ezx-linux-amd64 /usr/local/bin/ezx
RUN chmod +x /usr/local/bin/ezx
WORKDIR /src
COPY ezx.yaml ./ezx.yaml
# Stage autodetection via build arg.
ENV EZX_STAGE=setup
RUN ezx setup        # runs `setup.steps[]` in DAG order; bakes the binaries

FROM debian:bookworm-slim AS runtime
ARG EZX_VERSION=0.1.0
ADD https://github.com/supanadit/ezx/releases/download/v${EZX_VERSION}/ezx-linux-amd64 /usr/local/bin/ezx
RUN chmod +x /usr/local/bin/ezx
# Copy ONLY the runtime part of the YAML. The setup block is not present
# in this file, so even if the runtime image is exfiltrated the
# build-time secrets / commands never leave the build cache.
COPY ezx.runtime.yaml /etc/ezx/ezx.yaml
ENV EZX_STAGE=runtime
# Standard postgres dirs, ports, healthcheck.
RUN useradd -r -d /var/lib/postgresql -s /bin/bash postgres
USER postgres
ENTRYPOINT ["/usr/local/bin/ezx", "run", "--config", "/etc/ezx/ezx.yaml"]
```

`ezx.runtime.yaml` is the same file with the `setup:` block deleted by
the build pipeline (or hand-maintained). The `ezx setup` command
itself can emit a "rendered runtime YAML" with build-time values
substituted in, so the author can keep one source of truth.

### 5.4 Build-time caching: layers per step, not per script

The naive Dockerfile above has **one `RUN` per stage**, which
means **one Docker layer per stage**. If the user changes one
line of `setup.steps[]` — say, bumps `PGBACKREST_VERSION` from
2.58.0 to 2.59.0 — Docker invalidates the entire setup layer
and re-runs the whole `RUN ezx setup` from scratch. The
`apt-get update` runs again. The postgres source tree is
re-downloaded. Postgres re-compiles from source. **All that
work, even though 90% of the layer is unchanged.**

The current `container-scripts/postgresql/Dockerfile` has the
same problem: a single `RUN /opt/setup.sh` that calls 15
`0?-install-*.sh` files in order. One shell script = one layer =
no granular caching.

#### The fix: ezx emits one `RUN` per step

The `setup.steps[]` DAG already gives ezx the natural
boundaries. Each step is one logical unit of work, with its
own `Source`, its own build commands, its own dependencies.
ezx's `setup emit-dockerfile` subcommand generates a
Dockerfile that turns each step into its own `RUN`:

```dockerfile
# AUTO-GENERATED by `ezx setup emit-dockerfile --config ezx.setup.yaml`
# DO NOT EDIT. Edit ezx.setup.yaml instead.
# syntax=docker/dockerfile:1.7
FROM debian:bookworm-slim AS setup
ARG EZX_VERSION=0.1.0
ADD https://github.com/supanadit/ezx/releases/download/v${EZX_VERSION}/ezx-linux-amd64 /usr/local/bin/ezx
RUN chmod +x /usr/local/bin/ezx
WORKDIR /etc/ezx
COPY ezx.setup.yaml .

# One RUN per step. Each layer is cached independently by Docker.
# BuildKit cache mounts give each step its own persistent build dir.

# Step 1: apt-base (build tools + dev libraries)
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    ezx setup run-step --name apt-base

# Step 2: postgresql
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/ezx/build/postgresql,sharing=locked \
    ezx setup run-step --name postgresql

# Step 3: pgbouncer
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/ezx/build/pgbouncer,sharing=locked \
    ezx setup run-step --name pgbouncer

# Step 4: cleanup (always runs last; depends on everything)
RUN ezx setup run-step --name cleanup

FROM debian:bookworm-slim AS runtime
ARG EZX_VERSION=0.1.0
ADD https://github.com/supanadit/ezx/releases/download/v${EZX_VERSION}/ezx-linux-amd64 /usr/local/bin/ezx
RUN chmod +x /usr/local/bin/ezx
COPY ezx.runtime.yaml /etc/ezx/ezx.yaml
USER postgres
ENTRYPOINT ["/usr/local/bin/ezx", "run", "--config", "/etc/ezx/ezx.yaml"]
```

When the user changes only `PGBACKREST_VERSION`:

```
Docker build:
  Layer 1 (apt-base):     CACHED       (YAML copy is same → cache hit)
  Layer 2 (postgresql):   CACHED       (apt cache hit, no rebuild)
  Layer 3 (pgbouncer):    REBUILT      (version changed → cache miss)
  Layer 4 (cleanup):      CACHED       (cleanup's only input is "did anything before me change?" → no)
```

Total build time: **only pgbouncer re-runs**. Apt, postgres
source download, postgres compile, all skipped. Compare to the
naive Dockerfile where the whole setup stage re-runs.

#### The "dirty bit" problem: change the YAML and everything re-runs

The naive `RUN` sketch has a real problem. The user is
right to flag it. Let me trace through it concretely:

```dockerfile
WORKDIR /etc/ezx
COPY ezx.setup.yaml .                      # ← ONE copy of the WHOLE YAML

RUN --mount=type=cache,target=/var/cache/apt \
    ezx setup run-step --name apt-base
RUN --mount=type=cache,target=/ezx/build/postgresql \
    ezx setup run-step --name postgresql
RUN --mount=type=cache,target=/ezx/build/pgbouncer \
    ezx setup run-step --name pgbouncer
RUN ezx setup run-step --name cleanup
```

You change one line in `ezx.setup.yaml` (say, you bump
`PGBACKREST_VERSION` from `2.58.0` to `2.59.0`). Docker
sees the COPY's source checksum changed, so the cache for
**everything after that COPY** is invalidated. That means
**all four `RUN`s re-execute**, even though the apt-base,
postgresql, and cleanup steps have nothing to do with
pgbackrest.

The Docker-level cache gives you no benefit from a single
change. **All four layers re-run.**

**No, you do not need to hand-write one YAML per package.**
ezx does the splitting for you.

#### The fix: ezx emits one tiny file per step

`ezx setup emit-dockerfile --with-inputs` walks the
`setup.steps[]` DAG and produces a Dockerfile where each
`RUN` is preceded by a `COPY` of just that step's resolved
inputs. The author still writes **one** `ezx.setup.yaml` —
the per-step files are generated as a side artifact of
building the image.

Concretely, given this `ezx.setup.yaml`:

```yaml
setup:
  steps:
    - name: apt-base
      source: { apt: [build-essential, libreadline-dev, ...] }
    - name: postgresql
      requires: [apt-base]
      source:
        type: autotools
        registry: postgresql
        version: "16.4"
        checksum: { type: sha256, value: "24c45dd..." }
    - name: pgbouncer
      requires: [apt-base]
      source:
        type: autotools
        registry: pgbouncer
        version: "1.23.1"
        checksum: { type: sha256, value: "f6c8a87..." }
```

The author runs:

```bash
ezx setup emit-dockerfile --with-inputs
```

ezx produces a directory next to the Dockerfile:

```
ezx.setup.yaml          # the one file the author wrote
ezx.Dockerfile          # the generated Dockerfile
steps/
  apt-base.input        # the parts of the YAML apt-base depends on
  postgresql.input      # the parts the postgresql step depends on
  pgbouncer.input       # the parts the pgbouncer step depends on
```

`steps/postgresql.input` looks like:

```yaml
name: postgresql
source:
  type: autotools
  registry: postgresql
  version: "16.4"
  checksum: { type: sha256, value: "24c45dd..." }
requires: [apt-base]
```

It's the **smallest possible subset of the YAML** that
`postgresql` actually consumes. If the user changes
`PGBACKREST_VERSION`, only `steps/pgbouncer.input` is
regenerated; `steps/postgresql.input` is byte-for-byte
identical.

The generated `ezx.Dockerfile`:

```dockerfile
# AUTO-GENERATED by `ezx setup emit-dockerfile --with-inputs --config ezx.setup.yaml`
# DO NOT EDIT. Edit ezx.setup.yaml and re-run emit-dockerfile.
# syntax=docker/dockerfile:1.7
FROM debian:bookworm-slim AS setup
ARG EZX_VERSION=0.1.0
ADD https://github.com/supanadit/ezx/releases/download/v${EZX_VERSION}/ezx-linux-amd64 /usr/local/bin/ezx
RUN chmod +x /usr/local/bin/ezx
WORKDIR /etc/ezx

# Each step gets its own COPY of its own input. Bumping
# PGBACKREST_VERSION only changes steps/pgbouncer.input;
# only the pgbouncer RUN re-runs.

COPY steps/apt-base.input /etc/ezx/steps/apt-base.input
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    ezx setup run-step --name apt-base --input /etc/ezx/steps/apt-base.input

COPY steps/postgresql.input /etc/ezx/steps/postgresql.input
RUN --mount=type=cache,target=/ezx/build/postgresql,sharing=locked \
    ezx setup run-step --name postgresql --input /etc/ezx/steps/postgresql.input

COPY steps/pgbouncer.input /etc/ezx/steps/pgbouncer.input
RUN --mount=type=cache,target=/ezx/build/pgbouncer,sharing=locked \
    ezx setup run-step --name pgbouncer --input /etc/ezx/steps/pgbouncer.input

RUN ezx setup run-step --name cleanup

FROM debian:bookworm-slim AS runtime
# ... runtime stage unchanged ...
```

Now the cache behaves correctly:

```
User changes PGBACKREST_VERSION in ezx.setup.yaml.

ezx setup emit-dockerfile --with-inputs:
  steps/apt-base.input    — unchanged
  steps/postgresql.input  — unchanged
  steps/pgbouncer.input   — CHANGED (version 2.58.0 → 2.59.0)

Docker build:
  Layer (COPY apt-base.input)      — CACHED
  Layer (RUN apt-base)              — CACHED
  Layer (COPY postgresql.input)    — CACHED
  Layer (RUN postgresql)            — CACHED
  Layer (COPY pgbouncer.input)     — REBUILT (file changed)
  Layer (RUN pgbouncer)             — REBUILT
  Layer (RUN cleanup)               — CACHED
```

**Only pgbouncer re-runs.** Apt, postgres, cleanup all
hit Docker's cache directly. No work is redone for
unrelated steps.

#### How `--with-inputs` knows what each step depends on

The Go side maintains a `Inputs()` method on each
`Source` strategy:

```go
type Source interface {
    // ... existing methods ...
    // Inputs returns the subset of the YAML this Source
    // actually reads. Used by emit-dockerfile --with-inputs
    // to split the YAML for per-step COPYs.
    Inputs() []string
}

// autotools.go: reads version, checksum, configure, build, install, env
func (s *AutotoolsSource) Inputs() []string {
    return []string{"source.version", "source.url", "source.checksum",
                    "source.configure", "source.build", "source.install",
                    "source.env"}
}

// apt.go: reads the apt package list
func (s *AptSource) Inputs() []string {
    return []string{"source.apt"}
}
```

The generator walks each step, asks its `Source` for the
list of YAML keys it cares about, and writes a small
filtered YAML with only those keys. The runtime side
(`ezx setup run-step`) re-reads the same filtered YAML, so
the input set and the consumer are guaranteed to agree.

#### In-step cache: the second layer of defense

Even if Docker's layer cache somehow misses (e.g. you
hand-edited a comment in `steps/postgresql.input`), the
Go binary still has an **internal cache check**:

```go
func runStep(name string, inputPath string) error {
    input := readFile(inputPath)
    hash := sha256.Sum256(input)
    cacheKey := path.Join(buildDir, ".ezx/cache", name, hash)
    if fileExists(cacheKey + "/.done") {
        log.Info("step " + name + ": input hash matches cache, skipping")
        return nil   // ← work avoided even if Docker re-ran the layer
    }
    // ... actually do the work ...
    writeFile(cacheKey + "/.done", time.Now().Format(time.RFC3339))
}
```

This is the **second layer of defense**: even if Docker
invalidates the layer (because the COPY hash changed), ezx
detects "this step's inputs are unchanged" and skips the
actual work. The user gets **two** caches working in
their favor: Docker's coarse layer cache and ezx's
fine-grained input cache.

#### Do you need to hand-write one YAML per package?

**No.** The user writes **one** `ezx.setup.yaml`. ezx
emits the per-step `steps/<name>.input` files as a build
artifact. The author never has to manage N files by hand.

**But** — for power users who want full manual control
(e.g. CI systems that want to diff specific files in
code review), there's an opt-in: `source.manualInputs:
true` on a step tells ezx "don't generate this one,
expect it to exist already." That's the escape hatch for
`/etc/ezx/steps/<name>.input` as a tracked file in git.

The three modes side by side:

| Mode | Who manages step inputs? | Cache granularity | User files |
| ------ | -------------------------- | ------------------- | ----------- |
| **Default** (A) | ezx emits `steps/*.input` automatically | per-step | 1 YAML |
| **In-step cache only** (B) | one YAML, in-step cache as backup | per-step work, but whole-YAML COPY | 1 YAML |
| **Manual inputs** (C) | author hand-writes each `steps/*.input` | per-step | 1 YAML + N input files |

**A is the right default** for the v1 sandbox. B is a
strict fallback that we get for free from the in-step
cache. C is the escape hatch for users who want every
file under explicit version control.

#### What the user's existing knowledge buys

The user's existing Dockerfile already has **one** `RUN
/opt/setup.sh` (in `container-scripts/postgresql/Dockerfile`).
That `RUN` calls 15 install scripts. The current caching
behavior is: one line changes in `setup.sh` → all 15
scripts re-run → apt update, apt install, postgres
download, postgres compile, pgbouncer download, pgbouncer
compile, patroni pip install, etc. all happen again.

With ezx, the analog is: one line changes in
`ezx.setup.yaml` → only the **one** affected step's COPY
re-hashes → only that step's `RUN` re-executes → only
that step's actual work happens. **All the rest hit
cache.**

The user does not need to know anything about BuildKit
cache mounts, COPY invalidation, or layer ordering. ezx
emits the right Dockerfile from one YAML. The user
writes one file. The rest is automatic.

#### Two layers of cache, both automatic

ezx gets **two** layers of caching, both for free:

1. **Docker's native layer cache** (coarse, layer-level).
   Triggered by the COPY-of-YAML hash. Re-runs the layer's
   `RUN` command. This is what Docker does anyway.
2. **ezx's in-step input cache** (fine, step-level).
   Triggered by the step's own input hash. If Docker
   invalidates the layer (e.g. user tweaked a comment in the
   YAML), ezx still detects "this step's inputs are
   unchanged" and **skips the actual work** — no `apt
   install`, no re-download, no re-compile.

The step-level cache lives in `${EZX_BUILD_DIR}/.ezx/cache/`
and is keyed by a SHA-256 of the step's resolved input
(env vars, source URL, registry version, build command).

```
$ ls -la /ezx/build/.ezx/cache/
drwxr-xr-x  postgresql/   # input hash, build artifacts, install manifest
drwxr-xr-x  pgbouncer/
drwxr-xr-x  pgbackrest/
```

`ezx setup run-step --name X` is roughly:

```go
func runStep(name string) error {
    step := config.Steps[name]
    input := step.ResolvedInput()       // resolves env, templates, etc.
    hash := sha256.Sum256(input)
    cacheKey := path.Join(buildDir, ".ezx/cache", name, hash)
    if fileExists(cacheKey + "/.done") {
        log.Info("step already done, skipping")
        return nil
    }
    // ... actually run the step ...
    // on success:
    writeFile(cacheKey + "/.done", time.Now().Format(time.RFC3339))
}
```

So even when Docker's layer cache misses (YAML changed),
**most steps still skip** because their individual inputs
haven't changed. The user gets near-instant rebuilds.

#### Why the final image stays small

A common worry: "if every step is its own layer, the final
image is huge." It isn't, because:

- Each layer is **mostly a delta** from the previous layer.
  Docker uses overlay filesystems; identical files are shared
  between layers.
- The `cleanup` step is the **last** step, so it runs in the
  final layer. It does `rm -rf /ezx/build/* /var/cache/apt/*`
  etc. The final image size = previous layer + the
  cleanup's `rm` operations = tiny delta.
- The BuildKit cache mounts (`--mount=type=cache,target=...`)
  are **not** committed to the final image. They live in
  BuildKit's cache, not in the image's layers. This is the
  killer feature: you can have gigabytes of build cache that
  don't bloat the final image.

The `ezx` binary itself is in two layers (one per stage:
`setup` and `runtime`), but each is a single ~15MB binary.
Two copies = ~30MB, which is small for a database image.

#### What this replaces

- The single `RUN /opt/setup.sh` in the current
  `container-scripts/postgresql/Dockerfile` → N `RUN`s, one
  per `setup.steps[]` entry
- The hand-tuned `Dockerfile` the user has to maintain → an
  auto-generated one from `ezx setup emit-dockerfile`
- The "I bumped a version and now I have to wait 20 minutes
  for apt + postgresql to re-compile" pain → "I bumped
  pgbouncer, and the postgres layer is still cached, total
  build time is 30 seconds"

#### Migration plan for caching

This slots in as a small extension to milestone 6 (port
postgresql). After `ezx.setup.yaml` works and `ezx setup`
runs steps in order, the next sub-milestone is:

| # | Sub-milestone | Touches |
| - | ------------- | ------- |
| 6a | `ezx setup emit-dockerfile` — generates the Dockerfile from the YAML | new |
| 6b | `ezx setup run-step --name X` — runs one step, with in-step input cache | new |
| 6c | `ezx setup emit-dockerfile --with-inputs` — splits the YAML into per-step inputs | new |
| 6d | Verify: bumping PGBACKREST_VERSION re-runs only pgbouncer | new test |

The user's existing knowledge of "Docker has caching layers"
is exactly right. ezx just makes that caching **automatic**
and **per-step** instead of per-script.

### 4.6 Sources: declarative package installation

Looking at `setup/scripts/0?-install-*.sh`, there are **at least five
distinct download/build patterns** in the existing code:

| Pattern | Example today | Hand-rolled in bash |
| --------- | -------------- | --------------------- |
| **A. HTTP tarball + autotools** | postgresql | `curl -O ... && tar -xzf && ./configure && make && make install` |
| **B. Git clone + build** | cpython, patroni | `git clone -b ${VERSION} --depth 1 ... && pip install .` |
| **C. GitHub release archive** | pgaudit, pgbackrest | `curl -L -o ... https://github.com/.../archive/${VERSION}.tar.gz` |
| **D. apt package** | `build-essential`, `meson`, `ninja-build` | `apt-get install -y ...` |
| **E. Pip / npm / gem** | patroni's `psycopg`, `cdiff` | `pip install` |
| **F. Pre-built binary tarball** | golang, nodejs, kubectl | `wget ... && tar -C /usr/local -xzf` |

All six have:

- A `name` + `version`
- A set of **build-time dependencies** (apt packages needed before
  this step can run)
- A way to **derive the URL** from name+version
- A way to **build** (autotools, meson, plain tar, no-op)
- A way to **install** to a destination
- Optional **environment variables** to set during the build
  (e.g. `PG_CONFIG`, `PREFIX`, `LDFLAGS`)

The bash version has all of this as **imperative shell scripts**
that re-implement the same 5 patterns over and over. A version
bump is a hand-edit of a `curl` URL. A new package is a new
`0X-install-foo.sh` file. There is no central source of truth
for "what is the canonical URL pattern for an X.Y.Z release of
golang?"

The Go replacement: a typed **`Source`** field on each
`setup.steps[]` entry, with a small set of declarative
strategies + an optional `registry:` that resolves
`name@version` → `URL + checksum` (mirroring phpv's
`domain.Registry`):

```yaml
setup:
  steps:

    # Pattern D: apt packages
    - name: build-tools
      source: { apt: [build-essential, libreadline-dev, zlib1g-dev, libssl-dev, ca-certificates, meson, ninja-build, git, pkg-config] }

    # Pattern D: a tool we need later (Go is needed if we want to build something Go-based)
    - name: golang
      source:
        type: binary
        registry: go               # resolves to "go@VERSION → tarball URL" via the built-in registry
        version: "1.23.4"
        installTo: /usr/local/go
        env:
          PATH: "/usr/local/go/bin:$PATH"
        # No `build:` because it's a pre-compiled tarball.

    # Pattern A: HTTP tarball + autotools
    - name: postgresql
      requires: [build-tools]
      source:
        type: autotools
        registry: postgresql       # built-in registry
        version: "16.4"            # could be "{{ .Env.POSTGRESQL_VERSION }}" but locked for reproducibility
        url: "https://ftp.postgresql.org/pub/source/v16.4/postgresql-16.4.tar.gz"
        checksum: { type: sha256, value: "24c45dd0..." }
        configure: [./configure, --prefix=/usr/local/pgsql, --with-openssl, --with-uuid=ossp]
        build:    [make, "-j{{ .BuildJobs }}", world]
        install:  [make, install-world]
        env:
          PG_CONFIG: /usr/local/pgsql/bin/pg_config

    # Pattern B: git clone + build
    - name: patroni
      requires: [postgresql, golang]   # Note: golang is a transitive dep for some extensions
      source:
        type: git
        url: https://github.com/patroni/patroni.git
        ref: "v4.1.0"                  # tag, branch, or sha
        depth: 1
        build:    [pip, install, ".[etcd]"]

    # Pattern C: GitHub release archive + autotools
    - name: pgaudit
      requires: [postgresql]
      source:
        type: autotools
        registry: github
        repo: pgaudit/pgaudit
        version: "1.5.3"
        # `registry: github` resolves via GitHub Releases API:
        # https://github.com/pgaudit/pgaudit/archive/1.5.3.tar.gz
        configure: [make, "PG_CONFIG=/usr/local/pgsql/bin/pg_config", "USE_PGXS=1"]
        build:     [make, "PG_CONFIG=/usr/local/pgsql/bin/pg_config", "USE_PGXS=1"]
        install:   [make, "PG_CONFIG=/usr/local/pgsql/bin/pg_config", "USE_PGXS=1", install]
        env:
          PG_CONFIG: /usr/local/pgsql/bin/pg_config

    # Pattern F: pre-built binary tarball (no build)
    - name: golang                  # already declared above, but you can also do it inline
      source:
        type: binary
        url: https://go.dev/dl/go1.23.4.linux-amd64.tar.gz
        checksum: { type: sha256, value: "f6c8a87aa03b92c4b0bf3d558e28ea03006eb29db78917daec5cfb6ec1046265" }
        installTo: /usr/local/go
        # No configure/build — just untar.

    # Escape hatch: when the typed strategies don't fit
    - name: weird-thing
      source:
        type: script
        run: |
          # arbitrary shell, last resort
          set -euo pipefail
          # ...
```

### The registry: mirrors phpv's `domain.Registry`

The `registry:` field points to a **resolver** that maps
`name + version` → `URL + checksum`. The resolver is just a
`Source` strategy that knows the canonical URL pattern for a
known package.

`registry/` (use case layer) defines a `RegistryResolver`
interface, mirroring phpv's pattern:

```go
type RegistryResolver interface {
    Resolve(name, version string) (URL string, Checksum string, err error)
}
```

The **built-in resolver implementations** live in
`internal/repository/builtin/` (not in `registry/builtin/`):
`go.go`, `node.go`, `postgresql.go`, `github.go`, `pypi.go`,
`http.go`. This is the same separation phpv uses — the use
case package defines the interface; the infrastructure package
under `internal/` provides the implementation. The wiring in
`app/main.go` binds each implementation to the interface via
fx's `fx.As(new(...))` mechanism.

Adding a new package later is **one new file** in
`internal/repository/builtin/`, not a new shell script.

Built-in resolvers shipped in `internal/repository/builtin/`:

| `registry:` name | Resolves to | Example |
| ------------------ | ------------- | --------- |
| `go` | `https://go.dev/dl/go{VERSION}.linux-amd64.tar.gz` | `go 1.23.4` |
| `node` | `https://nodejs.org/dist/v{VERSION}/node-v{VERSION}-linux-amd64.tar.gz` | `node 20.10.0` |
| `postgresql` | `https://ftp.postgresql.org/pub/source/v{VERSION}/postgresql-{VERSION}.tar.gz` | `postgresql 16.4` |
| `pgbackrest` | `https://github.com/pgbackrest/pgbackrest/archive/release/{VERSION}.tar.gz` | `pgbackrest 2.58.0` |
| `pgbouncer` | `https://www.pgbouncer.org/downloads/files/{VERSION}/pgbouncer-{VERSION}.tar.gz` | `pgbouncer 1.23.1` |
| `github` | Resolves via the GitHub Releases API: `https://github.com/{repo}/archive/{version}.tar.gz` | `github repo=pgaudit/pgaudit version=1.5.3` |
| `pypi` | Resolves via the PyPI JSON API | `pypi package=patroni version=4.1.0` |
| `http` | Uses the explicit `url:` field as-is | `http` |

The user (or the image maintainer) can also define **custom
resolvers** in the YAML for private mirrors:

```yaml
setup:
  registries:
    - name: corp-postgresql
      type: http
      baseUrl: "https://artifacts.corp.example.com/postgresql/"
      checksumType: sha256
      # The resolver prepends baseUrl + name + "-" + version + ".tar.gz"
      # and downloads the checksum from {url}.sha256
    - name: corp-pypi
      type: pypi
      indexUrl: "https://pypi.corp.example.com/simple/"
      # Resolves like the public pypi resolver, but uses the private index
```

This is the **phpv pattern**: thin `RegistryRepository`
interface, pluggable implementations, default implementations
for the common cases.

### Version templates and reproducibility

The `version:` field accepts a string with optional Go template
syntax, so versions can be env-driven (e.g.
`{{ .Env.POSTGRESQL_VERSION }}`) but **only at the YAML
level**, not after the binary has been resolved. Once a step
starts, the version is fixed. A second `ezx setup` run with a
different env will rebuild from scratch (the bash `cleanup`
step pattern: cleanup deps the build, so re-running with a
different version triggers a full rebuild).

The `BuildJobs` template variable (e.g.
`make -j{{ .BuildJobs }}`) is filled by ezx from the
`--build-jobs` flag (default: `runtime.NumCPU()`).

### Why this is better than bash

A `0X-install-foo.sh` file today is a hand-coded mix of
`curl` / `wget` / `git clone` / `apt-get` / `pip install`
specific to one package. To add a new package, you copy-paste
an existing one, edit the URL, edit the build command, hope
you remembered the right deps. To upgrade a version, you edit
the URL and hope the upstream didn't change the directory
structure. To debug a checksum mismatch, you read 50 lines of
shell.

The Go version: each step is **declarative** (URL, checksum,
build command), the resolver knows the canonical pattern for
known packages, the URL is **typed** (you can't typo a path
that the resolver doesn't recognize), the checksum is
**verified before the build starts** (no half-installed
binaries from a corrupted download), and the build command
runs with `os/exec` instead of a bash subshell.

### What this replaces

- `docker/postgresql/setup/scripts/0?-install-*.sh` →
  `setup.steps[]` entries with a typed `Source` field
- The `cleanup` step's `rm -rf /usr/src/postgresql*` → ezx
  cleans up the **whole build directory** automatically after
  the install completes (per-step workdir, not a global `/temp`)
- The "if the URL changed, edit the script" workflow → bump
  the `version:` field; the resolver handles the rest

### What this doesn't do

It doesn't try to be a complete package manager. It's a thin
layer over **apt + curl + git + make** that knows a few common
URL patterns and a few common build systems. The
`source.type: script` escape hatch exists for the long tail
of "this one package is weird" cases. If a project needs more,
it can grow its own resolvers.

## 6. Migration plan (what to build, in what order)

| # | Milestone | Touches |
| --- | ----------- | --------- |
| 0 | Add `domain/config.go`, `domain/readiness.go`, `domain/template.go` (pure data) | `domain/` |
| 1 | Wire `go.uber.org/fx` in `app/`, add `internal/appctx/` + `internal/shutdown/` (port of phpv) | `app/`, `internal/` |
| 2 | Add `internal/loader/` (Viper → typed `domain.Config`) and `internal/renderer/` (`text/template` + `values:`) | new |
| 3 | Add `orchestrator/` (use case: DAG ordering, readiness probing, reverse-drain) + `internal/repository/system/process.go` refactored to I/O only (`Start`/`Stop`/`Wait`/`Signal`) + `internal/repository/system/probe.go` (probe implementations) | `orchestrator/`, `internal/repository/system/` |
| 4 | Add `cmd/setup/main.go` and `cmd/run/main.go`; route by `EZX_STAGE` | `app/`, `cmd/` |
| 5 | Add `healthcheck/` (port of healthcheck.sh, typed) | new |
| 5b | Add `scheduler/` (in-process cron, replaces backup-scheduler.sh and MariaDB's `/etc/cron.d`) | new |
| 5c | Add `registry/` (use case: `RegistryResolver` interface) + `internal/repository/builtin/` (resolver implementations) + `setup/` source strategies (apt / autotools / git / binary / script) — declarative package installation | new |
| 5d | Add `reconciler/` (use case: `Reconciler` interface + orchestration) + `internal/repository/system/actions.go` + `internal/repository/system/live.go` + `internal/repository/system/cache.go` — env vs live state, three modes: frozen / reconcile / reconcile-with-force — fixes the "can't change password" bug | `reconciler/`, `internal/repository/system/` |
| 6 | Port **postgresql** as the first end-to-end example. Keep the bash scripts in `container-scripts` for the rest | `examples/docker/postgresql/` |
| 7 | Port **apache-kafka** and **mariadb** | new examples |
| 8 | Drop bash once all stacks are migrated; delete `entrypoint.d/`, `setup.sh`, `setup/` from each service | `container-scripts` |

The user said: *"build and run postgresql just download this program and
simply create yaml file."* Milestone 6 is the day that promise is
fulfilled end-to-end.

## 7. What we keep

- `domain.Process`, `domain.ProcessNode`, `domain.ProcessChain`,
  `domain.Bootstrapper` — the existing data model is already mostly
  correct for the runtime half of the design. `NeedParentReady` is
  the primitive; what we're **adding** to `domain.ProcessNode` is:
  - `Optional bool` — if true, a crash logs a warning and does not
    fail the container's health. (See "Health" section.)
  - `Healthchecks []Healthcheck` — the per-process health checks
    scoped to this node. (Replaces the global `healthcheck.checks:`
    block — that block now only holds the Docker `HEALTHCHECK`
    timing/retries; the actual checks live per-process.)
  - `Scheduler *Schedule` — if non-nil, an in-process cron scheduler
    runs the declared jobs while this process is alive. (See
    "Schedules" section.) Tied to the process's lifecycle: starts
    with the process, stops on its shutdown.
  - `Reconcile []ReconcileEntry` — env-driven state changes that
    run on every boot, scoped to this process. (See
    "Reconciliation" section.) The `frozen` mode entries
    cover the existing bash behavior; the `reconcile` mode
    fixes the "can't change password" bug.
- `internal/repository/system/process.go` — keep, but **refactor** to
  contain only I/O methods (`Start`, `Stop`, `Wait`, `Signal`). The
  orchestration logic (DAG ordering, readiness probing, reverse-drain)
  moves to `orchestrator/service.go`. The probe implementations
  (`tcp`, `exec`, `http`, `postgres`, etc.) live in
  `internal/repository/system/probe.go`.
- Viper for YAML loading — it's already in `go.mod`.
- `conc` from `sourcegraph/conc` for parallel children (it's already
  in `go.sum`).
- `github.com/robfig/cron/v3` — **new** dep, the cron parser the
  scheduler uses. Battle-tested, the de-facto Go cron library.
- **phpv's `domain.Registry` pattern** is the model for
  `registry/`. The interface stays the same: a
  resolver takes `name + version` and returns `URL +
  checksum`. Implementations live in `internal/repository/builtin/`.

## 8. Decisions (resolved)

1. **Two YAML files, one binary.** `ezx.setup.yaml` + `ezx.runtime.yaml`.
   The binary reads `stage:` from the file. No env var guessing. ✅
2. **Secrets: env + file only** for v1. `runtime.envSchema[].secret: true`
   with `resolveFrom: [env, file]`. No vault. ✅
3. **No bash coexistence.** Don't bother porting `container-scripts`.
   Build a new sandbox `examples/docker/postgresql/` for a bare-bones
   postgresql (no extensions) as the v1 deliverable. The bash scripts
   in `container-scripts` are abandoned — they will be left in place
   as historical reference but not touched. New images use ezx only. ✅
4. **`stage:` field in YAML** (not `EZX_STAGE` env var). The file is
   the source of truth. The binary never has to guess. The `EZX_STAGE`
   env var was the original idea; replaced by the explicit field. ✅
5. **100% env-only at runtime.** The user never mounts a config file.
   The YAML's `envSchema` IS the public contract. Templates are
   private to the image maintainer. This is the whole point of ezx —
   bash can't do this reliably, Go can. ✅

## 9. Open questions (still open)

- Q5. For readiness probes with templated values
  (e.g. `port: "{{ .Env.PGBOUNCER_LISTEN_PORT | default 6432 }}"`),
  do we resolve at YAML-load time (binary startup) or at probe-execution
  time (each retry)? My recommendation: **load time**, because the env
  is fixed once the container starts. But that means a user can't
  change `PGBOUNCER_LISTEN_PORT` after start without a restart, which
  matches the bash behaviour today.
- Q6. Should `ezx setup` be **idempotent** (safe to re-run) or
  **fail-fast** (refuse to re-run)? Idempotency is nicer for
  iterative image development, but adds complexity.
- Q7. The `setup.steps[]` DAG — do we run steps in **parallel** where
  the DAG allows, or strictly serial? My recommendation: serial first
  (matches current bash), parallel later. Parallel `apt-get` calls are
  a known footgun.
- Q8. **Scheduler gate strictness.** The design above says
  `enabledWhen: [{ env: X, equals: "true" }, ...]` requires an
  exact-string match on `"true"` (no `1`/`yes`/`on` magic). For
  consistency with the rest of the system (which already does lenient
  boolean parsing for `PGBACKREST_ENABLE` in the bash scripts), do
  we want lenient parsing here too? My recommendation: **strict for
  scheduler gates** (the user said "explicit both" is the right DX),
  lenient for `envSchema` defaults (which only fill missing values).
  Two different contexts, two different rules.

## 10. Conclusion: what's solid, what's missing, what ships in v1

This document is 2271 lines. The actual code is 8 Go files.
Design-to-code ratio is **283:1**. That ratio is a smell.
I designed a lot, and not all of it is v1 work. This section
is the honest triage.

### What's solid (eight pillars, all agreed)

These eight designs are consistent, complete, and ready to
implement. They compose cleanly. They are the **full**
ezx design, but they don't all have to ship in v1.

1. **Two-stage YAML** (`ezx.setup.yaml` + `ezx.runtime.yaml`,
   `stage:` field, security story). 100% env-only at runtime.
2. **Four wildcard patterns** for env→config mapping
   (single / prefix / indexed-prefix / section-prefix).
3. **Per-process orchestrator** with typed readiness probes
   and reverse-DAG drain.
4. **Per-process healthchecks** with `severity: critical |
   warning` and `optional: true` for non-fatal processes.
5. **In-process scheduler** (robfig/cron) with explicit
   two-flag gate and per-job gates.
6. **Sources** (apt / autotools / git / binary / script)
   with phpv's `domain.Registry` pattern + per-step
   input cache + BuildKit cache mounts.
7. **Reconciler** for env vs live state (fixes the
   "can't change password" bug).
8. **Build-time caching** with per-step Docker layers,
   `emit-dockerfile --with-inputs`, and the in-step
   SHA-256 cache.

### What's missing (15 gaps the design didn't cover)

I should have asked about or designed for these but
didn't. Some are small; some are big. They are listed
honestly here so they aren't forgotten.

**Small (1–2 days each):**

- **Logging story.** The bash has 4-level logging
  (`log_debug`/`log_info`/`log_warn`/`log_error`) with
  `LOG_LEVEL` env var. I never designed `feature/log/`
  or said how the Go binary formats output.
- **CLI UX details.** Flags, `--help`, subcommand
  structure for `ezx setup`, `ezx run`, `ezx validate`,
  `ezx doctor`, `ezx status`. I sketched the names but
  not the surface.
- **Schema versioning.** `apiVersion: ezx/v1` is
  mentioned; behavior under v1 binary reading v2 YAML
  (or vice versa) is undefined.
- **Multi-arch.** ARM64 vs AMD64. The bash scripts run
  on the host arch; the Go binary and source downloads
  need to be arch-aware.
- **SBOM / license / provenance** integration. I
  designed `license/` and `sbom/` packages but never
  said how they integrate with the build artifact.
- **Mounted-config-file escape hatch.** I said "users
  never mount config files" — but a 12-factor purist
  will fight this. What's the override?
- **Unknown env vars.** Bash allows env passthrough
  (`POSTGRESQL_FOO=bar` just works). The Go version is
  strict. Is this a regression for some users?
- **Error messages at load time.** What does the
  binary say when the YAML is missing, malformed, or
  has an unknown `kind:`? I said "fail fast" but
  never wrote down the actual messages.

**Medium (1 week each):**

- **Test strategy.** phpv has tests everywhere. I
  designed 13 packages but no test infrastructure
  (table-driven, in-memory repos, golden YAML files).
- **Observability.** OpenTelemetry? Prometheus? Just
  structured logs? I never decided.
- **Migration path for existing bash users.** I said
  "abandon bash, build new sandbox image" but never
  designed a v0.9 where the user can run the new
  binary alongside the old scripts in the same image.
- **Signal handling matrix.** Bash handles SIGTERM,
  SIGINT, SIGHUP, SIGQUIT with different semantics.
  I mentioned "context.CancelFunc" but never wrote
  the full matrix.

**Big (multi-week, possibly its own design doc):**

- **HA / replication story.** I never designed
  primary/replica topology, streaming replication,
  patroni vs native, what happens on promotion. The
  bash code has 50+ env vars for this; I ignored all
  of them.
- **Setup-phase secrets.** Private mirror credentials,
  signing keys, pgbackrest stanza-create tokens.
  Runtime has `secret: true` in `envSchema`; setup
  has no equivalent.

These are **not blockers for v1** (basic postgresql
sandbox) but they are blockers for "replace
container-scripts entirely." Each one should become
its own design section when we get to it.

### What ships in v1 (the postgresql sandbox)

The user said: "first we need basic postgresql with no
extension." That is v1. **v1 is the smallest possible
thing that solves the original problem** (no more bash
fragility for the basic postgresql case). Everything
else in this document is v1.1+ work.

**v1 includes:**

- `domain/` types: `Process`, `ProcessNode`,
  `ProcessChain`, `Bootstrapper` (already exist)
- `internal/loader/` package: Viper → typed `Config`
- `orchestrator/` package: typed readiness probes,
  reverse-DAG drain (interface + service)
- `healthcheck/` package: critical/warning, but no
  per-process `severity` yet (one global flag)
- `internal/renderer/formats/` with two formats only:
  `postgres_ini` and `ini` (pgbouncer)
- `setup/` package: `apt` and `autotools` source
  strategies only (no git, no binary, no script yet)
- `internal/terminal/` with `setup` and `run` subcommands
  only
- `internal/appctx/` + `internal/shutdown/` (the phpv
  ports)
- `internal/repository/system/process.go` **refactored** to
  contain only I/O (`Start`, `Stop`, `Wait`, `Signal`) — no
  orchestration
- `internal/repository/system/probe.go` with the probe
  implementations (`tcp`, `exec`, `http`, `postgres`, etc.)

**v1 explicitly does NOT include:**

- The scheduler (no pgbackrest auto-backup yet)
- The reconciler (no live-state queries yet)
- Per-process `optional: true` and `severity:` flags
  (one global `healthcheck` block only)
- Multi-format rendering (just postgres_ini + ini)
- The four wildcard patterns (just the prefix one:
  `POSTGRESQL_CONFIG_*`)
- `reconciler/`, `scheduler/`, `registry/` packages
- `internal/wildcard/` (just the prefix mapping inline for v1)
- The build-caching story (one `RUN` per stage is
  fine for v1)
- `ezx status`, `ezx doctor`, `ezx validate` (just
  `ezx setup` and `ezx run`)

v1 demo: user downloads the binary, writes a
20-line `ezx.setup.yaml` and a 30-line `ezx.runtime.yaml`,
builds an image, runs `docker run -e POSTGRES_PASSWORD=foo
postgresql-sandbox:0.1`, gets a working postgresql with
pgbouncer, password is updatable, healthcheck works.
No extensions. No patroni. No pgbackrest. **That's the
v1 bar.** Everything else in this document is v1.1+.

### Path to v1.1, v1.2, v2

| Version | What's added | What it unblocks |
| --------- | -------------- | ------------------ |
| v0.1 | Skeleton: `domain/` + `fx` wiring + `internal/loader/`. Binary runs, prints "hello world" from YAML. | Nothing user-facing. Just proves the wiring works. |
| v0.5 | `orchestrator/` with one readiness probe. `setup/` with `apt` + `autotools`. Can build a postgresql image. | First user-testable artifact. |
| v1.0 | `healthcheck/`, `internal/renderer/formats/{postgres_ini,ini}`, `internal/terminal/{setup,run}`. **The basic postgresql sandbox.** | User can replace bash for the basic postgresql case. Ship it. |
| v1.1 | `scheduler/` (pgbackrest auto-backup), `internal/wildcard/` (the four patterns), `reconciler/` (password changes). | User can replace bash for postgresql with extensions. |
| v1.2 | Build-caching story (`emit-dockerfile --with-inputs`). | CI rebuilds are 30s, not 20min. |
| v2.0 | HA/replication story, setup-phase secrets, observability, migration path. Port kafka, mariadb. | Full replacement of `container-scripts`. |
| v3.0 | (Hypothetical) Other registries, distributed builds, multi-cluster. | Don't think about this yet. |

### What to do tomorrow

If the user wants to ship v1, the next 5 working days
should be:

1. **Day 1**: Add the new `domain/` types
   (`config.go`, `readiness.go`, `template.go`,
   `process_node.go`). Add `go.uber.org/fx` to `go.mod`.
   Wire the simplest possible fx app in `app/main.go`.
2. **Day 2**: Add `internal/loader/` (Viper → typed `Config`).
   Add `internal/appctx/` + `internal/shutdown/`. Wire
   the actual `ezx setup` and `ezx run` cobra
   subcommands. Binary still doesn't do useful work,
   but it loads a YAML and prints the parsed config.
3. **Day 3**: Add `setup/` with `apt` and `autotools`
   source strategies. `ezx setup` actually runs a DAG
   and installs packages.
4. **Day 4**: Add `orchestrator/` with one readiness
   probe type (`tcp`). `ezx run` actually starts a
   process and waits for it.
5. **Day 5**: Add `healthcheck/` and the
   `internal/renderer/formats/{postgres_ini,ini}` packages.
   Write the postgresql sandbox YAMLs. Build the
   example image. Ship v1.

This is a week of focused work. The rest of the
design (everything in §4.6, §2-schedules,
§2-reconciliation, §5.4) is v1.1+ and should be
deferred. Designing them in advance was useful for
finding the right shape, but **none of it has to be
implemented for v1.**

## 11. Lifecycle and Plugin System

The current design gives us **structural extension points** (the
`internal/repository/system/probe.go` for readiness probes,
`internal/repository/builtin/` for source resolvers, etc.) but no
**contractual plugin system**. A user who wants to add a custom
readiness probe, a custom reconciler action, a REST API endpoint, or
a custom scheduler has to fork ezx, add the code, rebuild, and
release. That is a fork, not a marketplace.

This section defines a proper **lifecycle system** with explicit
extension points and a **gRPC sidecar plugin model** (Linux-only)
so the user can extend ezx without touching the core binary.

> **Why Linux-only matters**: containers are overwhelmingly Linux
> (Docker Desktop on macOS/Windows runs a Linux VM). Dropping
> cross-platform lets us use Unix domain sockets, signal-based
> IPC, `/proc` introspection, and `prctl(PR_SET_PDEATHSIG)` for
> child process management — all of which are simpler, faster,
> and more reliable than the portable abstractions.

### 11.1 Lifecycle phases

ezx runs through six well-defined phases. Every phase is a
documented extension point. A plugin declares which phases it
participates in and which hooks it implements.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│  Phase 1: LOAD                                                              │
│  - Read YAML config                                                         │
│  - Validate against schema                                                  │
│  - Resolve secrets (env → file → vault → plugin)                            │
│  Extension points: SecretResolver, ConfigValidator                          │
├─────────────────────────────────────────────────────────────────────────────┤
│  Phase 2: SETUP (build-time only, in the Dockerfile)                       │
│  - Run setup.steps[] DAG                                                    │
│  - Download sources, verify checksums, build, install                       │
│  Extension points: SourceResolver, BuildRunner                             │
├─────────────────────────────────────────────────────────────────────────────┤
│  Phase 3: RENDER (runtime, before processes start)                          │
│  - Render config files from env + templates                                 │
│  - Write to disk with correct permissions                                   │
│  - Apply env transformations (plugin can rewrite env)                      │
│  Extension points: ConfigRenderer, FileFormatter, EnvTransformer            │
├─────────────────────────────────────────────────────────────────────────────┤
│  Phase 4: RUNTIME INIT (runtime, processes starting)                        │
│  - Start processes in DAG order                                             │
│  - Wait for readiness probes                                                │
│  - Run reconciler entries                                                   │
│  - Plugin can register HTTP/gRPC endpoints                                  │
│  Extension points: ReadinessProber, ReconcilerAction, ProcessLauncher,      │
│                     APIRegistrar                                            │
├─────────────────────────────────────────────────────────────────────────────┤
│  Phase 5: RUNTIME STEADY (runtime, while running)                           │
│  - Periodic healthchecks                                                    │
│  - Scheduled jobs (cron)                                                    │
│  - Periodic reconciler runs                                                 │
│  - Plugin long-running tasks (log forwarder, metrics push)                  │
│  Extension points: HealthChecker, Scheduler, ReconcilerAction,              │
│                     BackgroundTask                                          │
├─────────────────────────────────────────────────────────────────────────────┤
│  Phase 6: SHUTDOWN                                                          │
│  - Reverse-DAG drain                                                        │
│  - Plugin cleanup (close connections, flush buffers)                        │
│  - Temp file cleanup                                                        │
│  Extension points: ShutdownHook, CleanupHandler                            │
└─────────────────────────────────────────────────────────────────────────────┘
```

Each phase has a `Run(ctx, phase, ...)` entry point in the
respective use case package. The plugin manager walks all
registered plugins and calls the hooks they implement.

### 11.2 The extension point contract

A plugin is anything that implements the `Extension` interface
defined in `domain/lifecycle.go`:

```go
// domain/lifecycle.go — pure data + interface contract (no I/O)
type LifecyclePhase string

const (
    PhaseLoad        LifecyclePhase = "load"
    PhaseSetup       LifecyclePhase = "setup"
    PhaseRender      LifecyclePhase = "render"
    PhaseRuntimeInit LifecyclePhase = "runtime_init"
    PhaseRuntime     LifecyclePhase = "runtime"
    PhaseShutdown    LifecyclePhase = "shutdown"
)

type Extension interface {
    // Name returns a stable identifier (e.g., "pgvector-extension").
    Name() string
    // Phases returns the phases this extension participates in.
    Phases() []LifecyclePhase

    // Phase-specific hooks. Each is optional; nil = not participating.
    // The use case layer type-asserts the interface to a phase-specific
    // interface (e.g., ReadinessProber) before calling the hook.

    OnLoad(ctx context.Context, config *Config) error
    OnSetup(ctx context.Context, step *SetupStep) error
    OnRender(ctx context.Context, env Env, files []FileSpec) ([]FileSpec, error)
    OnRuntimeInit(ctx context.Context, node *ProcessNode) error
    OnRuntime(ctx context.Context, node *ProcessNode) error
    OnShutdown(ctx context.Context, node *ProcessNode) error
}
```

The use case layer discovers which hooks a plugin implements via
**type assertion** to phase-specific interfaces. This is the same
pattern NestJS and Angular use:

```go
// orchestrator/service.go — at runtime, the orchestrator does:
if prober, ok := ext.(ReadinessProber); ok {
    if err := prober.Probe(ctx, probe); err != nil { ... }
}
```

This means a plugin can implement **any subset** of extension
points. A simple plugin might only implement one hook. A
full-featured extension (like `pgvector-extension`) might implement
six.

### 11.3 The plugin model: auto-discovered binaries, two flavors

A plugin is a **Linux binary** that ezx finds by scanning one or
more directories. The user sets `EZX_EXTENSION` (or lists them in
`ezx.runtime.yaml` under `plugin_dirs:`) and ezx does the rest.

```bash
export EZX_EXTENSION="/opt/plugins:/usr/lib/ezx/plugins:$HOME/.ezx/plugins"
```

ezx scans each directory. For each file it finds, it decides what
to do based on the file type:

| File type | How it's loaded | How it self-describes |
| ----------- | ----------------- | ------------------------ |
| Executable file (sidecar) | `fork+exec`, gRPC over Unix socket | `<binary> --ezx-describe` prints JSON, exits 0 |
| `.so` file (in-process) | `plugin.Open()`, C ABI | `EzxPluginDescribe()` exported C symbol |
| Anything else | Skipped silently | — |

The binary is the **single source of truth** for its own metadata.
No separate `plugin.yaml`. No out-of-band configuration. The
binary knows its name, version, capabilities, and config schema
better than any sidecar file ever could.

#### Sidecar plugin (compiled binary)

A sidecar plugin is a regular Linux executable. It can be written
in any language (Go, Rust, C, Python — anything that can speak
gRPC). The host spawns it as a child process, waits for the gRPC
handshake, then calls hooks via gRPC over a Unix domain socket.

```go
// In the plugin's main.go (Go, Rust, C — any language)
func main() {
    if len(os.Args) > 1 {
        switch os.Args[1] {
        case "--ezx-describe":
            // Print self-description as JSON, exit 0
            json.NewEncoder(os.Stdout).Encode(GetDescriptor())
            return
        case "--ezx-serve":
            // Start gRPC server, talk to host over Unix socket
            ezxplugin.Serve(NewPgVectorExtension())
            return
        }
    }
    fmt.Fprintln(os.Stderr, "ezx plugin: use --ezx-describe or --ezx-serve")
    os.Exit(1)
}
```

Distributed as: a single static binary per arch. **Zero source
visible**. Can be obfuscated with `garble` (Go) or any C/Rust
obfuscator. Can be signed with cosign/Sigstore.

#### Shared library plugin (`.so`)

A `.so` plugin is a Linux shared library that exports a small C
ABI. The host loads it with Go's `plugin.Open()`, calls
`EzxPluginDescribe()` to get the metadata, then calls
`EzxPluginNew()` to get an instance.

```go
// In the .so (Go, compiled with -buildmode=plugin + garble)
package main

import "C"
import "unsafe"

//export EzxPluginDescribe
func EzxPluginDescribe() *C.char {
    return C.CString(`{
        "name": "pgvector-extension",
        "version": "0.7.0",
        "type": "shared_lib",
        "capabilities": [
            {"phase": "setup", "type": "source", "strategies": ["autotools"]},
            {"phase": "runtime_init", "type": "reconciler_action", "actions": ["create_extension_vector"]}
        ]
    }`)
}

//export EzxPluginNew
func EzxPluginNew() unsafe.Pointer {
    return unsafe.Pointer(&PgVectorExtension{})
}

// Must implement domain.Extension
type PgVectorExtension struct{}
func (p *PgVectorExtension) OnSetup(...) error { ... }
```

Distributed as: a single `.so` file per arch. **Zero source
visible**. Compiled with `garble` (for Go) or native symbol
obfuscation (for Rust/C/C++) to hide internal symbols. Can be
signed with cosign/Sigstore.

**The `.so` model supports any low-level language that can
produce a C-compatible `.so` file with exported C symbols.**
This is not a Go-only mechanism — Go's `plugin` package uses
the standard Linux `dlopen`/`dlsym` APIs, which work with any
language that exposes C ABI functions.

##### C ABI plugins (multi-language)

The only requirement is that the plugin exports two C-callable
symbols: `EzxPluginDescribe()` (returns a JSON string) and
`EzxPluginNew()` (returns an opaque pointer to a plugin
instance). Beyond that, the plugin's internal code can be in
any language.

**Rust:**

```rust
// src/lib.rs
use std::os::raw::{c_char, c_void};
use std::ffi::CString;

#[no_mangle]
pub extern "C" fn EzxPluginDescribe() -> *const c_char {
    let json = r#"{
        "name": "pgvector-extension",
        "version": "0.7.0",
        "type": "shared_lib",
        "capabilities": [
            {"phase": "setup", "type": "source", "strategies": ["autotools"]},
            {"phase": "runtime_init", "type": "reconciler_action", "actions": ["create_extension_vector"]}
        ]
    }"#;
    CString::new(json).unwrap().into_raw()
}

#[no_mangle]
pub extern "C" fn EzxPluginNew() -> *mut c_void {
    let plugin = Box::new(PgVectorExtension::new());
    Box::into_raw(plugin) as *mut c_void
}

#[no_mangle]
pub extern "C" fn EzxPluginFree(ptr: *mut c_void) {
    if !ptr.is_null() {
        unsafe { drop(Box::from_raw(ptr as *mut PgVectorExtension)) };
    }
}

struct PgVectorExtension { /* ... */ }
impl PgVectorExtension {
    fn new() -> Self { /* ... */ }
    fn on_setup(&self) -> Result<(), String> { /* ... */ }
}
```

```toml
# Cargo.toml
[lib]
crate-type = ["cdylib"]  # produces a C-compatible .so
```

**C++:**

```cpp
// plugin.cpp
#include <cstring>
#include <string>

extern "C" {
    const char* EzxPluginDescribe() {
        static const char* json = R"({
            "name": "pgvector-extension",
            "version": "0.7.0",
            "type": "shared_lib",
            "capabilities": [...]
        })";
        return json;
    }

    void* EzxPluginNew() {
        return new PgVectorExtension();
    }

    void EzxPluginFree(void* ptr) {
        delete static_cast<PgVectorExtension*>(ptr);
    }
}

class PgVectorExtension {
public:
    std::string onSetup() { /* ... */ }
    // ... other hook methods
};
```

**C:**

```c
// plugin.c
#include <string.h>
#include <stdlib.h>

typedef struct {
    // plugin state
} PgVectorExtension;

const char* EzxPluginDescribe() {
    return "{...}";  // JSON string
}

void* EzxPluginNew() {
    PgVectorExtension* p = malloc(sizeof(PgVectorExtension));
    // initialize
    return p;
}

void EzxPluginFree(void* ptr) {
    free(ptr);
}

// hook implementations
int PgVectorExtension_OnSetup(void* self) {
    PgVectorExtension* p = (PgVectorExtension*)self;
    // ...
    return 0;
}
```

**Go** (with `-buildmode=plugin`):

```go
// plugin.go (Go with //export for C ABI)
package main

/*
#include <stdlib.h>
*/
import "C"
import "unsafe"

//export EzxPluginDescribe
func EzxPluginDescribe() *C.char {
    return C.CString(`{
        "name": "pgvector-extension",
        "version": "0.7.0",
        "type": "shared_lib",
        "capabilities": [...]
    }`)
}

//export EzxPluginNew
func EzxPluginNew() unsafe.Pointer {
    return unsafe.Pointer(&PgVectorExtension{})
}

//export EzxPluginFree
func EzxPluginFree(p unsafe.Pointer) {
    // Go has GC, no explicit free needed
}

type PgVectorExtension struct{}
func (p *PgVectorExtension) OnSetup() error { /* ... */ }
```

```bash
# Build as Go plugin
$ go build -buildmode=plugin -o pgvector-extension.so .
```

**Trade-off** for Go plugins: the `.so` must be compiled with
the **same Go version** as the host ezx binary. This is a
real constraint — for Go plugins, the sidecar model (gRPC)
is usually simpler because it has no version coupling.

#### Why both models?

We support both because they serve different needs:

| Concern | Sidecar (compiled binary) | `.so` (shared library) |
| --------- | --------------------------- | ------------------------ |
| **Code protection** | ✅ Excellent (statically linked, obfuscated) | ✅ Excellent (compiled, obfuscated) |
| **Process isolation** | ✅ Independent process | ❌ Same process (panic = host crash) |
| **Go version coupling** | ✅ None (any version) | ⚠️ Only for Go plugins (Rust/C/C++ unaffected) |
| **glibc coupling** | ✅ None (statically linked) | ⚠️ Must match host's glibc (vendor can ship fully-static) |
| **Languages** | ✅ Any (gRPC-capable): Go, Rust, C, C++, Python, Ruby, Java, anything | ✅ C ABI capable: C, C++, Rust, Go, Zig, D, Swift (not Python/Ruby/Java) |
| **Performance** | ⚠️ IPC overhead per call | ✅ Direct function calls |
| **Distribution** | Single binary per arch | Single `.so` per arch |
| **Hot reload** | ✅ Safe (kill process, restart) | ⚠️ Possible but dangerous (no unload in Go) |
| **Commercial protection** | ✅ Source invisible, license enforcement built in | ✅ Source invisible, license enforcement built in |

**For most plugins, sidecar is the right choice**: language-
agnostic, version-independent, isolated. The `.so` model exists
for plugins that need **maximum performance** (e.g., a custom
config renderer called millions of times in a tight loop) where
the IPC overhead of gRPC would be a problem.

For the `.so` model:

- **C, C++, Rust, Zig, D vendors** get the full performance
  benefit with no Go version coupling concern.
- **Go vendors** should usually pick the sidecar model unless
  they specifically need the in-process performance (and are
  willing to ship per-Go-version builds).

Both models are first-class. The plugin chooses which one it is
in its self-described `type` field (`"sidecar"` or `"shared_lib"`).

#### Plugin descriptor (returned by both models)

The self-description is the same JSON shape for both models. The
host doesn't care which model produced it.

```go
type PluginDescriptor struct {
    Name         string          `json:"name"`
    Version      string          `json:"version"`
    Author       string          `json:"author"`
    Description  string          `json:"description"`
    License      string          `json:"license"`
    Type         PluginType      `json:"type"`         // "sidecar" | "shared_lib"
    Capabilities []Capability    `json:"capabilities"`
    ConfigSchema json.RawMessage `json:"config_schema"` // JSON Schema
}

type Capability struct {
    Phase   string   `json:"phase"`   // load | setup | render | runtime_init | runtime | shutdown
    Type    string   `json:"type"`    // probe | healthcheck | reconciler_action | source | renderer | scheduler | api | background_task | env_transformer | shutdown_hook | config_validator
    Actions []string `json:"actions,omitempty"` // for actions, the list of action names
}
```

### 11.4 User-facing plugin configuration (no manifest file needed)

The user never writes a `plugin.yaml`. The plugin binary itself
is the manifest. What the user **does** write is optional
**per-plugin configuration** in their `ezx.runtime.yaml`:

```yaml
# ezx.runtime.yaml
apiVersion: ezx/v1
stage: runtime

# Plugin directories (also via EZX_EXTENSION env var).
# ezx scans these on startup and auto-loads every valid plugin found.
plugin_dirs:
  - /opt/plugins
  - /usr/lib/ezx/plugins
  - $HOME/.ezx/plugins

# Optional: per-plugin configuration.
# Plugin existence is auto-discovered; this is just config.
# If a plugin is in plugin_dirs but not listed here, it loads with defaults.
# If a plugin is listed here but not in plugin_dirs, ezx warns at load time.
plugin_config:
  pgvector-extension:
    version_pin: "0.7.0"     # if multiple versions installed, pick this one
    enable: true             # default: true
    settings:
      vector_maintenance_work_mem: 256MB

  vault-secrets:
    enable: true
    settings:
      vault_addr: https://vault.example.com
      auth_method: kubernetes

  my-custom-plugin:
    enable: false            # discovered but disabled

runtime:
  # ... rest of the config
```

**No `plugins:` list.** ezx discovers them. The user can:

- **Override version** if multiple versions of the same plugin are installed
- **Disable a plugin** without uninstalling it (useful for A/B testing)
- **Pass configuration** that's specific to the plugin

But the user **never** declares which plugins exist. ezx finds
them by scanning directories.

### 11.5 First-party, third-party, and commercial plugins

| Type | Where it lives | Mechanism | When to use |
| ------ | ---------------- | ----------- | ------------- |
| **First-party (builtin)** | `internal/repository/builtin/` | In-process Go struct, compiled into the ezx binary | Ships with ezx, always available, zero overhead |
| **Third-party (sidecar)** | Any directory in `plugin_dirs` | Separate process, gRPC over Unix socket | Community extensions, hot-pluggable, language-agnostic |
| **Third-party (`.so`)** | Any directory in `plugin_dirs` | `plugin.Open()`, C ABI | Performance-critical extensions, single-language (Go) |
| **Commercial** | Any directory in `plugin_dirs` | Either sidecar or `.so` | Vendor ships protected code, embeds license check |

All four types implement the same `domain.Extension` interface.
The `PluginRepository` in `internal/repository/` has three
implementations:

- `internal/repository/builtin/` — first-party in-process
- `internal/repository/plugin/sidecar/` — third-party gRPC sidecar
- `internal/repository/plugin/sharedlib/` — third-party `.so` loader

fx wiring picks the right one per `PluginSpec.Type` at load time.

```go
// app/main.go — fx wiring (simplified)
fx.Provide(
    // First-party plugins (in-process, always available)
    fx.Annotate(builtin.NewPostgresProbe,     fx.As(new(plugin.PluginFactory), fx.GroupTags("factories"))),
    fx.Annotate(builtin.NewPgBouncerProbe,    fx.As(new(plugin.PluginFactory), fx.GroupTags("factories"))),
    fx.Annotate(builtin.NewAlterRoleAction,   fx.As(new(plugin.PluginFactory), fx.GroupTags("factories"))),

    // Third-party plugin loaders (sidecar + .so)
    fx.Annotate(sidecar.NewFactory,           fx.As(new(plugin.PluginFactory), fx.GroupTags("factories"))),
    fx.Annotate(sharedlib.NewFactory,         fx.As(new(plugin.PluginFactory), fx.GroupTags("factories"))),

    plugin.NewService,
)
```

The `plugin.Service` aggregates all factories. When it discovers
a plugin (via directory scan or explicit `plugin_config` entry),
it picks the right factory based on the descriptor's `Type` field.

### 11.6 The marketplace: distribution, not discovery

The marketplace is a **read-only HTTP registry** that helps users
**find and install** plugins. Once a plugin is installed (i.e., its
binary or `.so` is dropped into a `plugin_dirs` directory), ezx
discovers it automatically — the marketplace is **not** involved
in discovery at runtime.

The marketplace solves the "how do I find a good plugin?" problem,
not the "how does ezx know what plugins I have?" problem. Discovery
is solved by the directory scan.

```yaml
# https://marketplace.ezx.dev/v1/index.yaml
apiVersion: ezx/v1
kind: PluginIndex
generated: 2025-01-15T00:00:00Z
plugins:
  - name: pgvector-extension
    latest: 0.3.1
    versions:
      - 0.3.1
      - 0.3.0
      - 0.2.5
    description: pgvector for postgresql
    category: extension
    tags: [postgresql, vector, ai]
    repository: https://github.com/ezx-plugins/pgvector-extension
    binary_url: https://marketplace.ezx.dev/plugins/pgvector-extension/0.3.1/pgvector-extension-linux-amd64
    checksum: sha256:f6c8a87aa03b92c4b0bf3d558e28ea03006eb29db78917daec5cfb6ec1046265

  - name: vault-secrets
    latest: 1.0.0
    versions: [1.0.0, 0.9.2]
    description: HashiCorp Vault secret resolver
    category: secret
    tags: [vault, secrets, security]

  - name: prometheus-exporter
    latest: 0.5.0
    versions: [0.5.0, 0.4.1, 0.4.0]
    description: Prometheus metrics exporter
    category: observability
    tags: [prometheus, metrics, monitoring]
```

#### Installing a plugin

```bash
# Search the marketplace
$ ezx plugin search vector
pgvector-extension  0.3.1  Builds, installs, and manages pgvector for postgresql
pgvector-tools      0.1.0  Admin tools for pgvector indexes

# Install it (downloads binary, verifies checksum, drops into plugin_dir)
$ ezx plugin install pgvector-extension
[plugin] pgvector-extension@0.3.1: downloading from marketplace...
[plugin] pgvector-extension@0.3.1: checksum verified
[plugin] pgvector-extension@0.3.1: installed to /opt/plugins/pgvector-extension
[plugin] pgvector-extension@0.3.1: discovered and loaded (3 hooks registered)
```

The `install` subcommand is a thin wrapper: it downloads the
binary from the marketplace URL, verifies the checksum, and
drops it into the first writable directory in `plugin_dirs`.
After that, auto-discovery takes over.

#### Manual installation (no marketplace)

Users can also install plugins manually — just drop the binary
or `.so` into a `plugin_dirs` directory. The marketplace is
optional convenience, not a requirement.

```bash
# Vendor-supplied plugin (commercial, signed, etc.)
$ sudo cp pgvector-extension-enterprise-1.0.0 /opt/plugins/
$ sudo chmod 755 /opt/plugins/pgvector-extension-enterprise-1.0.0
# That's it. Next time ezx starts, it discovers and loads it.
```

#### Plugin verification

Every plugin is **checksum-verified** before execution. The
checksum comes from one of three sources:

1. The marketplace index (when installed via `ezx plugin install`)
2. The user's `ezx.runtime.yaml` (manual pin, for reproducibility)
3. A cosign/Sigstore signature attached to the binary (v2.0)

If the binary's checksum doesn't match any of these, the plugin
is **rejected at load time** and a clear error is logged:

```
[plugin] pgvector-extension: checksum mismatch
  expected: sha256:f6c8a87aa03b92c4b0bf3d558e28ea03006eb29db78917daec5cfb6ec1046265
  actual:   sha256:0000000000000000000000000000000000000000000000000000000000000000
  Hint: re-install via `ezx plugin install pgvector-extension`,
  or pin the expected checksum in ezx.runtime.yaml under
  `plugin_config.pgvector-extension.checksum`.
```

#### What runs on startup

```
ezx runtime --config ezx.runtime.yaml

# Auto-discovery from plugin_dirs
[plugin] scanning /opt/plugins ...
[plugin] scanning /usr/lib/ezx/plugins ...
[plugin] scanning $HOME/.ezx/plugins ...
[plugin] found 4 candidates: pgvector-extension, vault-secrets, prometheus-exporter, my-custom

# Self-description (each plugin reports what it is)
[plugin] pgvector-extension@0.3.1: sidecar, 3 capabilities
[plugin] vault-secrets@1.0.0: sidecar, 2 capabilities
[plugin] prometheus-exporter@0.5.0: sidecar, 1 capability
[plugin] my-custom: shared_lib, 2 capabilities

# User overrides from plugin_config
[plugin] my-custom: disabled by user config, skipping

# Checksum verification
[plugin] pgvector-extension@0.3.1: checksum OK
[plugin] vault-secrets@1.0.0: checksum OK
[plugin] prometheus-exporter@0.5.0: checksum OK

# Loading (sidecar = spawn process, .so = plugin.Open)
[plugin] pgvector-extension@0.3.1: starting sidecar (pid 42, socket /run/ezx/plugins/pgvector.sock)
[plugin] vault-secrets@1.0.0: starting sidecar (pid 43, socket /run/ezx/plugins/vault.sock)
[plugin] prometheus-exporter@0.5.0: starting sidecar (pid 44, socket /run/ezx/plugins/prometheus.sock)
[plugin] my-custom: loaded (skipped, disabled)

[plugin] 3 plugins loaded, 6 hooks registered across 4 phases
```

### 11.7 Edge case coverage

The user's concern is that "extension must cover almost any edge
case." The `Extension` interface plus phase-specific sub-interfaces
are designed to make this possible. Every conceivable extension
fits one of these patterns:

#### 1. **REST API endpoint** (`APIRegistrar`)

A plugin can expose HTTP endpoints that other plugins or external
tools can hit. This is the `prometheus-exporter` use case, or a
custom admin UI.

```go
// implemented by the plugin
type APIRegistrar interface {
    RegisterAPI(mux *http.ServeMux) error
}
```

```go
// internal/repository/plugin/grpc_plugin.go — translates gRPC
// stream into http.Handler registration on the local mux
```

Example: `pgvector-extension` exposes `GET /pgvector/info` to
return the installed version and index count, callable from
`docker exec` or a sidecar.

#### 2. **Custom healthcheck** (`HealthChecker`)

A plugin can register a custom healthcheck query that runs on
the standard healthcheck interval. The result feeds into the
same `severity: critical | warning` aggregator as first-party
checks.

```go
type HealthChecker interface {
    HealthChecks() []domain.HealthCheck  // declarative — no code to run
}
```

or, for checks that need custom logic:

```go
type HealthCheckRunner interface {
    RunHealthCheck(ctx context.Context, check domain.HealthCheck) (domain.CheckResult, error)
}
```

Example: `redis-plugin` registers `redis-cli PING` as a healthcheck
that runs every 30s and reports `severity: critical` if it fails.

#### 3. **Custom scheduler** (`Scheduler`)

A plugin can register a custom scheduler if `robfig/cron` isn't
enough. This is the `custom-scheduler` use case (Quartz-style
triggers, dependency chains, calendar-based schedules).

```go
type CustomScheduler interface {
    Schedule(jobs []domain.Job) (SchedulerHandle, error)
}

type SchedulerHandle interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    ListJobs() []domain.Job
}
```

The plugin's scheduler is **run by ezx** (so the lifecycle is
tied to the parent process), but the **scheduling logic** is
the plugin's responsibility.

#### 4. **Env manipulation** (`EnvTransformer`)

A plugin can transform environment variables between load time
and render time. This is the `consul-template` or `envsubst` use
case, or a secrets-to-env resolver that decodes a packed env blob.

```go
type EnvTransformer interface {
    TransformEnv(ctx context.Context, env Env) (Env, error)
}
```

Example: `consul-plugin` watches Consul KV at a path and injects
the values as env vars. The plugin runs `OnRuntime` in a loop,
firing `TransformEnv` when KV changes.

#### 5. **Custom source strategy** (`SourceResolver`)

A plugin can register a new download/build strategy for the
setup phase. The `npm`, `gem`, `cargo`, `pip` strategies all
fit here.

```go
type SourceResolver interface {
    ResolveSource(ctx context.Context, spec SourceSpec) (Source, error)
}
```

Example: `npm-plugin` lets the user write
`source: { type: npm, package: "@my-org/extension", version: "1.2.3" }`
in their `ezx.setup.yaml` and have the plugin handle the rest.

#### 6. **Custom reconciler action** (`ReconcilerAction`)

A plugin can register a new typed action for the reconciler.
The action takes a `ReconcileEntry` and applies it to the live
state.

```go
type ReconcilerAction interface {
    Apply(ctx context.Context, entry domain.ReconcileEntry) error
    Diff(ctx context.Context, entry domain.ReconcileEntry) (bool, error)
}
```

Example: `pgvector-extension` registers `create_extension_vector`
and `drop_extension_vector` actions, which the user references
in their `ezx.runtime.yaml`:

```yaml
runtime:
  processChain:
    roots:
      - name: postgresql
        reconcile:
          - env: PGVECTOR_ENABLED
            apply: create_extension_vector
            mode: reconcile
            target: { extension: vector }
```

#### 7. **Custom readiness probe** (`ReadinessProber`)

A plugin can register a new probe strategy. This is the same
interface that first-party probes implement.

```go
type ReadinessProber interface {
    Probe(ctx context.Context, probe domain.ReadinessProbe) error
}
```

Example: `vault-plugin` registers a `vault-secret-readable` probe
that checks a Vault token is valid before declaring the dependent
process ready.

#### 8. **Custom shutdown hook** (`ShutdownHook`)

A plugin can register cleanup logic that runs during shutdown,
after the reverse-DAG drain completes.

```go
type ShutdownHook interface {
    OnShutdown(ctx context.Context) error
}
```

Example: `log-forwarder` plugin flushes its log buffer to the
remote endpoint before exiting.

#### 9. **Long-running background task** (`BackgroundTask`)

A plugin can run a long-lived task that starts in `PhaseRuntimeInit`
and stops in `PhaseShutdown`. This is the log forwarder, metrics
pusher, or external state syncer use case.

```go
type BackgroundTask interface {
    Run(ctx context.Context) error  // blocks until ctx is cancelled
}
```

#### 10. **Config validator** (`ConfigValidator`)

A plugin can reject invalid YAML configurations at load time
before the runtime starts.

```go
type ConfigValidator interface {
    ValidateConfig(ctx context.Context, config *Config) []ValidationError
}
```

Example: `strict-mode-plugin` adds extra validation rules
(no shell metacharacters in paths, no uppercase env var names,
etc.) that the user can opt into.

### 11.8 Integration with clean architecture

The plugin system is **not** a separate architecture — it's a
**third implementation source** alongside the in-process
implementations in `internal/repository/memory/`, `disk/`, and
`system/`.

```
                    use case layer
                    (orchestrator/, healthcheck/, reconciler/, ...)
                              │
                              │ depends only on interfaces
                              ▼
    ┌──────────────────────────────────────────────────────────┐
    │                IMPLEMENTATIONS                           │
    │                                                          │
    │  internal/repository/memory/    ← tests (in-process)     │
    │  internal/repository/disk/      ← first-party (disk)     │
    │  internal/repository/system/    ← first-party (process)  │
    │  internal/repository/builtin/   ← first-party plugins    │
    │  internal/repository/plugin/    ← third-party (gRPC)     │
    │  internal/repository/marketplace/ ← marketplace (HTTP)   │
    └──────────────────────────────────────────────────────────┘
```

The use case layer **never knows** whether an `Extension` is:

- An in-process Go struct (`internal/repository/builtin/`)
- A gRPC client connected to a sidecar process
- A mock in a unit test
- A no-op stub for a phase the test doesn't care about

This is **exactly** the Dependency Inversion Principle in action,
and it's why the clean architecture is the right foundation —
plugins are a **trivial extension** of the existing interface
pattern. No new architecture, no special cases.

### 11.9 Concrete package layout

The plugin system adds these packages to the design:

```
ezx/
├── plugin/                            # USE CASE — plugin management
│   │                                 # Defines PluginRepository interface
│   ├── service.go                    # PluginManager: scan, describe, load, dispatch
│   ├── spec.go                       # PluginSpec, PluginDescriptor types
│   ├── discovery.go                  # directory scan, file-type detection
│   ├── lifecycle.go                  # phase dispatch, hook type-assertion
│   └── service_test.go
├── domain/                           # extends with:
│   ├── lifecycle.go                  # NEW — LifecyclePhase, Extension interface
│   └── extension_hooks.go            # NEW — ReadinessProber, HealthChecker, etc.
├── internal/
│   ├── repository/
│   │   ├── plugin/                   # NEW — third-party plugin loaders
│   │   │   ├── sidecar/              # gRPC sidecar loader
│   │   │   │   ├── client.go         # gRPC client wrapper
│   │   │   │   ├── process.go        # child process management (fork+exec, prctl)
│   │   │   │   ├── handshake.go      # initial capability negotiation
│   │   │   │   └── proto/            # generated protobuf code
│   │   │   │       ├── ezx_plugin.proto
│   │   │   │       └── ezx_plugin.pb.go
│   │   │   └── sharedlib/            # .so loader
│   │   │       ├── loader.go         # plugin.Open() wrapper
│   │   │       ├── describe.go       # EzxPluginDescribe symbol resolution
│   │   │       └── cgo_helpers.go    # C ABI shims for the loaded .so
│   │   ├── builtin/                  # extends (in-process first-party plugins)
│   │   │   ├── probes/               # readiness probes (postgres, pgbouncer, patroni)
│   │   │   │   ├── postgres.go
│   │   │   │   ├── pgbouncer.go
│   │   │   │   ├── patroni.go
│   │   │   │   └── kafka.go
│   │   │   ├── actions/              # reconciler actions (alter_role, create_extension)
│   │   │   │   ├── alter_role.go
│   │   │   │   ├── create_extension.go
│   │   │   │   └── set_setting.go
│   │   │   ├── sources/              # setup source strategies (apt, autotools, git, binary)
│   │   │   │   ├── apt.go
│   │   │   │   ├── autotools.go
│   │   │   │   ├── git.go
│   │   │   │   └── binary.go
│   │   │   ├── renderers/            # file format renderers (ini, hba, json, yaml, toml)
│   │   │   │   ├── ini.go
│   │   │   │   ├── postgres_hba.go
│   │   │   │   └── ...
│   │   │   └── api/                  # built-in API endpoints (status, metrics, health)
│   │   │       ├── status.go
│   │   │       └── health.go
│   │   └── marketplace/              # NEW — marketplace client (HTTP, optional)
│   │       ├── client.go             # fetch index, search, install
│   │       ├── verifier.go           # checksum verification, signing
│   │       └── installer.go          # download, verify, drop into plugin_dir
│   └── lifecycle/                    # NEW — lifecycle coordinator
│       ├── service.go                # walks phases, dispatches to plugin manager
│       └── service_test.go
└── examples/
    └── plugins/                      # example third-party plugins
        ├── pgvector-extension/       # real plugin example (sidecar)
        │   ├── main.go               # gRPC server entry point with --ezx-describe
        │   ├── go.mod
        │   └── README.md
        ├── prometheus-exporter/      # real plugin example (.so)
        │   ├── main.go               # Go plugin with C exports
        │   ├── go.mod                # buildmode=plugin
        │   └── README.md
        └── custom-scheduler/
            └── ...
```

Note: there is **no `plugin.yaml` manifest** anywhere in the
design. The plugin binary (whether sidecar or `.so`) is the
single source of truth for its own metadata. Users who want to
ship a plugin only need to ship the binary.

### 11.10 v1 scope for the plugin system

To keep v1 shippable, the plugin system is **progressively
delivered**:

| Version | What's in | What's out |
| --------- | ----------- | ------------ |
| **v1.0** | `Extension` interface in `domain/`, phase dispatch in `plugin/`, **first-party plugins only** (compiled into binary), auto-discovery from `plugin_dirs` (only first-party are discoverable) | Sidecar protocol, `.so` loader, third-party plugins, marketplace |
| **v1.1** | Sidecar protocol (`internal/repository/plugin/sidecar/`), `.so` loader (`internal/repository/plugin/sharedlib/`), auto-discovery of both types, self-description handshake, checksum verification, `plugin_config:` in `ezx.runtime.yaml` | Marketplace (HTTP registry), plugin install subcommand, plugin signing |
| **v1.2** | Marketplace client (`internal/repository/marketplace/`), `ezx plugin search` / `ezx plugin install` / `ezx plugin list` subcommands | Plugin signing, plugin web UI, hot-reload |
| **v2.0** | Signed marketplace (Sigstore/cosign), plugin web UI, hot-reload of plugins | — |

v1 still ships with the **architecture for plugins** — the
`Extension` interface, the phase dispatch, the package layout
— but the only plugins available are the first-party ones
compiled into the binary. Adding a third-party plugin in v1.1
is just "drop the binary into a `plugin_dirs` directory, done."
No manifest file, no YAML changes, no version pinning needed
unless the user wants to override the auto-discovered default.

This means **v1 proves the architecture works** (the dispatcher,
the lifecycle, the first-party plugins all use the same
`Extension` interface), and **v1.1 just adds the discovery
mechanism** for third-party binaries. The risk is bounded.

### 11.11 Why this matters

The current design treats ezx as a **single binary that does
everything**. The plugin system treats ezx as a **small kernel
that orchestrates plugins**. This is the same shift Kubernetes
made in 2014-2015 (from "Borg-style monolithic" to "CRI + CNI

- CSI plugin architecture"). The result was an ecosystem that
grew exponentially because:

- **Vendors** can ship products without forking the orchestrator
- **Users** can compose their own stacks from off-the-shelf pieces
- **The core** stays small, focused, and secure

ezx should aim for the same. The postgresql sandbox is v1.
The "compose your own database stack from a marketplace of
plugins" story is v1.1+.

### 11.12 Code protection for commercial plugins

A commercial vendor shipping a plugin needs to protect their
intellectual property. ezx's plugin model is **designed for this**:
both sidecar binaries and `.so` files are compiled artifacts
with no readable source code, and ezx never requires a separate
manifest or any other text file that would reveal the plugin's
internals.

#### What a vendor ships

A commercial plugin ships as **one binary** (plus optional docs).
Nothing else. The binary contains:

- The plugin code (compiled, obfuscated)
- The self-description metadata (name, version, capabilities)
- The config schema (JSON Schema)
- The license enforcement (key check, phone home, etc.)

There is **no source code, no manifest, no schema file, no docs
required for ezx to load it**. The user can drop the binary into
a `plugin_dirs` directory and it just works.

#### Sidecar binary protection (recommended for commercial)

A sidecar plugin is a **statically-linked Linux binary**. For
maximum protection:

```bash
# Vendor's build pipeline (Go example with garble for symbol obfuscation)
$ garble -literals -tiny build -o pgvector-extension-enterprise-1.0.0 .
$ file pgvector-extension-enterprise-1.0.0
pgvector-extension-enterprise-1.0.0: ELF 64-bit LSB executable, x86-64, statically linked, Go buildID=...

# Sign with cosign for supply chain security
$ cosign sign-blob --key cosign.key pgvector-extension-enterprise-1.0.0 > pgvector-extension-enterprise-1.0.0.sig

# Distribute (binary + signature + checksum)
$ ls -la
-rwxr-xr-x  pgvector-extension-enterprise-1.0.0       (8.2 MB, all Go runtime + plugin code)
-rw-r--r--   pgvector-extension-enterprise-1.0.0.sig   (signature)
-rw-r--r--   SHA256SUMS                                 (checksum)
```

**What the user sees** when they examine the binary:

```bash
$ ./pgvector-extension-enterprise-1.0.0 --ezx-describe
{"name":"pgvector-extension-enterprise","version":"1.0.0","type":"sidecar","capabilities":[...]}
```

That's it. No source, no internals, no way to read the code.
The vendor can use any combination of:

- **garble** (Go symbol + literal obfuscation)
- **Static linking** (no shared library dependencies to inspect)
- **UPX** (binary compression, also makes disassembly harder)
- **cosign/Sigstore** (cryptographic signature attached to the binary)
- **License key check** (plugin refuses to run without a valid key)

The vendor ships **one file** (plus the signature, which is also
binary). The user never sees readable Go source, never sees a
manifest with internal details, never sees a config schema in
a separate file that reveals the plugin's capabilities beyond
what `--ezx-describe` reports.

#### Shared library (`.so`) protection

A `.so` plugin has the same protection properties as a sidecar
binary, with the additional benefit of **no IPC overhead** (the
plugin runs in-process). The vendor uses the same obfuscation
toolchain:

```bash
# Vendor's build pipeline (Go with buildmode=plugin + garble)
$ garble -literals -tiny build -buildmode=plugin -o pgvector-extension-enterprise-1.0.0.so .
$ file pgvector-extension-enterprise-1.0.0.so
pgvector-extension-enterprise-1.0.0.so: ELF 64-bit LSB shared object, x86-64, BuildID=...

# Same signing + distribution
$ cosign sign-blob --key cosign.key pgvector-extension-enterprise-1.0.0.so > *.sig
$ ls -la
-rwxr-xr-x  pgvector-extension-enterprise-1.0.0.so    (8.2 MB, obfuscated, all Go runtime + plugin code)
-rw-r--r--  pgvector-extension-enterprise-1.0.0.so.sig
-rw-r--r--  SHA256SUMS
```

**Trade-off** vs sidecar: the `.so` runs in-process, so a panic
in the plugin **can** crash ezx. For commercial code that the
vendor has tested thoroughly, this is acceptable. For experimental
or untrusted code, the sidecar model is safer.

#### License enforcement (built into the plugin)

The vendor embeds license enforcement **inside the plugin binary**.
ezx doesn't enforce licenses (that's the vendor's job). Common
patterns:

- **License key file**: the plugin checks `/etc/ezx/license/<name>.key`
  on startup and refuses to run without a valid key.
- **Online activation**: the plugin calls the vendor's activation
  server on first run and caches a token locally.
- **Hardware binding**: the plugin binds the license to a machine
  ID (CPU serial, MAC address, etc.) and refuses to run on other
  machines.
- **Time-limited trial**: the plugin runs for N days without a
  license, then refuses to start.

ezx **doesn't see any of this** — the plugin's `OnLoad` hook
returns an error if the license check fails, and ezx treats it
the same as any other plugin error (log and skip).

#### What ezx guarantees to the vendor

ezx makes the following guarantees to plugin vendors:

1. **The plugin binary is the only file the user inspects.** There
   is no manifest, no schema file, no config file required.
2. **The self-description is opt-in.** The vendor chooses what
   capabilities to advertise. Internal hooks (not advertised via
   `--ezx-describe`) are completely hidden from the user.
3. **The plugin runs in isolation (sidecar).** A plugin crash
   doesn't crash ezx. A plugin misconfiguration is caught at
   load time and the plugin is skipped (other plugins continue).
4. **The plugin gets the same APIs as first-party code.** Whether
   the user adds a `vault-secrets` plugin or a `my-corp-vault`
   plugin, both implement the same `SecretResolver` interface.
   There's no "second-class citizen" for third-party plugins.
5. **The plugin can be updated independently.** The vendor ships
   a new binary, the user drops it into `plugin_dirs`, restarts
   ezx, done. No ezx upgrade required, no version compatibility
   matrix to manage.

#### What ezx does NOT guarantee

For transparency, ezx does **not** guarantee:

1. **Source code invisibility against determined reverse engineering.**
   No compiled binary is truly unreadable. ezx makes reverse
   engineering **hard and expensive**, not impossible. A motivated
   attacker with `objdump` / `ghidra` / `IDA` can still disassemble
   the binary. The protection is against casual inspection, not
   nation-state adversaries.
2. **Plugin portability across Go versions (for `.so` plugins only).**
   A `.so` plugin must be compiled against the same Go version
   ezx was built with. The vendor is responsible for shipping
   versions for each supported Go version. Sidecar plugins have
   no such constraint.
3. **Plugin portability across Linux distributions (for `.so` plugins only).**
   A `.so` plugin must be compiled against a compatible glibc.
   The vendor should ship a fully-static `.so` (using
   `CGO_ENABLED=0` for Go) to avoid this. Sidecar plugins are
   fully static by default and have no such constraint.

These trade-offs are documented so vendors can make informed
decisions about which model to use.

#### Why "low-level compiled binary" is the right answer

The user asked for "low-level programming" and "compiled unreadable
binary file." This is exactly the right call. The alternatives
(all rejected) were:

- **No plugins** — forces every extension to be a fork of ezx.
  The user has to maintain their own ezx fork forever. Not viable.
- **Plugin source in a known scripting language** (Python, Lua,
  JavaScript) — readable by anyone, no IP protection, performance
  overhead, and forces ezx to embed a script interpreter.
- **JSON/YAML manifest + interpreted config** — the manifest
  reveals the plugin's structure, the config is read at runtime,
  and there's no compiled code to enforce licensing.
- **Plugin source in a high-level Go package compiled at runtime**
  (Traefik's old Yaegi approach) — slow, insecure (full access to
  host's memory), and the source is still readable in the
  plugin package.

**The compiled-binary approach is the only one that gives the
vendor real IP protection, real performance, and a real
distribution story** (one file, checksum, signature, done).
It's the same approach HashiCorp uses for Vault, Nomad, and
Consul, and the same approach every commercial database vendor
uses for their plugins (Oracle, MongoDB, etc.).

### 11.13 Language support: what can a plugin be written in?

A common vendor question: "I want to write a plugin in Rust
(or C, or C++, or whatever). Can I use the `.so` model, or is
it Go-only?" The answer is: **the `.so` model supports any
low-level language that can produce a C-compatible `.so` file
with exported C symbols**. The sidecar model supports **literally
any language** that can speak gRPC.

#### The technical reason

Go's `plugin` package is a thin wrapper around the standard
Linux dynamic loading APIs: `dlopen()` to load the `.so` file
and `dlsym()` to resolve symbols. These are **C APIs** — they
can only find symbols with C linkage (i.e., functions declared
with C calling conventions and C name mangling).

This means:

- Any language that can produce a `.so` file with **C ABI exports**
  works with the `.so` model. The internal implementation can be
  in any language — only the **entry points** that the host
  (ezx) calls must be C-callable.
- Any language that can speak **gRPC** works with the sidecar
  model. The internal protocol between host and plugin is gRPC,
  which has bindings for every major language.

#### Language support matrix

| Language | `.so` model | Sidecar model | Notes |
| ---------- | ------------- | --------------- | ------- |
| **C** | ✅ Native | ✅ (via gRPC C库) | The original target of `dlsym`. No special considerations. |
| **C++** | ✅ (with `extern "C"`) | ✅ (via g++) | Needs `extern "C"` to prevent C++ name mangling. Trivial. |
| **Rust** | ✅ (with `#[no_mangle] pub extern "C"`) | ✅ (via `tonic` gRPC) | Cargo `crate-type = ["cdylib"]` for `.so`. First-class FFI. |
| **Go** | ✅ (with `-buildmode=plugin`) | ✅ (native) | Sidecar is simpler (no Go version coupling). |
| **Zig** | ✅ Native C ABI | ✅ (via gRPC) | `export fn` in C ABI. Excellent FFI story. |
| **D** | ✅ (with `extern(C)`) | ✅ (via gRPC) | `extern(C)` gives C linkage. |
| **Swift** | ⚠️ Possible on Linux | ✅ (via Swift gRPC) | `@_cdecl` annotation for `.so`. More complex than Rust. |
| **Nim** | ✅ (with `{.exportc, dynlib.}`) | ✅ (via gRPC) | First-class C interop. |
| **Crystal** | ✅ (with `extern`/`@[Link]` ) | ✅ (via gRPC) | C bindings are easy. |
| **Python** | ❌ (CPython uses its own ABI) | ✅ (via `grpcio`) | Use sidecar. Python `ctypes` can call C but Go can't load Python's `.so`. |
| **Ruby** | ❌ (Ruby C API, not C ABI) | ✅ (via `grpc` gem) | Use sidecar. |
| **Java/JVM** | ❌ (JNI, not C ABI) | ✅ (via `grpc-java`) | Use sidecar. |
| **JavaScript/Node.js** | ❌ (N-API is Node-specific) | ✅ (via `@grpc/grpc-js`) | Use sidecar. |
| **Lua** | ❌ (Lua C API, not C ABI) | ✅ (via `lua-grpc` or `lunatic`) | Use sidecar. |

#### Recommendation for vendors

- **Rust, C, C++, Zig, D vendors** (any low-level language):
  Both models work. Choose based on your needs:
  - **`.so` for max performance** (no IPC overhead, in-process)
  - **Sidecar for max safety** (process isolation, no version coupling)
  - For most plugins, sidecar is the simpler choice.

- **Go vendors**: Both models work, but the **sidecar model is
  usually the right choice** because it avoids the Go version
  coupling. Use `.so` only if you specifically need in-process
  performance and are willing to ship per-Go-version builds.

- **Python, Ruby, Java, Node.js vendors**: Use the **sidecar
  model only**. Your plugin is a gRPC server in your language
  of choice. You ship a single executable that wraps your code
  with a gRPC server (e.g., Python with `grpcio`).

#### Why this matters for the marketplace

The marketplace needs to be **language-agnostic**. A Rust vendor
should be able to publish a plugin as easily as a Go vendor. The
plugin format is just "a binary or `.so` that responds to
`--ezx-describe`" (or `EzxPluginDescribe` for `.so`). The
marketplace doesn't care what language the plugin is written in.

This is exactly how HashiCorp's plugin model works: Vault plugins
can be written in Go, Rust, or anything else. The marketplace
(or in HashiCorp's case, the `vault plugin register` command)
just stores the binary and its metadata.

### 11.14 Schema extensions: plugin-defined YAML properties

A third-party vendor shouldn't just be able to add new runtime
behavior — they should also be able to add new **YAML
properties** that the user writes in their `ezx.runtime.yaml`.
This is the same problem Kubernetes solved with **Custom
Resource Definitions (CRDs)**: a plugin can declare new fields
in the schema, the loader validates the user's YAML against
the merged schema, and the plugin's hooks read the
plugin-owned fields.

#### The problem with the current design

The current `domain.Config` is a **fixed Go struct** with
predefined fields. If a user writes:

```yaml
apiVersion: ezx/v1
kind: Bootstrapper
metadata:
  name: postgresql-stack
spec:
  middleware:           # ← plugin-defined, not in built-in schema
    redis:
      enabled: true
      max_memory: 1GB
  bootstrapper:
    name: postgresql-stack
    processChain: ...
```

The current loader (Viper → typed struct) would either:

- **Silently ignore** the `middleware:` field (most common, default Viper behavior)
- **Reject the YAML** with a "unknown field" error (strict mode)

Neither is acceptable. The user explicitly wants to use
`middleware:` (a plugin-defined field), and the plugin's code
needs to read it. The current design has no way to:

1. Tell the loader "this field is owned by plugin X, don't reject it"
2. Pass the plugin-owned field to the plugin's hooks
3. Validate the plugin-owned field's structure

#### The schema extension model

Each plugin can declare **schema extensions** — JSON Schema
fragments that get merged into ezx's built-in schema before
validation. The loader then validates the user's YAML against
the **merged schema** (built-in + all plugin extensions).

```go
// domain/lifecycle.go — extends Extension with schema capabilities
type Extension interface {
    // ... existing hooks ...

    // SchemaExtensions returns JSON Schema fragments that this
    // plugin adds to the top-level YAML schema. The loader merges
    // all fragments and validates the user's YAML against the
    // merged schema. Plugin-owned fields are then passed to the
    // plugin's hooks via RawExtensions.
    SchemaExtensions() []SchemaExtension
}

type SchemaExtension struct {
    // APIVersion is the schema group this extension belongs to.
    // Use "ezx/v1" to extend the built-in schema, or your own
    // group (e.g., "ezx.acme.io/v1") for an entirely separate
    // resource type. See "New kinds" below.
    APIVersion string

    // Scope determines where in the schema this extension applies.
    Scope SchemaScope

    // Key is the top-level YAML key this extension adds
    // (only used when Scope == ScopeTopLevel).
    Key string

    // Kind is the resource kind this extension defines
    // (only used when Scope == ScopeNewKind).
    Kind string

    // Path is a JSON Pointer (RFC 6901) into the existing
    // schema where this extension adds properties
    // (only used when Scope == ScopeExtendPath).
    // Example: "/properties/spec/properties" adds fields to spec.
    Path string

    // Schema is a JSON Schema fragment (draft 2020-12) that
    // describes the properties this plugin owns.
    Schema json.RawMessage
}

type SchemaScope string
const (
    // ScopeTopLevel adds a new top-level key to the YAML.
    // Example: "middleware:", "monitoring:", "backup:".
    ScopeTopLevel SchemaScope = "top_level"

    // ScopeExtendKind adds fields to an existing kind.
    // Example: add fields to Bootstrapper.spec.
    // Path = "/properties/spec/properties".
    ScopeExtendKind SchemaScope = "extend_kind"

    // ScopeExtendPath adds fields at an arbitrary JSON Pointer
    // path. More powerful than ScopeExtendKind, but error-prone.
    // Example: add a field to processChain.roots[].healthchecks.
    ScopeExtendPath SchemaScope = "extend_path"

    // ScopeNewKind defines an entirely new resource kind
    // (v1.1+). See "New kinds" below.
    ScopeNewKind SchemaScope = "new_kind"
)
```

#### Example: a `redis-middleware` plugin

A plugin that adds Redis as a middleware between postgresql and
pgbouncer would declare:

```go
func (p *RedisMiddlewarePlugin) SchemaExtensions() []domain.SchemaExtension {
    return []domain.SchemaExtension{
        {
            APIVersion: "ezx/v1",
            Scope:      domain.ScopeTopLevel,
            Key:        "middleware",
            Schema: json.RawMessage(`{
                "type": "object",
                "properties": {
                    "redis": {
                        "type": "object",
                        "properties": {
                            "enabled": {"type": "boolean", "default": false},
                            "max_memory": {"type": "string", "default": "256mb"},
                            "eviction_policy": {
                                "type": "string",
                                "enum": ["noeviction", "allkeys-lru", "volatile-lru"],
                                "default": "allkeys-lru"
                            },
                            "persistence": {
                                "type": "string",
                                "enum": ["none", "rdb", "aof", "both"],
                                "default": "rdb"
                            }
                        },
                        "required": ["enabled"]
                    }
                }
            }`),
        },
    }
}
```

The user can then write:

```yaml
apiVersion: ezx/v1
kind: Bootstrapper
metadata:
  name: postgresql-with-redis

spec:
  # Built-in fields (ezx's schema)
  bootstrapper:
    name: postgresql-stack
    processChain: ...

  # Plugin-defined field (redis-middleware plugin's schema)
  middleware:
    redis:
      enabled: true
      max_memory: 1GB
      eviction_policy: allkeys-lru
      persistence: rdb
```

The loader validates that `middleware.redis.eviction_policy` is
one of `noeviction | allkeys-lru | volatile-lru` (from the
plugin's schema), then passes the parsed `middleware:` value
to the plugin's hooks.

#### How the loader works

The loader (`internal/loader/`) does this on every YAML load:

```
1. Start with ezx's built-in schema (hardcoded in domain/)
                ↓
2. Call SchemaExtensions() on every loaded plugin
                ↓
3. Merge all plugin schema fragments into the built-in schema
   - ScopeTopLevel: add the key to top-level properties
   - ScopeExtendKind: merge into the kind's spec.properties
   - ScopeExtendPath: merge at the given JSON Pointer
                ↓
4. Parse the user's YAML into a generic map[string]any
                ↓
5. Validate the map against the merged schema
                ↓
6. Extract built-in fields into the typed domain.Config
                ↓
7. Keep plugin-owned fields in Config.RawExtensions
   (a map[string]any keyed by plugin name → field path → value)
                ↓
8. Pass domain.Config (with RawExtensions) to the runtime
```

The user gets **validation errors with clear messages** when
their YAML is wrong:

```
$ ezx validate --config ezx.runtime.yaml

✓ spec.bootstrapper.name: "postgresql-stack" (string, required OK)
✓ spec.bootstrapper.processChain: 2 roots, 1 child
✗ spec.middleware.redis.eviction_policy: "lru" is not a valid value
   Plugin: redis-middleware
   Valid values: noeviction, allkeys-lru, volatile-lru
   Hint: did you mean "allkeys-lru"?
```

#### How plugins read their owned fields

The plugin's hooks receive the `domain.Config` (or a phase-
specific subset) and can read `RawExtensions`:

```go
// internal/repository/plugin/sidecar/grpc_client.go
// (the loader passes RawExtensions as part of the hook call)

func (p *RedisMiddlewarePlugin) OnRender(
    ctx context.Context,
    cfg *domain.Config,
    files []domain.FileSpec,
) ([]domain.FileSpec, error) {
    // Extract the plugin-owned field
    middleware, ok := cfg.RawExtensions["middleware"].(map[string]any)
    if !ok {
        return files, nil  // not configured, skip
    }
    redis, ok := middleware["redis"].(map[string]any)
    if !ok {
        return files, nil
    }
    enabled, _ := redis["enabled"].(bool)
    if !enabled {
        return files, nil
    }

    // Render the redis.conf file using plugin-owned values
    maxMem, _ := redis["max_memory"].(string)
    evict, _ := redis["eviction_policy"].(string)
    persistence, _ := redis["persistence"].(string)

    redisConf := renderRedisConf(maxMem, evict, persistence)
    files = append(files, domain.FileSpec{
        Destination: "/etc/redis/redis.conf",
        Content:     redisConf,
        Permissions: "0640",
        Owner:       "redis:redis",
    })
    return files, nil
}
```

The plugin **owns** the field, the **ezx core never sees it**
(beyond the schema fragment for validation), and the user gets
a validated, type-safe configuration experience.

#### What plugins can do in v1

| Scope | Example | v1 support | Notes |
| ------- | --------- | ------------ | ------- |
| **Top-level key** | `middleware:`, `monitoring:`, `backup:` | ✅ Full | The most common case. Easy to validate, easy to pass through. |
| **Extend `Bootstrapper` spec** | Add `spec.middleware` to Bootstrapper | ✅ Full | Uses `ScopeExtendKind` with `Path: "/properties/spec/properties"`. |
| **Extend `ProcessNode`** | Add custom fields to process nodes | ✅ Full | Uses `ScopeExtendPath` with the appropriate JSON Pointer. |
| **Add new enum value to built-in field** | A new probe `type: vault-secret-readable` | ✅ Full | Already supported by the abstract `ReadinessProbe` design. |
| **Define entirely new `kind:`** | `kind: CronJob`, `kind: Middleware` | ❌ v1.1+ | Requires kind dispatch, multi-resource YAML support. |

#### New `kind:` values (v1.1+)

Defining an entirely new `kind:` is a bigger change because it
requires:

1. **Kind dispatch**: the loader needs to know which plugin
   handles `kind: CronJob` and route the parsed YAML to it
2. **Multi-resource YAML**: support `---` separators like
   Kubernetes (`apiVersion: ezx/v1\nkind: Bootstrapper\n---\napiVersion: ezx.cron/v1\nkind: CronJob`)
3. **Cross-kind references**: `kind: CronJob` referencing
   `kind: Bootstrapper` for its container image

This is **not v1 work**. For v1, plugins can extend the
existing `Bootstrapper` kind with new top-level keys and
fields, which covers 90% of the use cases. New `kind:`
support is a v1.1+ feature with its own design pass.

#### Backward compatibility

What happens if a user writes a `middleware:` field in their
YAML but the `redis-middleware` plugin is not installed?

- The loader **accepts the field** (the schema is permissive
  for unknown fields by default, or strict if the plugin is
  installed and registered its schema)
- The runtime **logs a warning**: `middleware.redis: plugin
  redis-middleware not installed, ignoring this field`
- The container **starts normally** (degraded mode — the
  feature is not applied, but nothing breaks)

This is the principle of least surprise: the YAML works, but
the user is informed that the plugin they assumed was present
isn't.

If the user wants strict mode (reject unknown fields), they
can set `strict_schema: true` in their `ezx.runtime.yaml`:

```yaml
apiVersion: ezx/v1
kind: Bootstrapper
metadata:
  name: postgresql-stack

spec:
  strict_schema: true   # reject any field not owned by ezx or a registered plugin

  bootstrapper: ...
  middleware: ...        # must have redis-middleware plugin installed
```

#### Why not just pass everything as `interface{}`?

A simpler design would be: ignore all unknown fields, pass
everything in a generic `map[string]any`, let plugins figure
it out. This is what Viper does by default.

The downside: **no validation**. The user writes
`eviction_policy: lru` (typo), the plugin silently treats it
as a no-op, and the user has no idea why their config isn't
working. With schema extensions, the loader catches the typo
at `ezx validate` time and gives a clear error message.

Schema extensions are the **difference between "ezx works"**
and "ezx works and tells you when you're using it wrong."

#### Why this matters for the marketplace

This is what makes the **marketplace story** actually work.
Without schema extensions, every plugin is a **black box** that
the user can't configure in their YAML — they'd have to use
the plugin's own CLI, env vars, or config files.

With schema extensions, a plugin becomes a **first-class
citizen** in the user's `ezx.runtime.yaml`. The user sees
the plugin's options inline, gets validation, gets IDE
completion (via the merged JSON Schema), and can use the
same `ezx validate` command for everything.

This is exactly the Kubernetes UX:

```bash
kubectl explain middleware.spec.redis
kubectl get middleware
kubectl edit middleware my-redis
```

ezx can offer the same:

```bash
ezx explain middleware.spec.redis
ezx validate --config ezx.runtime.yaml
ezx plugin schema redis-middleware  # dumps the plugin's schema fragment
```

#### v1 scope for schema extensions

| Feature | v1 | v1.1+ |
| --------- | --- | ------- |
| `ScopeTopLevel` (new top-level key) | ✅ | ✅ |
| `ScopeExtendKind` (extend Bootstrapper) | ✅ | ✅ |
| `ScopeExtendPath` (extend nested field) | ✅ | ✅ |
| New enum value for built-in field | ✅ | ✅ |
| `ScopeNewKind` (entirely new `kind:`) | ❌ | ✅ |
| Multi-resource YAML (`---` separator) | ❌ | ✅ |
| Cross-kind references | ❌ | ✅ |
| `ezx explain` subcommand | ❌ | ✅ |
| `ezx plugin schema <name>` subcommand | ❌ | ✅ |

v1 ships with **90% of the schema extension use cases** —
plugins can add their own top-level keys and extend the
existing `Bootstrapper` kind. The remaining 10% (new `kind:`
values, multi-resource YAML) is a v1.1+ feature that requires
its own design pass.

## 12. File rendering reconciliation

The design's §2 "Reconciliation" section addresses env-driven
state for the **database** (passwords, settings, roles). But the
same problem exists for **config files** rendered from env vars,
and the current file rendering design does not solve it.

### 12.1 The gap: env var removal is a no-op

Consider this file spec from §4.1:

```yaml
runtime:
  files:
    - destination: /etc/pgbouncer/pgbouncer.ini
      format: ini
      fromEnv:
        prefix: PGBOUNCER_CONFIG_
        keyTransform: lower
        valueTransform: none
        policy: replace_or_append
```

**First start** — user has `PGBOUNCER_CONFIG_POOL_SIZE=20`:

```ini
# /etc/pgbouncer/pgbouncer.ini (rendered)
[pgbouncer]
listen_port = 6432      # from template default
pool_size = 20          # from PGBOUNCER_CONFIG_POOL_SIZE
```

**Second start** — user removes `PGBOUNCER_CONFIG_POOL_SIZE`:

```ini
# /etc/pgbouncer/pgbouncer.ini (rendered)  ← same as before!
[pgbouncer]
listen_port = 6432
pool_size = 20          # ← stale, but no env var set
```

The `policy: replace_or_append` semantics only handle
**additions and updates**. When an env var is **removed**, the
corresponding config line stays in the file. The env is no
longer the source of truth — the file is.

This is the **exact same problem** as the reconciler solves
for database state: env-driven state that must survive across
boots, and must be **reversible** (the user can revert a change
by removing the env var).

### 12.2 The fix: apply the reconciliation model to file rendering

The reconciler already has the right model:

| Reconciler (database state) | File rendering (config files) |
| ---------------------------- | ------------------------------- |
| `reconcile:` block on a `ProcessNode` | `files:` block in runtime config |
| `Reconcile(ctx, entry, live, env)` | `Render(file, env)` |
| `LiveStateQuerier` (queries postgres) | `FileReader` (reads current file) |
| `AppliedStateCache` (`${PGDATA}/.ezx/applied/`) | `RenderedStateCache` (`${PGDATA}/.ezx/rendered/`) |
| `frozen` / `reconcile` / `reconcile-with-force` | `frozen` / `reconcile` / `append_only` |
| `EZX_FORCE_RECONCILE`, `EZX_FORCE_INIT`, `EZX_DRY_RUN` | `EZX_FORCE_RENDER`, `EZX_DRY_RUN` |

We extend the file renderer with the same three modes and the
same diff-based reconciliation. The **env is the source of truth**;
the **file is the rendered state**; the **state file is the cache**.

### 12.3 The diff algorithm

On every render, the renderer does:

```
1. Read the current env (EZX_* + the runtime envSchema)
2. Read the template (the default state)
3. Read the state file (${PGDATA}/.ezx/rendered/<file>.json)
4. Compute the desired state: template defaults + env overrides
5. Compute the diff:
   - For each env var in the state file's `applied`:
     - If still set with same value → no-op
     - If still set with different value → update
     - If no longer set → REMOVE the override, revert to template
   - For each env var in the current env:
     - If not in state file's `applied` → add the override
6. Apply the diff to the file (in-place edit, preserving manual changes)
7. Write the new file atomically
8. Update the state file with the new `applied` values
```

### 12.4 The state file (provenance tracking)

The state file is stored alongside the reconciler's state:

```
${PGDATA}/.ezx/
├── applied/                          # reconciler state (from §2)
│   ├── postgresql.password.json
│   └── postgresql.replication_user.json
└── rendered/                         # NEW: file rendering state
    ├── pgbouncer.ini.json
    ├── postgresql.conf.json
    └── pg_hba.conf.json
```

Each state file records the **provenance** — which config keys
came from which env vars, and what the template defaults were:

```json
// ${PGDATA}/.ezx/rendered/pgbouncer.ini.json
{
  "destination": "/etc/pgbouncer/pgbouncer.ini",
  "last_rendered": "2025-01-15T00:00:00Z",
  "format": "ini",
  "env_to_key": {
    "PGBOUNCER_CONFIG_POOL_SIZE":       "pool_size",
    "PGBOUNCER_CONFIG_MAX_CLIENT_CONN": "max_client_conn"
  },
  "applied": {
    "PGBOUNCER_CONFIG_POOL_SIZE":       "20",
    "PGBOUNCER_CONFIG_MAX_CLIENT_CONN": "100"
  },
  "template_defaults": {
    "pool_size":         "10",
    "max_client_conn":   "100",
    "listen_port":       "6432",
    "listen_addr":       "0.0.0.0",
    "auth_type":         "md5"
  }
}
```

On the next render, the renderer knows:

- `pool_size` was last set to `20` from env `PGBOUNCER_CONFIG_POOL_SIZE`
- The template default is `10`
- If `PGBOUNCER_CONFIG_POOL_SIZE` is no longer set → **revert to `10`**
- If `PGBOUNCER_CONFIG_POOL_SIZE` is now `30` → **update to `30`**
- If `PGBOUNCER_CONFIG_POOL_SIZE` is still `20` → no-op

### 12.5 Three render policies (same as reconciler modes)

```yaml
files:
  - destination: /etc/pgbouncer/pgbouncer.ini
    format: ini
    render_policy: reconcile    # default: full env-driven, reversible

    template: |
      [pgbouncer]
      listen_port = 6432
      listen_addr = 0.0.0.0
      auth_type = md5
      pool_size = 10            # template default
      max_client_conn = 100     # template default

    fromEnv:
      prefix: PGBOUNCER_CONFIG_
      keyTransform: lower
      valueTransform: none
```

| Policy | First render | Env added | Env changed | Env removed |
| -------- | -------------- | ----------- | ------------- | ------------- |
| `frozen` | applies template + env | ignored | ignored | ignored |
| `reconcile` (default, replaces `replace_or_append`) | applies template + env | adds override | updates override | **removes override, reverts to template** |
| `append_only` (legacy) | applies template + env | adds override | updates override | **stays (no removal)** |

- **`frozen`**: For things that must never change after first
  boot (e.g., the initial cluster config, the first admin user).
  Same as the reconciler's `frozen` mode.
- **`reconcile`** (default, replaces `replace_or_append`): Full
  env-driven config. The env is the source of truth. Removing
  an env var reverts to the template default. **This is the
  recommended policy for almost all config files.**
- **`append_only`**: Legacy behavior for users who want the old
  "env only adds, never removes" semantics. Useful for
  one-time-only config additions that should never revert.

### 12.6 Concrete example: the user's scenario

The user has a file spec with `PGBOUNCER_CONFIG_*` prefix:

```yaml
files:
  - destination: /etc/pgbouncer/pgbouncer.ini
    format: ini
    render_policy: reconcile    # default

    template: |
      [pgbouncer]
      listen_addr = 0.0.0.0
      listen_port = 6432
      auth_type = md5
      pool_mode = transaction
      max_client_conn = 100
      default_pool_size = 20
      admin_users = postgres
      stats_users = postgres
      logfile = /var/log/pgbouncer/pgbouncer.log
      pidfile = /var/run/pgbouncer/pgbouncer.pid
      user = postgres

    fromEnv:
      prefix: PGBOUNCER_CONFIG_
      keyTransform: lower
      valueTransform: none
```

#### Day 1: user sets `PGBOUNCER_CONFIG_POOL_SIZE=30`

```
$ docker compose up -d

# ezx reads env: PGBOUNCER_CONFIG_POOL_SIZE=30
# ezx applies template + env override
# Rendered file:
[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
auth_type = md5
pool_mode = transaction
max_client_conn = 100
default_pool_size = 20
admin_users = postgres
stats_users = postgres
logfile = /var/log/pgbouncer/pgbouncer.log
pidfile = /var/run/pgbouncer/pgbouncer.pid
user = postgres
pool_size = 30              # ← from PGBOUNCER_CONFIG_POOL_SIZE

# State file:
{
  "applied": {
    "PGBOUNCER_CONFIG_POOL_SIZE": "30"
  },
  "env_to_key": {
    "PGBOUNCER_CONFIG_POOL_SIZE": "pool_size"
  },
  "template_defaults": {
    "pool_size": "(not in template — would be removed if env unset)"
  }
}
```

#### Day 2: user removes `PGBOUNCER_CONFIG_POOL_SIZE`

```
$ docker compose up -d    # (env file no longer has PGBOUNCER_CONFIG_POOL_SIZE)

# ezx reads env: PGBOUNCER_CONFIG_POOL_SIZE is unset
# ezx diffs against state file:
#   - PGBOUNCER_CONFIG_POOL_SIZE was set to "30", now unset → REMOVE
# Rendered file:
[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
auth_type = md5
pool_mode = transaction
max_client_conn = 100
default_pool_size = 20
admin_users = postgres
stats_users = postgres
logfile = /var/log/pgbouncer/pgbouncer.log
pidfile = /var/run/pgbouncer/pgbouncer.pid
user = postgres
# ← pool_size line removed

# State file (updated):
{
  "applied": {},
  "env_to_key": {...}
}
```

The env var removal **reverts the config** — pgbouncer falls
back to its compiled-in default for `pool_size` (which is 20
in pgbouncer 1.23+, matching the template).

#### Day 3: user sets `PGBOUNCER_CONFIG_POOL_SIZE=50`

```
$ docker compose up -d

# ezx reads env: PGBOUNCER_CONFIG_POOL_SIZE=50
# ezx diffs against state file:
#   - PGBOUNCER_CONFIG_POOL_SIZE is now set, wasn't before → ADD
# Rendered file:
[pgbouncer]
...
pool_size = 50              # ← from new PGBOUNCER_CONFIG_POOL_SIZE
```

### 12.7 What about template defaults that should always be there?

Some keys in the template **must always be present** in the
file, regardless of env vars. For example, `listen_port = 6432`
should never be removed just because the user didn't set it via
env. The template provides the default.

The renderer distinguishes between:

- **Template-owned keys**: present in the template, never removed
  (e.g., `listen_port`, `listen_addr`, `auth_type`)
- **Env-owned keys**: present only because of an env var, removed
  when the env var is unset (e.g., `pool_size` from
  `PGBOUNCER_CONFIG_POOL_SIZE`)

The state file records both:

```json
{
  "template_owned": ["listen_port", "listen_addr", "auth_type", ...],
  "env_owned": {
    "PGBOUNCER_CONFIG_POOL_SIZE": "pool_size",
    "PGBOUNCER_CONFIG_MAX_CLIENT_CONN": "max_client_conn"
  }
}
```

On re-render:

- Template-owned keys: always present, updated only if template
  changes
- Env-owned keys: present if env var is set, absent if not

### 12.8 The four wildcard patterns, reconciled

All four wildcard patterns from §2.2 (single, prefix, indexed,
section) get the same treatment. Each has a **provenance record**
in the state file.

#### 1. Single env var → one config setting

```yaml
POSTGRESQL_SHARED_BUFFERS: shared_buffers
```

State file:

```json
{
  "env_owned": {
    "POSTGRESQL_SHARED_BUFFERS": "shared_buffers"
  },
  "applied": {
    "POSTGRESQL_SHARED_BUFFERS": "256MB"
  }
}
```

If user removes `POSTGRESQL_SHARED_BUFFERS` → `shared_buffers = 256MB`
is removed from `postgresql.conf`. Postgres falls back to its
compiled-in default.

#### 2. Prefix wildcard

```yaml
prefix: POSTGRESQL_CONFIG_
keyTransform: lower
```

State file:

```json
{
  "env_owned_prefix": "POSTGRESQL_CONFIG_",
  "applied": {
    "POSTGRESQL_CONFIG_SHARED_BUFFERS": "256MB",
    "POSTGRESQL_CONFIG_MAX_CONNECTIONS": "200"
  }
}
```

If user removes `POSTGRESQL_CONFIG_MAX_CONNECTIONS` →
`max_connections = 200` is removed. Other env-owned keys stay.

#### 3. Indexed prefix (PG_HBA_ADD_*)

This is the **trickiest case** because the order matters and
new indices can be added/removed dynamically. The current
design already uses **managed block markers**:

```yaml
managedBlock:
  marker: "# >>> ezx:pg_hba_add >>>"
  endMarker: "# <<< ezx:pg_hba_add <<<"
  onStart: remove
```

The renderer already strips the old block and rewrites it from
scratch on every render. With reconciliation:

- State file records which env vars were applied: `PG_HBA_ADD_1`, `PG_HBA_ADD_2`, ...
- On re-render, read the current env, generate the block
- **Indices can be added/removed freely**: the block is rewritten
  from the current env, not incrementally

This is already how the design works. The state file just makes
it explicit and reversible.

#### 4. Section prefix (PGBOUNCER_CONFIG_*)

Same as prefix wildcard, but applied to a specific section of
an INI-style file. Same provenance tracking, same removal
behavior.

### 12.9 Force escape hatches

Same as the reconciler:

| Env var | Effect |
| --------- | -------- |
| `EZX_FORCE_RENDER=true` | Re-render every file, ignoring the state file cache. Useful when the state file is missing or corrupted. |
| `EZX_DRY_RUN=true` | Don't write any files. Print the diff for every file. The standard "show me what you'd do" flag. |

`ezx validate` reports the policy of every file at load time:

```
✓ /etc/pgbouncer/pgbouncer.ini: render_policy=reconcile, 3 env-owned keys
✓ /etc/postgresql/postgresql.conf: render_policy=reconcile, 5 env-owned keys
⚠ /etc/postgresql/pg_hba.conf: render_policy=append_only (legacy), 0 env-owned keys
  Hint: use render_policy=reconcile for full env-driven config.
```

### 12.10 The new `render_policy` field replaces `policy`

The current `policy: replace_or_append | append_only | upsert`
is replaced by `render_policy: frozen | reconcile | append_only`:

| Old `policy` | New `render_policy` | Notes |
| ------------- | --------------------- | ------- |
| `replace_or_append` (default) | `reconcile` (default) | New behavior: also handles removals |
| `append_only` | `append_only` | Same: never remove |
| `upsert` | `reconcile` | New behavior: also handles removals |
| — (new) | `frozen` | For things that should never change after first render |

The old `policy` field is **deprecated** but still accepted in
v1.1 for backward compatibility, with a deprecation warning
at load time. In v2.0, it's removed entirely.

### 12.11 What about manually edited files?

If the user mounts their own `pgbouncer.ini` and edits it
manually, what happens?

**Default behavior** (`render_policy: reconcile`):

- For keys ezx **knows about** (via explicit mappings or
  wildcards): ezx owns them, can replace/remove
- For keys ezx **doesn't know about**: ezx leaves them alone

So a user who adds a custom `application_name_additional =
myapp` to pgbouncer.ini by hand will see it preserved across
restarts, because ezx doesn't know that key.

If the user wants strict mode (reject any key not in the
schema), they can use the `render_policy: frozen` mode or
the global `strict_schema: true` setting.

### 12.12 What this kills from bash

Today's `container-scripts` has a **partial** version of this:

- `startup.sh` has `sed -i` loops that add/update keys
- `.pg_hba_env_entries` sidecar file tracks what was written
- But there's no concept of "remove on env unset"

The user has to manually `docker exec ... sed -i` to remove
a key. This is exactly the bug the user's question identified.

With the reconciliation model, **removing an env var is a
first-class operation** that the user can rely on. The env
becomes a true **declarative interface** to the config file.

### 12.13 v1 scope

| Feature | v1 | v1.1 | v2.0 |
| --------- | --- | ------ | ------ |
| `append_only` (current behavior) | ✅ | ✅ | ✅ |
| `replace_or_append` → `reconcile` | ❌ | ✅ (default) | ✅ |
| State file at `${PGDATA}/.ezx/rendered/` | ❌ | ✅ | ✅ |
| Diff algorithm (add/update/remove) | ❌ | ✅ | ✅ |
| Template-owned vs. env-owned tracking | ❌ | ✅ | ✅ |
| `render_policy: frozen` | ❌ | ✅ | ✅ |
| `EZX_FORCE_RENDER`, `EZX_DRY_RUN` | ❌ | ✅ | ✅ |
| Backward-compat with old `policy` field | ❌ | ✅ (with deprecation warning) | ❌ (removed) |

v1 ships with the **current behavior** (the gap exists but is
documented). v1.1 ships the full reconciliation model. v2.0
removes the legacy `policy` field.

This is a **v1.1 milestone**, not v1, because it requires
significant changes to the renderer and the state file format.
The migration path is: users on `policy: replace_or_append`
get a deprecation warning in v1.1, and the behavior changes to
`reconcile` (full diff including removals) automatically.

## 13. Conflict detection: catching human error at load time

The current design has **no safety mechanism** for env var
configuration errors. A user can write:

```yaml
runtime:
  files:
    - destination: /etc/pgbouncer/pgbouncer.ini
      format: ini
      fromEnv:
        prefix: PGBOUNCER_CONFIG_
        keyTransform: lower
        valueTransform: none
    - destination: /etc/pgbouncer/userlist.txt
      format: text
      fromEnv:
        prefix: PGBOUNCER_CONFIG_      # ← same prefix as above
        keyTransform: none
        valueTransform: none
```

…and the renderer would silently do whatever its rules say
(first match wins, or some other implicit precedence) without
warning the user. This is exactly the kind of **silent
misconfiguration** that the design is supposed to prevent.

This section adds **conflict detection** as a first-class
feature of the loader, with three severity levels and clear,
actionable error messages.

### 13.1 The four classes of configuration errors

The loader detects four classes of errors:

#### 1. Conflicts (errors — refuse to start)

The same env var is **claimed by multiple destinations** with
no clear precedence.

```yaml
files:
  - destination: /etc/pgbouncer/pgbouncer.ini
    fromEnv:
      prefix: PGBOUNCER_CONFIG_
      keyTransform: lower
  - destination: /etc/pgbouncer/userlist.txt
    fromEnv:
      prefix: PGBOUNCER_CONFIG_      # ← conflict!
      keyTransform: none
```

```
✗ CONFLICT: env var PGBOUNCER_CONFIG_POOL_SIZE is claimed by
  multiple file specs:
    - /etc/pgbouncer/pgbouncer.ini → pool_size (keyTransform: lower)
    - /etc/pgbouncer/userlist.txt → PGBOUNCER_CONFIG_POOL_SIZE (keyTransform: none)
  Resolution: use different prefixes (e.g., PGBOUNCER_INI_* and
  PGBOUNCER_USERS_*), or merge the two file specs into one.
```

#### 2. Collisions (errors — refuse to start)

In the **same file**, multiple env vars map to the **same key**.

```yaml
files:
  - destination: /etc/pgbouncer/pgbouncer.ini
    fromEnv:
      mappings:
        PGBOUNCER_POOL_SIZE: pool_size       # explicit
      prefix: PGBOUNCER_                     # wildcard (also matches)
```

```
✗ COLLISION: multiple env vars map to the same key "pool_size"
  in /etc/pgbouncer/pgbouncer.ini:
    - PGBOUNCER_POOL_SIZE (explicit mapping)
    - PGBOUNCER_CONFIG_POOL_SIZE (wildcard PGBOUNCER_)
  Resolution: rename one of the env vars, or remove the wildcard.
```

#### 3. Ambiguities (warnings — proceed but warn)

An env var matches **both** an explicit mapping AND a wildcard,
or matches multiple rules with the same resolution (redundant
but not conflicting).

```yaml
files:
  - destination: /etc/postgresql/postgresql.conf
    fromEnv:
      mappings:
        POSTGRESQL_SHARED_BUFFERS: shared_buffers    # explicit
      prefix: POSTGRESQL_                              # wildcard (also matches)
```

```
⚠ AMBIGUITY: env var POSTGRESQL_SHARED_BUFFERS matches both:
    - explicit mapping → shared_buffers
    - wildcard POSTGRESQL_ → shared_buffers (keyTransform: lower)
  Resolution: explicit wins, but the wildcard match is redundant.
  Consider removing the explicit mapping.
```

#### 4. Declarations (warnings — proceed but warn)

An env var is **declared in `envSchema` but not used**, or
**used in a file spec but not declared in `envSchema`**.

```yaml
envSchema:
  - name: PGBOUNCER_CONFIG_POOL_SIZE      # declared
    type: int
    default: 20

runtime:
  files:
    - destination: /etc/pgbouncer/pgbouncer.ini
      fromEnv:
        prefix: PGBOUNCER_OTHER_           # different prefix
```

```
⚠ UNUSED: env var PGBOUNCER_CONFIG_POOL_SIZE is declared in
  envSchema but not used in any file spec or plugin config.
  Resolution: remove the declaration, or add a file spec that uses it.
```

```yaml
envSchema: []    # empty

runtime:
  files:
    - destination: /etc/pgbouncer/pgbouncer.ini
      fromEnv:
        prefix: PGBOUNCER_CONFIG_        # not declared
```

```
⚠ UNDECLARED: env var PGBOUNCER_CONFIG_POOL_SIZE is used in
  file spec but not declared in envSchema. It will be accepted
  at runtime but not validated, logged, or documented.
  Resolution: add the env var to envSchema with type and default.
```

### 13.2 The detection algorithm

The loader builds a complete map of `env_var → [(file, key)]`
across all file specs, then checks for the four classes of
errors:

```go
// internal/loader/conflict.go
type EnvMapping struct {
    EnvVar string
    File   string
    Key    string
    Source string  // "explicit" | "wildcard" | "indexed"
}

func (s *Service) DetectConflicts(config *domain.Config, env Env) []ValidationIssue {
    var issues []ValidationIssue

    // 1. Build the env → [(file, key)] map
    envMap := make(map[string][]EnvMapping)
    for _, file := range config.Runtime.Files {
        for _, mapping := range s.resolveFileMappings(file, env) {
            envMap[mapping.EnvVar] = append(envMap[mapping.EnvVar], mapping)
        }
    }

    // 2. Check for conflicts (same env var, different files)
    for envVar, mappings := range envMap {
        files := make(map[string]bool)
        for _, m := range mappings {
            files[m.File] = true
        }
        if len(files) > 1 {
            issues = append(issues, ValidationIssue{
                Severity: IssueSeverityError,
                Code:     "conflict",
                EnvVar:   envVar,
                Message:  fmt.Sprintf("env var %s is claimed by multiple file specs", envVar),
            })
        }
    }

    // 3. Check for collisions (same env var, same file, different keys)
    for envVar, mappings := range envMap {
        byFile := make(map[string]map[string]bool)  // file → set of keys
        for _, m := range mappings {
            if byFile[m.File] == nil {
                byFile[m.File] = make(map[string]bool)
            }
            byFile[m.File][m.Key] = true
        }
        for file, keys := range byFile {
            if len(keys) > 1 {
                issues = append(issues, ValidationIssue{
                    Severity: IssueSeverityError,
                    Code:     "collision",
                    EnvVar:   envVar,
                    Message:  fmt.Sprintf("env var %s maps to multiple keys in %s", envVar, file),
                })
            }
        }
    }

    // 4. Check for ambiguities (same env var, same file, same key, different sources)
    for envVar, mappings := range envMap {
        byFileAndKey := make(map[string][]string)  // "file|key" → list of sources
        for _, m := range mappings {
            k := m.File + "|" + m.Key
            byFileAndKey[k] = append(byFileAndKey[k], m.Source)
        }
        for fk, sources := range byFileAndKey {
            if len(sources) > 1 {
                issues = append(issues, ValidationIssue{
                    Severity: IssueSeverityWarning,
                    Code:     "ambiguity",
                    EnvVar:   envVar,
                    Message:  fmt.Sprintf("env var %s matches multiple rules in %s", envVar, fk),
                })
            }
        }
    }

    // 5. Check for unused/undeclared env vars
    declared := make(map[string]bool)
    for _, e := range config.EnvSchema {
        declared[e.Name] = true
    }
    used := make(map[string]bool)
    for envVar := range envMap {
        used[envVar] = true
    }
    for envVar := range declared {
        if !used[envVar] {
            issues = append(issues, ValidationIssue{
                Severity: IssueSeverityWarning,
                Code:     "unused",
                EnvVar:   envVar,
                Message:  fmt.Sprintf("env var %s is declared but not used", envVar),
            })
        }
    }
    for envVar := range used {
        if !declared[envVar] {
            issues = append(issues, ValidationIssue{
                Severity: IssueSeverityWarning,
                Code:     "undeclared",
                EnvVar:   envVar,
                Message:  fmt.Sprintf("env var %s is used but not declared", envVar),
            })
        }
    }

    return issues
}
```

### 13.3 The `ezx validate` output

The `ezx validate` subcommand runs the full detection and
produces a structured report:

```bash
$ ezx validate --config ezx.runtime.yaml --env-file .env

# Validation results:
✓ Built-in schema: 23 env vars declared
✓ Plugin schemas merged: 3 plugins, 5 env vars added
✓ File specs: 3 files, 15 env-to-key mappings

# Errors (refuse to start):
✗ CONFLICT: env var PGBOUNCER_CONFIG_POOL_SIZE is claimed by
  multiple file specs:
    - /etc/pgbouncer/pgbouncer.ini → pool_size (keyTransform: lower)
    - /etc/pgbouncer/userlist.txt → PGBOUNCER_CONFIG_POOL_SIZE (keyTransform: none)
  Resolution: use different prefixes, or merge the file specs.

# Warnings (proceed but warn):
⚠ AMBIGUITY: env var POSTGRESQL_SHARED_BUFFERS matches both
  explicit mapping and wildcard POSTGRESQL_ in postgresql.conf.
⚠ UNDECLARED: env var CUSTOM_FOO is used in file spec but not
  in envSchema.

# Summary:
1 error, 2 warnings, 0 info
Validation FAILED. Fix the error above before starting.
```

The `ezx runtime` command runs the same validation before
starting, and fails fast if there are errors.

### 13.4 Severity levels and strict mode

| Severity | Default behavior | With `strict_schema: true` |
| ---------- | ------------------ | ---------------------------- |
| **error** (conflict, collision) | Refuse to start | Refuse to start |
| **warning** (ambiguity, unused, undeclared) | Log warning, proceed | Refuse to start |
| **info** (redundancy) | Log at debug level | Log warning |

The `strict_schema: true` setting is a global opt-in for
maximum safety:

```yaml
apiVersion: ezx/v1
kind: Bootstrapper
metadata:
  name: postgresql-stack

spec:
  strict_schema: true   # warnings are also fatal

  envSchema: ...
  files: ...
```

### 13.5 Plugin-aware conflict detection (v1.1+)

The detection is **plugin-aware**: if a plugin declares a
schema extension that owns certain env vars, the loader
knows not to flag those env vars as "undeclared" even if
they're not in ezx's built-in `envSchema`.

```
# Plugin: redis-middleware owns "middleware.redis.*"
⚠ UNDECLARED: env var middleware.redis.enabled is used in
  file spec but not in envSchema.
  Hint: this env var is owned by the plugin "redis-middleware"
  (declared via schema_extension). The warning can be ignored
  if the plugin is installed and loaded.
```

The detection algorithm becomes:

1. Build the merged schema (built-in + all plugin extensions)
2. For each env var used in a file spec, check if it's in the
   merged schema (built-in OR plugin-owned)
3. If yes → no warning
4. If no → warning (with hint about which plugin might own it)

This requires the plugin's schema extensions to be available
at load time, which means plugins must be loaded **before**
the conflict detection runs. This is already the case in the
v1.1 design (plugins are loaded in `OnLoad` phase).

### 13.6 What about runtime-added env vars?

Some env vars are added at runtime by Docker, Kubernetes, or
ezx itself (e.g., `HOSTNAME`, `PATH`, `HOME`, `EZX_STAGE`).
These should **not** trigger conflict warnings.

The detection algorithm only checks env vars that are:

- Declared in `envSchema` (or plugin schema extensions)
- Used in a file spec
- Used in a plugin config

Anything else is ignored. This prevents false positives from
runtime-injected env vars.

### 13.7 v1 scope

| Feature | v1 | v1.1 | v2.0 |
| --------- | --- | ------ | ------ |
| Basic env-to-key map building | ✅ | ✅ | ✅ |
| Conflict detection (same env var, multiple files) | ✅ | ✅ | ✅ |
| Collision detection (same env var, same file, different keys) | ✅ | ✅ | ✅ |
| Ambiguity detection (explicit + wildcard) | ❌ | ✅ | ✅ |
| Unused/undeclared detection | ❌ | ✅ | ✅ |
| Plugin-aware detection | ❌ | ✅ | ✅ |
| Strict mode (`strict_schema: true`) | ❌ | ✅ | ✅ |
| Detailed `ezx validate` output | ✅ (basic) | ✅ (full) | ✅ (full) |

v1 ships with **error-level conflict detection only** — the
user's exact scenario (same prefix in two files) is caught at
`ezx validate` time and the runtime refuses to start. The
more nuanced warnings (ambiguities, unused, undeclared) and
plugin-aware detection are v1.1+ features.

This is a **v1.1 milestone** because it requires the plugin
system to be partially in place (for the plugin-aware
detection). The basic conflict detection (v1 scope) can ship
without plugins.

## 14. Security model

The current design is **silent on security**. This is a critical
gap. ezx runs as **PID 1** in containers, handles **secrets**,
spawns **untrusted plugin code**, and has **network access**
for downloads and plugin APIs. A security incident in ezx could
compromise every database managed by it, every secret in the
user's environment, and potentially the host system.

This section defines a comprehensive security model covering all
attack surfaces, with concrete mechanisms and v1 scope.

### 14.1 Security principles

ezx follows four core security principles, in order of priority:

1. **Defense in depth** — no single mechanism is the only
   defense. Multiple layers must fail for an attack to succeed.
2. **Least privilege** — every process, plugin, and file
   operation gets the minimum permissions needed to function.
3. **Fail secure** — when something goes wrong, ezx refuses to
   start, not starts in a degraded/insecure state.
4. **Secure by default** — security is on by default. Users
   must explicitly opt out, with a clear warning.

### 14.2 Threat model

ezx protects against these threats:

| Threat | Impact | Mitigation |
| -------- | -------- | ------------ |
| **Malicious plugin** | Steals secrets, exfiltrates data, compromises host | Plugin signing, sandboxing, checksum verification, capability restrictions |
| **Compromised marketplace** | Pushes malicious plugin to existing users | Signed marketplace index (cosign), pinned checksums in user config |
| **Compromised upstream source** | Malicious code in postgresql, pgbouncer, etc. | Checksum verification, GPG signature verification, version pinning |
| **Secret leakage** | Passwords visible in `/proc`, logs, `docker inspect` | Secret redaction in logs, file-based secrets (`/run/secrets/`), optional secret zeroing |
| **Container escape** | ezx compromised → host compromised | No privileged mode, capability dropping, seccomp, AppArmor, user namespaces |
| **SQL injection in reconciler** | Reconciler actions run arbitrary SQL | Parameterized queries only, no string concatenation |
| **Path traversal in file rendering** | Plugin writes outside intended directory | Destination path validation, no `..` escapes |
| **YAML deserialization attack** | Malicious YAML executes code | Use `yaml.v3` (safe decoder), no `yaml.Unmarshal` with arbitrary types |
| **Signal handling bugs (PID 1)** | Child processes become zombies, container hangs | Explicit signal forwarding, child reaping |
| **Supply chain attack on dependencies** | Malicious code in a Go module ezx imports | `go.sum` verification, minimal dependency tree, reproducible builds |
| **Privilege escalation** | Plugin or child process gains root | `no_new_privs`, capability dropping, run as non-root |
| **DoS via resource exhaustion** | Plugin or child process consumes all memory/CPU | cgroups, rlimits, process supervision with restart limits |

### 14.3 Process security

ezx runs as **PID 1** in the container, which has special
semantics (signals aren't auto-forwarded, zombie processes
aren't auto-reaped). This is a major source of bugs and
security issues.

#### PID 1 responsibilities

```
1. Handle SIGTERM, SIGINT, SIGHUP from Docker/Kubernetes
2. Forward signals to child processes (postgresql, pgbouncer, plugins)
3. Reap zombie children (call wait() in a loop)
4. Run shutdown hooks in reverse order
5. Exit with the correct code (0 = clean, non-zero = error)
```

The current `internal/repository/system/process.go` doesn't
handle these correctly (it calls `cmd.Wait()` which blocks
forever if the child is reparented). This needs to be fixed
with a proper signal manager.

#### Privilege dropping

ezx starts as root (required for `fork+exec` of postgresql,
filesystem permissions, etc.) but **must drop privileges**
before spawning database processes. The standard pattern:

```go
// In internal/repository/system/process.go
func (r *ProcessNodeRepository) Start(ctx context.Context) (*exec.Cmd, error) {
    // 1. Start as root (current uid)
    cmd := exec.CommandContext(ctx, r.Node.Process.BinaryPath, r.Node.Process.Arguments...)

    // 2. Set uid/gid from the process spec
    if r.Node.Process.User != "" {
        uid, gid := lookupUser(r.Node.Process.User)
        cmd.SysProcAttr.Credential = &syscall.Credential{
            Uid: uint32(uid),
            Gid: uint32(gid),
        }
    }

    // 3. Drop capabilities
    cmd.SysProcAttr.AmbientCaps = nil  // no ambient capabilities
    cmd.SysProcAttr.NoNewPrivs = true  // prevent SUID/SGID escalation

    return cmd, cmd.Start()
}
```

#### no_new_privs

Every child process should be started with `PR_SET_NO_NEW_PRIVS`
to prevent privilege escalation via SUID/SGID binaries:

```go
cmd.SysProcAttr.NoNewPrivs = true
```

This is a **one-way valve** — once set, it cannot be unset.
A child process cannot gain privileges it didn't have at start.

#### Capability dropping

Linux capabilities are a fine-grained alternative to root.
ezx should drop all capabilities by default and add only
what's needed:

```go
// Start with all capabilities dropped
cmd.SysProcAttr.Capability = &syscall.Capability{
    Permitted:   nil,  // no capabilities
    Effective:   nil,  // no capabilities
    Inheritable: nil,  // no capabilities
    Ambient:     nil,  // no ambient capabilities
}
```

For processes that need specific capabilities (e.g., postgresql
needs `CAP_SYS_NICE` for `nice` priority), declare them in
the YAML:

```yaml
runtime:
  processChain:
    roots:
      - name: postgresql
        process:
          binaryPath: /usr/local/pgsql/bin/postgres
          arguments: ["-D", "{{ .Env.PGDATA }}"]
          capabilities:
            - CAP_SYS_NICE         # for nice priority
            - CAP_NET_BIND_SERVICE  # for binding low ports (if needed)
```

ezx starts with `CAP_SETPCAP` to set the child's permitted
capabilities, then drops `CAP_SETPCap` before exec.

#### Resource limits (rlimits)

Every child process should have resource limits to prevent
DoS:

```go
cmd.SysProcAttr.Rlimit = []syscall.Rlimit{
    {Type: syscall.RLIMIT_CPU, Cur: uint64(60 * 60), Max: uint64(60 * 60)},  // 1 hour CPU
    {Type: syscall.RLIMIT_AS, Cur: uint64(4 * 1024 * 1024 * 1024), Max: uint64(4 * 1024 * 1024 * 1024)},  // 4GB address space
    {Type: syscall.RLIMIT_NOFILE, Cur: 1024, Max: 1024},  // 1024 open files
    {Type: syscall.RLIMIT_NPROC, Cur: 64, Max: 64},  // 64 processes
}
```

The actual values come from the process spec (with sensible
defaults from ezx).

### 14.4 Filesystem security

#### Read-only root filesystem

The container should run with a read-only root filesystem.
Only specific paths are writable:

| Path | Purpose | Permissions |
| ------ | --------- | ------------- |
| `${PGDATA}` | Postgres data directory | `0700`, owned by postgres |
| `/var/log/pgbouncer` | Pgbouncer logs | `0750`, owned by pgbouncer |
| `/var/run/pgbouncer` | Pgbouncer pidfile/socket | `0750`, owned by pgbouncer |
| `/run/ezx` | ezx runtime state (sockets, state files) | `0750`, owned by ezx |
| `/tmp` | Temporary files | `1777` (tmpfs) |

Everything else is read-only. This prevents:

- Attackers writing to `/etc/passwd`, `/etc/shadow`
- Attackers modifying the ezx binary
- Attackers planting backdoors in system directories

#### noexec on data directories

Directories that contain data but never executables should
be mounted `noexec`:

```
${PGDATA}      noexec,nodev,nosuid
/var/log       noexec,nodev,nosuid
/var/run       noexec,nodev,nosuid
/run/ezx       noexec,nodev,nosuid
```

This prevents attackers from dropping and executing binaries
in data directories.

#### Secure file permissions

Config files with secrets should be `0600`, owned by the
service user:

```
-rw-------  postgres  postgres  /etc/postgresql/postgresql.conf
-rw-------  postgres  postgres  /var/lib/postgresql/data/pg_hba.conf
-rw-------  pgbouncer pgbouncer /etc/pgbouncer/userlist.txt
-rw-------  pgbouncer pgbouncer /etc/pgbouncer/pgbouncer.ini
```

ezx's file renderer must set these permissions atomically:

```go
// internal/renderer/file.go
func WriteFile(path string, content []byte, mode os.FileMode, owner string) error {
    // 1. Write to temp file with secure permissions from the start
    tmp, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
    if err != nil { return err }
    defer os.Remove(tmp.Name())

    // 2. Set ownership BEFORE writing content (avoid TOCTOU)
    if owner != "" {
        uid, gid := lookupUser(owner)
        os.Chown(tmp.Name(), uid, gid)
    }

    // 3. Write content
    if _, err := tmp.Write(content); err != nil { return err }

    // 4. Atomic rename (replaces destination atomically)
    return os.Rename(tmp.Name(), path)
}
```

#### Atomic writes

File writes must be **atomic** — either the new file is fully
written, or the old file is unchanged. Use `rename(2)` which
is atomic on Linux:

```go
// 1. Write to temp file
tmpPath := path + ".tmp." + randomString(8)
os.WriteFile(tmpPath, content, mode)

// 2. Set ownership and permissions
os.Chown(tmpPath, uid, gid)
os.Chmod(tmpPath, mode)

// 3. Atomic rename (this is the critical part)
os.Rename(tmpPath, path)
```

This prevents:

- Partial writes (e.g., config file half-written, service can't read it)
- TOCTOU attacks (attacker swaps the file between write and chmod)

### 14.5 Secret management

#### The current design's `secret: true` is not enough

The current design has:

```yaml
envSchema:
  - name: POSTGRES_PASSWORD
    secret: true
    resolveFrom: [{ env: POSTGRES_PASSWORD }, { file: /run/secrets/postgresql/password }]
```

`secret: true` prevents the value from being **logged**, but
does nothing about:

- Value visible in `/proc/<pid>/environ`
- Value visible in `docker inspect`
- Value visible in core dumps
- Value stored in memory indefinitely

#### Secret resolution chain

Secrets should be resolved in this order (first hit wins):

1. **File** (`/run/secrets/<name>`) — Docker/K8s secret mount
2. **Environment variable** — `docker run -e` or `compose.yaml`
3. **External secret backend** (v1.1+) — Vault, AWS Secrets Manager, etc.

Files are preferred over env vars because:

- Files can have `0400` permissions (env vars are world-readable via `/proc`)
- Files don't appear in `docker inspect` output
- Files can be mounted from tmpfs (in-memory, never on disk)
- Files are easier to rotate (just write a new file)

#### Secret redaction in logs

Every log line that mentions a secret env var must redact the
value:

```go
// internal/logger/secret.go
type SecretRedactor struct {
    secretNames map[string]bool
}

func (r *SecretRedactor) Redact(line string) string {
    for name := range r.secretNames {
        // Redact "POSTGRES_PASSWORD=foo" → "POSTGRES_PASSWORD=[REDACTED]"
        line = regexp.MustCompile(name+"=[^\\s]+").ReplaceAllString(line, name+"=[REDACTED]")
        // Redact JSON: "password": "foo" → "password": "[REDACTED]"
        line = regexp.MustCompile(`"(password|secret|key|token)":\s*"[^"]+"`).ReplaceAllString(line, `"$1": "[REDACTED]"`)
    }
    return line
}
```

The redactor is registered at startup with all env vars marked
`secret: true`. Every log line passes through it.

#### Memory zeroing (best effort)

Go does not provide guaranteed memory zeroing, but we can do
our best:

```go
// After using a secret, zero the memory
func useSecret(secret []byte) {
    defer func() {
        for i := range secret {
            secret[i] = 0
        }
    }()
    // ... use the secret ...
}
```

This is **best effort** — the Go runtime may have copied the
secret elsewhere (e.g., during garbage collection). The real
defense is to avoid keeping secrets in memory longer than
necessary.

#### Secret rotation (v1.1+)

For v1, secrets are read once at startup. To change a secret,
the user must restart the container.

For v1.1+, ezx should support **hot rotation**:

- Watch `/run/secrets/<name>` for changes (inotify)
- Re-resolve on change
- Apply via the reconciler (e.g., `ALTER ROLE ... WITH PASSWORD`)

This is a v1.1+ feature.

### 14.6 Network security

#### Bind to localhost only

Services like pgbouncer admin console, patroni REST API, and
plugin APIs should bind to **localhost only** by default:

```yaml
runtime:
  processChain:
    roots:
      - name: pgbouncer
        process:
          binaryPath: /usr/local/pgbouncer/bin/pgbouncer
        network:
          listen_addr: 127.0.0.1      # localhost only
          admin_addr: 127.0.0.1        # admin console on localhost
          stats_addr: ""                # stats disabled
```

The user must explicitly opt in to public binding:

```yaml
network:
  listen_addr: 0.0.0.0    # explicit opt-in to public binding
  admin_addr: 0.0.0.0     # explicit opt-in
```

#### TLS for plugin gRPC

Plugin communication over gRPC should use **TLS by default**:

```go
// internal/repository/plugin/sidecar/client.go
creds, err := credentials.NewServerTLSFromFile(certFile, keyFile)
if err != nil { return nil, err }

server := grpc.NewServer(grpc.Creds(creds))
```

The cert/key is generated by ezx at startup and stored in
`/run/ezx/plugin-cert.pem` with `0600` permissions.

For mTLS (mutual TLS, where the plugin also presents a cert):

```go
// Plugin also presents a client cert signed by ezx's CA
clientCert, err := tls.LoadX509KeyPair(pluginCert, pluginKey)
creds := credentials.NewTLS(&tls.Config{
    Certificates: []tls.Certificate{serverCert},
    ClientCAs:    caCertPool,
    ClientAuth:   tls.RequireAndVerifyClientCert,
})
```

This is the **default in v1.1+**. v1 ships with TLS but
without mTLS (plugins present a cert but ezx doesn't verify it).

#### mTLS for marketplace

Plugin downloads from the marketplace should use **HTTPS with
certificate verification**:

```go
httpClient := &http.Client{
    Transport: &http.Transport{
        TLSClientConfig: &tls.Config{
            MinVersion: tls.VersionTLS12,
            // Use system CA bundle, verify chain
        },
    },
}
```

Plus, the plugin binaries should be **signed with cosign**:

```bash
# Vendor signs the plugin
$ cosign sign-blob --key cosign.key pgvector-extension-1.0.0 > pgvector-extension-1.0.0.sig

# ezx verifies the signature before loading
$ ezx plugin verify pgvector-extension-1.0.0 --signature pgvector-extension-1.0.0.sig --key cosign.pub
✓ signature valid
```

This is the **v2.0 supply chain story**. v1 relies on
checksum verification only.

### 14.7 Plugin security

The plugin system (§11) is the **largest attack surface** in
ezx. A malicious plugin can:

- Read all env vars (including secrets)
- Spawn child processes
- Open network connections
- Write to the filesystem
- Read other plugins' state

#### Plugin signing (v2.0)

Every plugin must be **signed by a trusted key**. ezx
maintains a trust store:

```yaml
# /etc/ezx/trusted-keys.yaml
trusted_keys:
  - key_id: sha256:abc123...
    public_key: |
      -----BEGIN PUBLIC KEY-----
      ...
    source: marketplace      # marketplace root key
  - key_id: sha256:def456...
    public_key: |
      -----BEGIN PUBLIC KEY-----
      ...
    source: vendor           # vendor's signing key
```

On load, ezx verifies the plugin's cosign signature against
the trust store. Unsigned plugins are rejected.

For v1, ezx relies on **checksum verification** (the user
pins the expected checksum in their config). Signed plugins
are a v2.0 feature.

#### Plugin sandboxing (v1.1+)

Plugin processes should run with **restricted capabilities**:

```go
// Spawn the plugin with minimal capabilities
cmd.SysProcAttr.Credential = &syscall.Credential{
    Uid: uint32(pluginUID),  // dedicated plugin user
    Gid: uint32(pluginGID),
}
cmd.SysProcAttr.Capability = &syscall.Capability{
    Permitted: []string{"CAP_NET_BIND_SERVICE"},  // only if plugin needs it
    Effective: []string{"CAP_NET_BIND_SERVICE"},
    Ambient:   nil,  // no ambient capabilities
}
cmd.SysProcAttr.NoNewPrivs = true
```

And with a **seccomp profile** that restricts syscalls:

```json
// /etc/ezx/seccomp/plugin.json
{
  "defaultAction": "SCMP_ACT_ERRNO",
  "syscalls": [
    {"names": ["read", "write", "close", "exit", "exit_group"], "action": "SCMP_ACT_ALLOW"},
    {"names": ["open", "openat"], "action": "SCMP_ACT_ALLOW", "args": [{"index": 1, "value": 0, "op": "SCMP_CMP_MASKED_EQ"}]},
    {"names": ["socket", "connect"], "action": "SCMP_ACT_ALLOW"},
    {"names": ["clone", "fork", "vfork"], "action": "SCMP_ACT_ALLOW"}
  ]
}
```

ezx loads the seccomp profile with `prctl(PR_SET_SECCOMP)`
before exec. This is a v1.1+ feature.

#### Plugin capabilities (v1.1+)

A plugin must **declare what it needs**:

```yaml
# Plugin manifest (returned by --ezx-describe)
{
  "name": "prometheus-exporter",
  "version": "0.5.0",
  "capabilities": {
    "needs_network": true,
    "needs_filesystem_write": ["/var/log/prometheus"],
    "needs_capabilities": [],
    "needs_env_vars": ["PROMETHEUS_PORT", "PROMETHEUS_NAMESPACE"]
  }
}
```

ezx checks the plugin's declared needs against the granted
capabilities at load time. A plugin that needs network but
wasn't granted network is rejected.

#### Plugin isolation

Each plugin process should be in its **own PID namespace** (so
it can't see other processes), **own network namespace** (so
it can only use explicitly granted network), and **own mount
namespace** (so it only sees explicitly mounted filesystems).

For v1, this is **not implemented** (plugins share the host's
namespaces). For v1.1+, this is the default.

### 14.8 Setup phase security

The setup phase runs in the **Dockerfile build stage**, which
is a different container from the runtime. This is a major
security win — the build container is destroyed after the
image is built, so any compromise is bounded.

#### Source verification

Every downloaded source must be **checksum-verified**:

```yaml
setup:
  steps:
    - name: postgresql
      source:
        type: autotools
        url: https://ftp.postgresql.org/pub/source/v16.4/postgresql-16.4.tar.gz
        checksum:
          type: sha256
          value: "24c45dd0..."
```

ezx verifies the checksum **before** extracting. A mismatch
is a fatal error.

For v1.1+, sources should also be **GPG signature-verified**:

```yaml
source:
  type: autotools
  url: https://ftp.postgresql.org/pub/source/v16.4/postgresql-16.4.tar.gz
  checksum: { type: sha256, value: "24c45dd0..." }
  signature:
    type: gpg
    url: https://ftp.postgresql.org/pub/source/v16.4/postgresql-16.4.tar.gz.sig
    key_id: "postgresql.org"
```

ezx downloads the signature, verifies it against the trusted
key, and rejects the source if verification fails.

#### No arbitrary script execution

The `source.type: script` escape hatch is a security risk.
Limit what scripts can do:

```yaml
- name: weird-thing
  source:
    type: script
    run: |
      # arbitrary shell
    # Optional: restrict what the script can access
    sandbox:
      network: false       # no network access
      filesystem: read-only # read-only filesystem (except /tmp)
      timeout: 300         # max 5 minutes
      memory: 1GB          # max 1GB memory
```

For v1, the sandbox is **not enforced** (the script runs with
full permissions). For v1.1+, the sandbox is the default.

### 14.9 Runtime security

#### SQL injection prevention

The reconciler runs SQL against the live database. **Never**
use string concatenation — always use parameterized queries:

```go
// WRONG: SQL injection vulnerability
query := fmt.Sprintf("ALTER ROLE %s WITH PASSWORD '%s'", role, password)
db.Exec(query)

// RIGHT: parameterized query
db.Exec("ALTER ROLE $1 WITH PASSWORD $2", role, password)
```

This is enforced by code review and by the reconciler's
interface (the reconciler takes a typed `ReconcileEntry`, not
a raw SQL string).

#### Path traversal prevention

The file renderer must validate destination paths to prevent
escape from intended directories:

```go
func validateDestination(path string, allowedRoots []string) error {
    abs, err := filepath.Abs(path)
    if err != nil { return err }

    for _, root := range allowedRoots {
        if strings.HasPrefix(abs, root) {
            return nil
        }
    }

    return fmt.Errorf("destination %s is outside allowed roots %v", path, allowedRoots)
}
```

The `allowedRoots` come from the process spec:

```yaml
runtime:
  files:
    - destination: /etc/pgbouncer/pgbouncer.ini    # allowed: /etc
    - destination: ../../../etc/passwd            # REJECTED: outside /etc
```

#### YAML deserialization safety

ezx uses `go.yaml.in/yaml/v3` (the modern YAML library for
Go). Unlike the older `gopkg.in/yaml.v2`, it does **not**
support custom unmarshalers that can execute arbitrary code.

Still, the loader should:

- Reject unknown fields by default (already designed in §13)
- Use `yaml.Decoder.KnownFields(true)` to enforce strict mode
- Limit YAML document size (prevent billion-laughs attack)

#### Template injection

The file renderer uses Go's `text/template`, which is safe by
default (it auto-escapes based on context). But if the user
uses `html/template` for any HTML output, they need to be
aware of the security implications.

For v1, only `text/template` is supported. HTML output is a
plugin concern (plugins can register custom renderers).

#### Symlink attack prevention

The file renderer must not follow symlinks when writing:

```go
// WRONG: follows symlinks, vulnerable to TOCTOU
os.WriteFile(path, content, mode)

// RIGHT: O_NOFOLLOW rejects if path is a symlink
fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_EXCL, mode)
```

### 14.10 Container security

ezx is designed to run in a **hardened container** with
minimal privileges.

#### User namespaces

The container should run as a **non-root user from the start**:

```dockerfile
# Add a non-root user for ezx
RUN useradd -r -u 1000 -d /var/lib/ezx -s /sbin/nologin ezx
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/ezx", "run", "--config", "/etc/ezx/ezx.yaml"]
```

ezx starts as user 1000, but needs root to:

- Switch to the postgres user (requires `CAP_SETUID`)
- Bind to privileged ports (< 1024) (requires `CAP_NET_BIND_SERVICE`)
- Mount filesystems (requires `CAP_SYS_ADMIN`)

The container is started with **only these capabilities**:

```bash
docker run \
  --cap-drop=ALL \
  --cap-add=SETUID \
  --cap-add=SETGID \
  --cap-add=NET_BIND_SERVICE \
  --security-opt=no-new-privileges:true \
  --read-only \
  --tmpfs=/tmp:rw,noexec,nosuid,size=100m \
  ezx-postgresql
```

#### Seccomp profiles

The container should run with a **seccomp profile** that
restricts syscalls to only what's needed:

```json
// /etc/ezx/seccomp/default.json
{
  "defaultAction": "SCMP_ACT_ERRNO",
  "syscalls": [
    {"names": ["read", "write", "close", "fstat", "mmap", "mprotect", "brk", "exit", "exit_group"], "action": "SCMP_ACT_ALLOW"},
    {"names": ["open", "openat"], "action": "SCMP_ACT_ALLOW", "args": [{"index": 1, "value": 0, "op": "SCMP_CMP_MASKED_EQ"}]},
    {"names": ["socket", "connect", "bind", "listen", "accept", "accept4"], "action": "SCMP_ACT_ALLOW"},
    {"names": ["clone", "fork", "vfork", "execve", "wait4"], "action": "SCMP_ACT_ALLOW"},
    {"names": ["setuid", "setgid", "setgroups"], "action": "SCMP_ACT_ALLOW"}
  ]
}
```

```bash
docker run --security-opt seccomp=/etc/ezx/seccomp/default.json ezx-postgresql
```

#### AppArmor profiles

AppArmor provides **mandatory access control** on top of
Linux DAC. An AppArmor profile for ezx:

```
#include <tunables/global>

/usr/local/bin/ezx {
  #include <abstractions/base>
  #include <abstractions/nameservice>

  # Allow reading config
  /etc/ezx/** r,
  /var/lib/ezx/** rwk,

  # Allow writing data
  /var/lib/postgresql/** rwk,
  /var/log/pgbouncer/** rwk,
  /run/ezx/** rwk,

  # Allow network
  network inet stream,
  network inet6 stream,

  # Deny everything else
  deny /** w,
  deny /bin/** x,
  deny /usr/bin/** x,
  deny /usr/sbin/** x,
}
```

```bash
docker run --security-opt apparmor=ezx ezx-postgresql
```

### 14.11 Logging security

#### Secret redaction

As covered in §14.5, every log line passes through a secret
redactor. The redactor knows all env vars marked `secret: true`
and replaces their values with `[REDACTED]`.

#### Structured logging

ezx uses **structured logging** (key-value pairs), not
unstructured strings. This makes it easier to:

- Filter and search logs
- Redact specific fields
- Ship to log aggregation systems (Loki, Elasticsearch, etc.)

```go
logger.Info("process started",
    "name", node.Name,
    "pid", cmd.Process.Pid,
    "user", node.Process.User,
    // secrets are automatically redacted
)
```

#### Log levels

The user can control log verbosity:

```bash
ezx runtime --config ezx.yaml --log-level=info    # default
ezx runtime --config ezx.yaml --log-level=debug   # verbose
ezx runtime --config ezx.yaml --log-level=warn    # quiet
```

The default is `info`. `debug` may leak sensitive information
(useful for development, dangerous in production).

#### Log rotation

Logs are written to stdout/stderr (collected by Docker) and
optionally to a file:

```yaml
runtime:
  logging:
    file: /var/log/ezx/ezx.log
    max_size: 100MB
    max_files: 10
    compress: true
```

This prevents disk fill attacks (a malicious plugin can't
fill the disk by spamming logs).

### 14.12 Supply chain security

#### SBOM (Software Bill of Materials)

ezx generates an **SBOM** for the final image, listing every
component and its version:

```yaml
# /etc/ezx/sbom.yaml
apiVersion: ezx/v1
kind: SBOM
generated: 2025-01-15T00:00:00Z
image: postgresql-sandbox:0.1.0
components:
  - name: postgresql
    version: 16.4
    source: https://ftp.postgresql.org/pub/source/v16.4/postgresql-16.4.tar.gz
    checksum: sha256:24c45dd0...
    license: PostgreSQL
  - name: pgbouncer
    version: 1.23.1
    source: https://www.pgbouncer.org/downloads/files/1.23.1/pgbouncer-1.23.1.tar.gz
    checksum: sha256:f6c8a87...
    license: ISC
  - name: ezx
    version: 0.1.0
    source: https://github.com/supanadit/ezx
    license: MIT
```

The SBOM is signed with cosign for tamper-evidence.

#### Provenance

The build emits **SLSA-style provenance** — a signed
attestation of how the image was built:

```json
{
  "predicateType": "https://slsa.dev/provenance/v0.2",
  "predicate": {
    "builder": { "id": "https://github.com/supanadit/ezx" },
    "invocation": {
      "configSource": { "uri": "git+https://github.com/supanadit/ezx", "digest": { "sha1": "abc123..." } },
      "parameters": { "ezx_version": "0.1.0", "image": "postgresql-sandbox" }
    },
    "materials": [
      { "uri": "ezx.setup.yaml", "digest": { "sha256": "..." } },
      { "uri": "ezx.runtime.yaml", "digest": { "sha256": "..." } }
    ]
  }
}
```

This proves the image was built from a specific source, by
a specific builder, with specific inputs. Anyone can verify.

#### Vulnerability scanning

The SBOM can be scanned for known vulnerabilities using
tools like `grype`, `trivy`, or `osv-scanner`. The user runs:

```bash
$ ezx scan --sbom /etc/ezx/sbom.yaml
✓ postgresql 16.4: no known CVEs
⚠ pgbouncer 1.23.1: CVE-2024-1234 (low severity)
✗ openssl 1.1.1k: CVE-2024-5678 (high severity, fixed in 1.1.1l)
```

This is a v1.1+ feature. v1 ships the SBOM but not the
scanner.

### 14.13 Security defaults (what's on by default)

| Feature | v1 default | v1.1 default | v2.0 default |
| --------- | ------------- | --------------- | --------------- |
| Secret redaction in logs | ✅ on | ✅ on | ✅ on |
| Secret resolution from file (preferred over env) | ✅ on | ✅ on | ✅ on |
| Checksum verification (sources, plugins) | ✅ on | ✅ on | ✅ on |
| Plugin signing | ❌ off | ❌ off | ✅ on |
| TLS for plugin gRPC | ❌ off | ✅ on | ✅ on |
| mTLS for plugin gRPC | ❌ off | ⚠️ opt-in | ✅ on |
| Container capabilities dropped | ✅ on (--cap-drop=ALL) | ✅ on | ✅ on |
| no_new_privs | ✅ on | ✅ on | ✅ on |
| Read-only root filesystem | ⚠️ opt-in (recommended) | ✅ on | ✅ on |
| Seccomp profile | ⚠️ opt-in (recommended) | ✅ on | ✅ on |
| AppArmor profile | ❌ off | ⚠️ opt-in | ✅ on |
| Plugin sandboxing (seccomp, caps) | ❌ off | ✅ on | ✅ on |
| Secret rotation (hot) | ❌ off | ⚠️ opt-in | ✅ on |
| SBOM generation | ✅ on | ✅ on | ✅ on |
| Vulnerability scanning | ❌ off | ⚠️ opt-in | ✅ on |
| SLSA provenance | ❌ off | ❌ off | ✅ on |

### 14.14 What the user must do (security checklist)

The user (the ezx image maintainer) is responsible for:

- [ ] Run the container with `--cap-drop=ALL` and only add
      necessary capabilities
- [ ] Run with `--security-opt=no-new-privileges:true`
- [ ] Run with `--read-only` and explicit tmpfs mounts
- [ ] Run with a seccomp profile (the default one in
      `/etc/ezx/seccomp/default.json` is a good start)
- [ ] Mount secrets as files (`/run/secrets/<name>`), not
      environment variables
- [ ] Pin plugin checksums in `plugin_config` to prevent
      tampering
- [ ] Regularly run `ezx scan` to check for vulnerabilities
- [ ] Use a minimal base image (distroless, alpine, or
      debian-slim) to reduce attack surface
- [ ] Don't run as root in the Dockerfile (`USER 1000:1000`)
- [ ] Sign the image with cosign after building
- [ ] Distribute via a trusted registry with pull-time
      verification

### 14.15 v1 scope for security

| Security feature | v1 | v1.1+ |
| ------------------ | --- | ------ |
| Secret redaction in logs | ✅ | ✅ |
| Secret resolution from file | ✅ | ✅ |
| Checksum verification (sources, plugins) | ✅ | ✅ |
| Privilege dropping (uid/gid) | ✅ | ✅ |
| `no_new_privs` on child processes | ✅ | ✅ |
| Capability dropping (per-process) | ✅ | ✅ |
| rlimits on child processes | ✅ | ✅ |
| Read-only root filesystem support | ✅ (opt-in) | ✅ (default) |
| Atomic file writes (rename, not write+truncate) | ✅ | ✅ |
| YAML strict mode (reject unknown fields) | ✅ | ✅ |
| SQL parameterized queries in reconciler | ✅ | ✅ |
| Path traversal prevention in file renderer | ✅ | ✅ |
| Symlink attack prevention in file renderer | ✅ | ✅ |
| Secret zeroing (best effort) | ✅ (best effort) | ✅ (best effort) |
| Structured logging | ✅ | ✅ |
| Log redaction | ✅ | ✅ |
| SBOM generation | ✅ | ✅ |
| GPG signature verification for sources | ❌ | ✅ |
| TLS for plugin gRPC | ❌ | ✅ |
| mTLS for plugin gRPC | ❌ | ⚠️ opt-in |
| Plugin signing (cosign) | ❌ | ❌ |
| Plugin sandboxing (seccomp, caps) | ❌ | ✅ |
| Seccomp profile for ezx | ⚠️ opt-in | ✅ |
| AppArmor profile for ezx | ❌ | ⚠️ opt-in |
| Secret rotation (hot) | ❌ | ⚠️ opt-in |
| Vulnerability scanning (`ezx scan`) | ❌ | ⚠️ opt-in |
| SLSA provenance | ❌ | ❌ |

v1 ships with the **security fundamentals**: privilege
dropping, capability dropping, secret redaction, atomic
writes, SQL injection prevention, path traversal prevention.
The advanced features (plugin sandboxing, signing, GPG
verification) are v1.1+ and v2.0 milestones.

The v1 security story is **good enough for production** if
the user follows the security checklist (§14.14). The v1.1+
features add defense in depth.

## 15. Package management: mixed source/apt with transitive dependencies

The current design's §4.6 "Sources" provides a **low-level**
model: the user writes `setup.steps[]` explicitly, manually
ordering steps with `requires:` and manually specifying which
packages come from apt vs. which are built from source. This
works for simple cases but breaks down when:

- You build postgresql from source and need to know it
  transitively requires openssl, readline, zlib
- You want a custom curl version that isn't in apt
- You want to upgrade one library and need to know which
  packages depend on it
- You want the build stage to have a compiler but the final
  image to not

The phpv project solves this with a **declarative package
manager** that resolves transitive dependencies automatically.
ezx should offer the same, alongside the low-level model.

### 15.1 Two modes: low-level steps and high-level packages

ezx offers **two complementary models** for the setup phase:

| Model | When to use | Complexity | User writes |
| ------- | ------------- | ------------ | ------------ |
| **Low-level** (`setup.steps[]`) | Simple cases, full control, no transitive deps | Low | Every step explicitly |
| **High-level** (`setup.packages[]`) | Real-world stacks with transitive deps, mixed apt/source | Medium | Just the packages they want |

The user picks one (or mixes them). Both models produce the
same internal representation (a DAG of build steps) and the
same execution engine.

#### Low-level: `setup.steps[]` (existing, unchanged)

```yaml
setup:
  steps:
    - name: apt-base
      run: apt-get install -y build-essential libreadline-dev libssl-dev zlib1g-dev
    - name: postgresql
      requires: [apt-base]
      source: { type: autotools, url: "...", checksum: "..." }
    - name: cleanup
      requires: [postgresql]
      run: apt-get purge -y build-essential *-dev
```

The user writes every step, every dep, every cleanup. Full
control, but high maintenance.

#### High-level: `setup.packages[]` (new)

```yaml
setup:
  packages:
    - name: postgresql
      version: "16.4"
    - name: pgbouncer
      version: "1.23.1"
    - name: curl
      version: "8.10.0"
      source: build    # always build from source
```

ezx resolves the dependency graph, picks apt vs. source for
each dep, generates the build plan, and runs it. The user
just declares what they want.

### 15.2 The package manifest

For ezx to resolve dependencies, each package needs a
**manifest** that declares:

- Its name, version, source URL, checksum
- Its dependencies (other packages, with version constraints)
- Its build configuration
- Whether deps are build-time or runtime

Built-in manifests live in `internal/repository/memory/packages/`.
Users can add custom manifests in their `ezx.setup.yaml`.

```yaml
# internal/repository/memory/packages/postgresql.yaml
name: postgresql
type: source                    # source | apt
registry: postgresql            # which built-in registry resolves the URL
versions: ["16.4", "16.3", "16.2", "16.1", "16.0", "15.8", "15.7", ...]
default_version: "16.4"
dependencies:
  - name: openssl
    constraint: ">= 1.1.0"
    type: optional              # postgresql can build without openssl
  - name: readline
    constraint: ">= 6.0"
    type: required
  - name: zlib
    constraint: ">= 1.2.0"
    type: required
  - name: gcc
    constraint: ">= 4.0"
    type: build-time             # only needed during build
  - name: make
    constraint: ">= 3.0"
    type: build-time
build:
  configure: [./configure, "--prefix=/usr/local/pgsql", "--with-openssl", "--with-readline"]
  build:     [make, "-j{{ .BuildJobs }}", world]
  install:   [make, install-world]
provides:
  binaries: [psql, postgres, pg_ctl, initdb]
  headers:  [libpq-fe.h]
  libs:     [libpq.so]
```

For apt packages, the manifest is simpler:

```yaml
# internal/repository/memory/packages/libreadline-dev.yaml
name: libreadline-dev
type: apt
versions: ["8.2-1.3"]           # whatever apt has
provides:
  apt_package: libreadline-dev
  libs: [libreadline.so]
  headers: [readline.h]
```

### 15.3 The dependency resolver

The resolver takes the user's `setup.packages[]` list and
produces a **complete build plan** with all transitive
dependencies resolved.

```go
// assembler/service.go (new use case)
type PackageRequest struct {
    Name      string
    Version   string            // empty = use default
    Source    string            // auto | apt | build | system
}

type BuildPlan struct {
    Steps []BuildStep
}

type BuildStep struct {
    Name         string
    Type         StepType       // apt | source | cleanup
    Package      string         // apt package name or source name
    Version      string         // resolved version
    DependsOn    []string       // step names this depends on
    Satisfies    []string       // constraints this step satisfies
    Source       *SourceSpec    // for type=source
    Cleanup      *CleanupSpec   // for type=cleanup
}

func (s *Service) ResolvePackages(ctx context.Context, reqs []PackageRequest) (*BuildPlan, error) {
    // 1. Walk the dependency graph, collecting all required packages
    // 2. For each package, resolve version constraints
    // 3. For each dep, decide: apt or source build?
    // 4. Use the registry to get source URLs
    // 5. Return a BuildPlan
}
```

#### The resolution algorithm

```
1. Initialize the "needed" set with the user's requested packages
2. While needed is not empty:
   a. Pick a package from needed
   b. Look up its manifest (built-in or user-defined)
   c. If source=auto: decide based on availability
      - If the user's apt has a compatible version, use apt
      - Otherwise, build from source
   d. For each of its dependencies:
      - Add to needed if not already resolved
      - Record the constraint (e.g., "openssl >= 1.1.0")
3. Resolve version constraints:
   - For each package, find the highest version that satisfies
     all constraints
   - For apt packages, use whatever apt provides
   - For source packages, pick from the available versions
4. Detect conflicts:
   - If two packages require incompatible versions of the same
     dep, report a conflict
5. Topological sort:
   - Order the build steps so deps come first
6. Generate cleanup:
   - For each build-time dep, add a cleanup step at the end
7. Return the BuildPlan
```

#### The "auto" source decision

For each package, `source: auto` means "ezx decides". The
decision tree:

```
Is the package in apt?
  └── Yes → use apt (system version)
  └── No  → is the package's source URL resolvable?
            └── Yes → build from source
            └── No  → ERROR: "no source for package X"
```

The user can override:

```yaml
setup:
  packages:
    - name: openssl
      version: "3.0.0"        # pin a specific version
      source: build           # always build, even if apt has it
```

This is how the user's "custom curl" works:

```yaml
setup:
  packages:
    - name: curl
      version: "8.10.0"       # specific version
      source: build           # always build from source
      # Even if apt has curl 8.5.0, we build 8.10.0
```

### 15.4 The build plan (output of the resolver)

The resolver produces a `BuildPlan` that can be inspected:

```bash
$ ezx setup plan --config ezx.setup.yaml

# Build plan for postgresql-sandbox:

Step 1: apt-base
  Type: apt
  Packages: build-essential, libssl-dev, libreadline-dev,
            zlib1g-dev, libevent-dev
  Satisfies: gcc>=4.0, make>=3.0, openssl>=1.1.0, readline>=6.0,
             zlib>=1.2.0, libevent>=2.0

Step 2: postgresql
  Type: source
  Version: 16.4
  Source: https://ftp.postgresql.org/.../postgresql-16.4.tar.gz
  Depends on: apt-base
  Provides: psql, postgres, pg_ctl

Step 3: pgbouncer
  Type: source
  Version: 1.23.1
  Source: https://www.pgbouncer.org/.../pgbouncer-1.23.1.tar.gz
  Depends on: apt-base
  Provides: pgbouncer

Step 4: curl
  Type: source
  Version: 8.10.0
  Source: https://curl.se/.../curl-8.10.0.tar.gz
  Depends on: apt-base
  Provides: curl

Step 5: cleanup
  Type: cleanup
  Removes: build-essential, libssl-dev, libreadline-dev,
           zlib1g-dev, libevent-dev
  Depends on: postgresql, pgbouncer, curl

# 5 steps, 3 source builds, 1 apt install, 1 cleanup
```

The user can `ezx setup plan` to see what ezx will do before
running the build.

### 15.5 Mixed execution: apt + source builds

The build executor consumes the `BuildPlan` and runs each step
in topological order:

```go
// setup/service.go (extends existing)
func (s *Service) Execute(ctx context.Context, plan *BuildPlan) error {
    for _, step := range plan.Steps {
        switch step.Type {
        case StepTypeApt:
            return s.runAptInstall(ctx, step)
        case StepTypeSource:
            return s.runSourceBuild(ctx, step)
        case StepTypeCleanup:
            return s.runCleanup(ctx, step)
        }
    }
}
```

The executor:

1. Runs each step in the right order (deps first)
2. Caches the result (per §5.4 "Build-time caching")
3. Logs progress with the package name and version
4. Fails fast on any error (with the step name and exit code)

### 15.6 Build-time vs. runtime dependencies

This is the key to the user's question: **the compiler is
always in the build stage but never in the final image**.

Each dependency in a manifest has a `type`:

- `type: build-time` — needed only during compilation
- `type: runtime` — needed at runtime too
- `type: optional` — used if available, but not required

The resolver tracks which deps are build-time only. The
cleanup step automatically removes them:

```yaml
# postgresql.yaml
dependencies:
  - name: gcc
    type: build-time       # → removed by cleanup
  - name: openssl
    type: optional         # → kept at runtime (for SSL support)
  - name: readline
    type: required         # → kept at runtime
```

The cleanup step generated by the resolver:

```yaml
# Auto-generated by assembler
- name: cleanup
  type: cleanup
  removes:
    - build-essential         # gcc, g++, make
    - libssl-dev              # openssl headers (but not libssl3)
    - libreadline-dev         # readline headers (but not libreadline8)
    - zlib1g-dev              # zlib headers (but not zlib1g)
    - libevent-dev            # libevent headers (but not libevent-2.1)
  keep:
    - libssl3                 # runtime SSL
    - libreadline8            # runtime readline
    - zlib1g                  # runtime zlib
    - libevent-2.1            # runtime libevent
```

ezx is smart enough to **keep the runtime package** (e.g.,
`libssl3`) while **removing only the dev package** (e.g.,
`libssl-dev`). This is automatic based on the `*-dev` naming
convention.

#### User override

The user can keep build-time deps if they want (e.g., for
development images):

```yaml
setup:
  packages:
    - name: postgresql
      version: "16.4"
      keep_build_deps: true    # keep the compiler (for dev images)
```

Or remove more aggressively (for minimal images):

```yaml
setup:
  packages:
    - name: postgresql
      version: "16.4"
      # Default: keep all runtime deps
      # Aggressive: remove optional deps too
      minimal: true
```

### 15.7 The custom-curl example (the user's exact case)

User wants: "custom curl version that didn't want from apt or
dnf install".

```yaml
# ezx.setup.yaml
setup:
  packages:
    - name: postgresql
      version: "16.4"
    - name: pgbouncer
      version: "1.23.1"
    - name: curl
      version: "8.10.0"       # the user's custom version
      source: build           # always build from source
```

ezx's resolver produces:

```
Step 1: apt-base
  Type: apt
  Packages: build-essential, libssl-dev, libreadline-dev,
            zlib1g-dev, libevent-dev
  Satisfies: gcc, make, openssl-dev, readline-dev, zlib-dev, libevent-dev

Step 2: postgresql
  Type: source
  Version: 16.4
  Source: https://ftp.postgresql.org/.../postgresql-16.4.tar.gz
  Depends on: apt-base
  Build: ./configure --with-openssl --with-readline && make world && make install-world

Step 3: pgbouncer
  Type: source
  Version: 1.23.1
  Source: https://www.pgbouncer.org/.../pgbouncer-1.23.1.tar.gz
  Depends on: apt-base
  Build: ./configure && make && make install

Step 4: curl
  Type: source
  Version: 8.10.0
  Source: https://curl.se/.../curl-8.10.0.tar.gz
  Depends on: apt-base
  Build: ./configure --with-openssl && make && make install

Step 5: cleanup
  Type: cleanup
  Removes: build-essential, libssl-dev, libreadline-dev, zlib1g-dev, libevent-dev
  Keeps:   libssl3, libreadline8, zlib1g, libevent-2.1
```

Final image contains:

- postgresql 16.4 (built from source)
- pgbouncer 1.23.1 (built from source)
- curl 8.10.0 (built from source) — **the user's custom version**
- libssl3, libreadline8, zlib1g, libevent-2.1 (runtime libs only)
- **No compiler** (build-essential removed)
- **No dev headers** (all *-dev removed)

Exactly what the user asked for.

### 15.8 Custom package manifests

Users can add their own packages in the YAML:

```yaml
setup:
  packages:
    - name: my-custom-thing
      type: source
      url: https://example.com/my-thing-1.0.tar.gz
      checksum: sha256:abc123...
      dependencies:
        - name: openssl
          constraint: ">= 1.1.0"
          type: required
        - name: zlib
          constraint: ">= 1.2.0"
          type: optional
      build:
        configure: [./configure, --prefix=/usr/local, --with-openssl]
        build:     [make, "-j{{ .BuildJobs }}"]
        install:   [make, install]
      provides:
        binaries: [my-thing]
```

ezx merges the user's manifests with the built-in ones at
load time. Conflict detection (§13) catches any conflicts
(e.g., two packages providing the same binary).

### 15.9 Constraint conflicts (the "incompatible versions" problem)

What if the user wants:

- postgresql 16.4 (needs openssl >= 1.1.0)
- some-old-thing (needs openssl < 1.0.0)

The resolver detects this and reports a conflict:

```
✗ CONFLICT: incompatible version constraints for "openssl"
  Required by:
    - postgresql 16.4: openssl >= 1.1.0
    - some-old-thing 1.0: openssl < 1.0.0
  Resolution: upgrade some-old-thing to a newer version,
  or remove postgresql, or use a different openssl.
```

The user must resolve the conflict before ezx will build.

### 15.10 The assembler package (new use case)

To support this, we add a new use case package:

```
ezx/
├── assembler/                     # NEW USE CASE — package resolution
│   │                             # Mirrors phpv's assembler
│   ├── service.go                # ResolvePackages, ExecutePlan
│   ├── resolver.go               # constraint satisfaction
│   ├── planner.go                # build plan generator
│   ├── manifest.go               # PackageManifest type
│   └── service_test.go
├── internal/
│   ├── repository/
│   │   ├── memory/
│   │   │   ├── packages/         # NEW — built-in package manifests
│   │   │   │   ├── postgresql.yaml
│   │   │   │   ├── pgbouncer.yaml
│   │   │   │   ├── curl.yaml
│   │   │   │   ├── openssl.yaml
│   │   │   │   ├── zlib.yaml
│   │   │   │   └── ...
│   │   │   └── packages.go       # PackageRepository implementation
│   │   └── disk/
│   │       └── packages.go       # for user-defined packages
```

The `assembler/` package:

- Defines `PackageManifest`, `PackageRequest`, `BuildPlan` types in `domain/`
- Implements constraint satisfaction (uses a SAT solver or simple backtracking)
- Calls `registry/` for source URL resolution
- Calls `setup/` for build execution
- Is wired in `app/main.go` via fx

### 15.11 Integration with existing design

The new high-level model **extends** the existing design:

- **§4.6 (Sources)**: stays as-is for low-level users. The
  `source:` field in `setup.packages[]` is the same as the
  `source:` field in `setup.steps[]`.
- **§11.3 (Plugin model)**: plugins can provide custom package
  manifests via schema extensions. A plugin like
  `php-packages` could register manifests for `php`, `php-fpm`,
  `php-redis`, etc.
- **§13 (Conflict detection)**: extends to detect version
  conflicts in the high-level model.
- **§14.8 (Setup phase security)**: applies to both low and
  high-level models. Source verification is the same.

### 15.12 v1 scope

| Feature | v1 | v1.1+ |
| --------- | --- | ------ |
| `setup.steps[]` (low-level, current) | ✅ | ✅ |
| `setup.packages[]` (high-level) | ❌ | ✅ |
| Built-in package manifests (10-20 common packages) | ❌ | ✅ |
| Dependency resolver | ❌ | ✅ |
| Constraint satisfaction (version ranges) | ❌ | ✅ |
| Build plan generator | ❌ | ✅ |
| Automatic compiler stripping | ❌ | ✅ |
| Custom package manifests in user YAML | ❌ | ✅ |
| Version constraints in custom manifests | ❌ | ✅ |
| Conflict detection (incompatible versions) | ❌ | ✅ (extends §13) |
| Mixed apt + source builds | ❌ | ✅ |
| `ezx setup plan` subcommand | ❌ | ✅ |
| `ezx setup why <package>` (explain why a package is included) | ❌ | ✅ |

v1 ships with the **low-level `setup.steps[]` only**. The
high-level `setup.packages[]` is a v1.1+ feature.

This is consistent with the v1 philosophy: **v1 is the smallest
possible thing that solves the original problem** (basic
postgresql sandbox). The high-level model is a significant
addition that requires:

- A constraint solver (or backtracking algorithm)
- A package manifest format
- A built-in manifest database
- A build plan generator
- A way to detect version conflicts

These are all substantial work. v1.1 is the right milestone.

### 15.13 Why this matters

The current design's `setup.steps[]` works for simple cases
but doesn't scale. Real-world stacks have:

- 10-50 packages to build
- Transitive library dependencies
- Custom versions that aren't in apt
- Mixed apt + source builds
- Build-time vs. runtime distinction

The phpv model handles all of this declaratively. ezx should
too. The high-level `setup.packages[]` is the answer.

Without it, ezx users have to:

- Manually figure out the transitive deps
- Manually decide apt vs. source
- Manually write cleanup steps
- Hope they got it right

With it, ezx users just declare what they want and ezx
handles the rest. This is the same UX as:

- `apt install postgresql` (apt handles deps automatically)
- `cargo build` (cargo handles deps automatically)
- `go get github.com/foo/bar` (go modules handle deps automatically)
- `composer require php` (composer handles deps automatically)

ezx should join this list of "package managers that handle
transitive deps for you."

## 16. Dependency isolation: multiple library versions side by side

The §15 "Package management" model assumes all packages can
share the system's library tree. In reality, this is often
impossible:

- **postgresql 16** needs **openssl 1.1**
- **etcd 3.5** needs **openssl 3.0**
- **A custom exporter** needs **glibc 2.35**
- **WordPress + Apache + PHP** needs **PHP 8.3** for the site
  but **PHP 8.2** for the exporter
- **Patroni** needs a **Python venv** separate from system Python

Two different versions of `libssl.so` cannot both live in
`/usr/lib/x86_64-linux-gnu/`. The kernel only loads one. Two
different PHP versions with different module trees cannot both
live in `/usr/lib/php/`.

### 16.1 Why this matters: single-container deployment

In a microservices world (Kubernetes, Docker Compose), you'd
solve this by running each program in its own container with
its own filesystem. **But not all environments support that:**

- **Google Cloud Run** runs a single container. No sidecar
  pattern, no easy multi-container.
- **AWS Lambda** runs a single container.
- **Edge functions** (Cloudflare Workers, Vercel Edge) often
  have even more restrictions.
- **Some PaaS** (Render, Fly.io machines) restrict internal
  networking between services.
- **Monoliths by design**: WordPress + Apache + PHP + a custom
  prometheus exporter are conceptually one application. Running
  them as separate services adds operational complexity that
  many users don't want.
- **Demo / dev / CI images**: a single image that "just works"
  is much easier to distribute than a docker-compose stack.

The user's exact words: "hosting provider for example google
cloud run, can run docker image directly, and setup networking
is crazy difficult". This is a **legitimate, common, real-
world deployment scenario** that the design must support.

### 16.2 The phpv model: versioned install prefixes

The phpv project solves this for PHP using a pattern that's
been used by Linux distributions for decades: **versioned
install prefixes** with **build-time isolation** (`LDPATH`,
`LDFLAGS`) and **runtime isolation** (`LD_LIBRARY_PATH`).

phpv's approach:

1. **Each PHP version installs to its own prefix:**

   ```
   /opt/php/8.2.0/
   │ bin/php, bin/php-fpm
   │ lib/ (PHP modules, openssl-1.1, libxml2, etc.)
   │ include/
   /opt/php/8.3.0/
   │ bin/php, bin/php-fpm
   │ lib/ (PHP modules, openssl-3.0, libxml2, etc.)
   │ include/
   ```

2. **Build with custom library paths:**

   ```bash
   ./configure --prefix=/opt/php-8.3.0 \
               --with-openssl=/opt/openssl-1.1 \
               --with-curl=/opt/curl-8.10
   ```

   The `LDFLAGS=-L/opt/openssl-1.1/lib` tells the linker where
   to find openssl at build time. The `LD_LIBRARY_PATH=...`
   tells the dynamic loader where to find it at runtime.

3. **Shims for the active version:**
   `~/.phpv/shims/php` is a tiny wrapper:

   ```bash
   #!/bin/sh
   exec /opt/php-8.3.0/bin/php \
       -d extension_dir=/opt/php-8.3.0/lib/extensions \
       "$@"
   ```

   The shim sets the right env vars and execs the real binary.

4. **Switch versions atomically:**
   `phpv use 8.3` changes which shim is "active" by updating
   symlinks. The user can have 8.2 and 8.3 installed side by
   side and switch between them.

### 16.3 Generalizing for ezx: any package, any version

ezx generalizes the phpv pattern to **any package**, not just
PHP. The key insight is that the same approach works for
**any program that needs its own library tree**:

- **C/C++ programs** built against specific library versions
  (postgresql, etcd, redis, nginx)
- **Python programs** in their own venv (patroni, ansible)
- **Node.js programs** with their own node_modules (custom
  exporters, sidecar agents)
- **Go programs** that need a specific glibc version
- **Any program** that conflicts with another program in the
  same image

ezx's `setup.packages[]` model gets a new `isolated: true`
field:

```yaml
setup:
  packages:
    - name: postgresql
      version: "16.4"
      isolated: true          # NEW: install to /opt/postgresql-16.4/
    
    - name: etcd
      version: "3.5"
      isolated: true          # NEW: install to /opt/etcd-3.5/
      # This conflicts with postgresql's openssl 1.1
      # but isolation resolves it
    
    - name: patroni
      version: "4.1"
      isolated: true          # NEW: install to /opt/patroni-4.1/ (Python venv)
```

### 16.4 How isolation works

When `isolated: true` is set, ezx:

1. **Installs the package to a versioned prefix:**

   ```
   /opt/<name>/<version>/
   ├── bin/         # executables
   ├── lib/         # shared libraries
   ├── include/     # headers
   ├── share/       # data files
   └── ...
   ```

2. **Installs the package's dependencies to their own
   versioned prefixes (when they conflict):**

   ```
   /opt/openssl-1.1/     # for postgresql
   /opt/openssl-3.0/     # for etcd
   /opt/readline-8.2/    # for postgresql (only)
   /opt/zlib-1.2/        # shared
   ```

3. **Builds the package with explicit library paths:**

   ```bash
   ./configure \
       --prefix=/opt/postgresql-16.4 \
       --with-openssl=/opt/openssl-1.1 \
       --with-readline=/opt/readline-8.2 \
       --with-zlib=/opt/zlib-1.2
   ```

   The package's binaries are **statically linked** to the
   right library versions (via rpath or runpath).

4. **Generates a wrapper shim in `/usr/local/bin/`:**

   ```bash
   #!/bin/sh
   # /usr/local/bin/postgresql (auto-generated by ezx)
   export LD_LIBRARY_PATH=/opt/openssl-1.1/lib:/opt/readline-8.2/lib:/opt/zlib-1.2/lib:${LD_LIBRARY_PATH}
   exec /opt/postgresql-16.4/bin/postgres "$@"
   ```

5. **Updates the runtime config to use the shim:**

   ```yaml
   runtime:
     processChain:
       roots:
         - name: postgresql
           process:
             binaryPath: /usr/local/bin/postgresql    # the shim
             arguments: ["-D", "{{ .Env.PGDATA }}"]
   ```

The shim is **tiny** (a few shell lines) and sets the right
env vars before exec. The real binary lives in the versioned
prefix and is never in `PATH` directly.

### 16.5 The user's exact use cases, solved

#### Case 1: Google Cloud Run / single-container deployment

User runs the entire stack in one image. Networking is hard
or impossible between services. The image must be self-
contained.

```yaml
setup:
  packages:
    # postgresql needs openssl 1.1
    - name: postgresql
      version: "16.4"
      isolated: true
    
    # etcd needs openssl 3.0 (conflicts with postgresql)
    - name: etcd
      version: "3.5"
      isolated: true
    
    # patroni is a Python app, needs its own venv
    - name: patroni
      version: "4.1"
      isolated: true
      # Python deps: python 3.11, requests, psycopg, etcd3
    
    # pgbouncer can use system openssl (no conflict)
    - name: pgbouncer
      version: "1.23.1"
      # isolated: false (default) → installs to /usr/local/
```

ezx's resolver:

1. Sees postgresql needs openssl >= 1.1
2. Sees etcd needs openssl >= 3.0
3. **Installs two versions of openssl:**
   - `/opt/openssl-1.1/` (for postgresql)
   - `/opt/openssl-3.0/` (for etcd)
4. Builds postgresql against openssl 1.1
5. Builds etcd against openssl 3.0
6. Builds patroni as a Python venv
7. Installs pgbouncer normally (no isolation)
8. Generates shims in `/usr/local/bin/`:
   - `postgresql` → `/opt/postgresql-16.4/bin/postgres` (with openssl 1.1)
   - `etcd` → `/opt/etcd-3.5/bin/etcd` (with openssl 3.0)
   - `patroni` → `/opt/patroni-4.1/bin/patroni` (with Python venv)
   - `pgbouncer` → `/usr/local/bin/pgbouncer` (system openssl)

The final image has **two versions of openssl** side by side,
each used by the right program. No conflict. No need for
multiple containers. Works on Cloud Run.

#### Case 2: WordPress + Apache + PHP + exporter

The WordPress site uses PHP 8.3. The prometheus exporter
(also a PHP app) uses PHP 8.2 because of a specific module.

```yaml
setup:
  packages:
    - name: apache
      version: "2.4.62"
      isolated: true          # needs specific openssl, apr
    
    - name: php-83
      version: "8.3"
      isolated: true          # WordPress's PHP
      # provides: php, php-fpm
      # needs: openssl 3.0, libxml2, libcurl
    
    - name: php-82
      version: "8.2"
      isolated: true          # exporter's PHP
      # provides: php82, php82-fpm
      # needs: openssl 3.0 (same as 8.3, so can share)
      # different module set
    
    - name: wordpress-exporter
      version: "1.0"
      # The exporter is a PHP app that uses php-82
      # It's installed to /opt/wordpress-exporter/
      # Its shebang: #!/opt/php-82/bin/php
```

ezx installs:

- `/opt/apache-2.4.62/` (linked against /opt/openssl-3.0/)
- `/opt/php-8.3/` (linked against /opt/openssl-3.0/)
- `/opt/php-8.2/` (linked against /opt/openssl-3.0/, different modules)
- `/opt/wordpress-exporter/` (uses /opt/php-8.2/bin/php)

Two PHP versions coexist. The WordPress site uses 8.3. The
exporter uses 8.2. No conflict.

#### Case 3: HA PostgreSQL with Patroni

The classic HA setup. Patroni is a Python app that manages
postgres replication, but it needs a consensus backend
(etcd, zookeeper, or redis). All in one image, no networking
between containers.

```yaml
setup:
  packages:
    - name: postgresql
      version: "16.4"
      isolated: true
    
    - name: etcd
      version: "3.5"
      isolated: true          # consensus backend
    
    - name: patroni
      version: "4.1"
      isolated: true          # Python app
      # talks to localhost:5432 (postgres) and localhost:2379 (etcd)
```

All three run in the same container, talk to each other over
localhost. Each uses its own library tree. No conflicts.

### 16.6 The resolver's role

The §15 resolver extends to handle the isolation case:

```go
type PackageRequest struct {
    Name      string
    Version   string
    Source    string            // auto | apt | build | system
    Isolated  bool              // NEW: install to versioned prefix
}

type BuildPlan struct {
    Steps []BuildStep
}

type BuildStep struct {
    Name           string
    Type           StepType
    Package        string
    Version        string
    InstallPrefix  string         // NEW: /opt/<name>-<version> if isolated
    DependsOn      []string
    LibPaths       []string       // NEW: for LD_LIBRARY_PATH
    ShimScript     string         // NEW: path to generated shim
    // ... existing fields
}
```

The resolver:

1. Walks the dependency graph
2. For each dep, checks if it's shared (one version works for
   everyone) or conflicting (different versions needed)
3. **Installs one version for shared deps, multiple versions
   for conflicting deps**
4. Records the install prefix and lib path for each package
5. Generates shims for isolated packages

The dependency graph gets a new concept: **version edges**.
Two packages can both depend on `openssl` but require
different versions. The resolver tracks this:

```
postgresql 16.4 → openssl 1.1 (version edge)
etcd 3.5      → openssl 3.0 (version edge)

Resolver sees: two different openssl versions needed
Resolution: install both
  /opt/openssl-1.1/  (for postgresql)
  /opt/openssl-3.0/  (for etcd)
```

### 16.7 The shim generation

For each isolated package, ezx generates a wrapper script:

```go
// setup/shim.go
type ShimSpec struct {
    ShimName     string    // e.g., "postgresql"
    RealBinary   string    // e.g., "/opt/postgresql-16.4/bin/postgres"
    LibPaths     []string  // e.g., ["/opt/openssl-1.1/lib", ...]
    PathPrepend  []string  // e.g., ["/opt/postgresql-16.4/bin"]
    EnvVars      []string  // additional env vars
}

func GenerateShim(spec ShimSpec) (string, error) {
    template := `#!/bin/sh
# Auto-generated by ezx — do not edit
export LD_LIBRARY_PATH={{.LibPaths}}:${LD_LIBRARY_PATH}
export PATH={{.PathPrepend}}:${PATH}
{{- range .EnvVars}}
export {{.}}
{{- end}}
exec {{.RealBinary}} "$@"
`
    // ... render and write to /usr/local/bin/<shim_name>
}
```

The shim is **executable** (chmod 755), **owned by root**,
and is the entry point that the runtime orchestrator calls.

The runtime YAML references the shim:

```yaml
runtime:
  processChain:
    roots:
      - name: postgresql
        process:
          binaryPath: /usr/local/bin/postgresql    # the shim
          arguments: ["-D", "{{ .Env.PGDATA }}"]
          # environment: [...]                     # additional env if needed
```

The user never references `/opt/postgresql-16.4/bin/postgres`
directly. They use the shim, which abstracts the install
location.

### 16.8 rpath vs LD_LIBRARY_PATH

There are two ways to make a binary find its libraries:

1. **`LD_LIBRARY_PATH`**: set at runtime, looked up first by
   the dynamic linker. Easy to set, easy to override.
2. **rpath / runpath**: embedded in the binary at link time.
   The binary itself knows where its libs are.

The shim approach uses `LD_LIBRARY_PATH` because:

- It's simple and explicit
- It works for all binaries (including Python, Node, Go)
- It doesn't require modifying the binary at link time
- It's easy to debug (just `echo $LD_LIBRARY_PATH`)

The build-time approach (during setup) uses rpath as a backup:

- The binary is built with `LDFLAGS="-Wl,-rpath=/opt/openssl-1.1/lib"`
- This means the binary can find its libs even without
  `LD_LIBRARY_PATH` set
- The shim sets `LD_LIBRARY_PATH` for clarity, but rpath is
  the actual mechanism

For maximum reliability, ezx uses **both**:

- The binary is built with rpath pointing to its versioned
  lib directory
- The shim sets `LD_LIBRARY_PATH` for additional safety

This way, if the user runs the binary directly (without the
shim), it still finds its libs.

### 16.9 The cleanup step

The cleanup step in §15.6 (automatic compiler stripping) is
extended for isolated packages:

```yaml
# Auto-generated by assembler
- name: cleanup
  type: cleanup
  removes:
    - build-essential            # the compiler
    - libssl-dev                 # dev headers (NOT the runtime libs)
    - libreadline-dev
    - zlib1g-dev
    - libevent-dev
  keeps:
    # Isolated packages stay in the final image
    - /opt/postgresql-16.4
    - /opt/etcd-3.5
    - /opt/patroni-4.1
    - /opt/openssl-1.1           # needed by postgresql
    - /opt/openssl-3.0           # needed by etcd
    # Shared packages can be either kept or removed
    - libssl3                    # if no isolated package needs it
    - libreadline8
```

The user can override:

```yaml
setup:
  packages:
    - name: postgresql
      version: "16.4"
      isolated: true
      # Default: keep /opt/postgresql-16.4 in final image
      # Override: remove after use (won't work, postgres runs at runtime)
      # Actually: isolated=true means the package is kept
```

For isolated packages, the cleanup **never removes the
package itself** (it's needed at runtime). It only removes
the build-time artifacts (sources, build dirs, compilers).

### 16.10 `/etc/ld.so.conf.d/` integration (v1.2+)

In addition to the shim approach, ezx can write to
`/etc/ld.so.conf.d/ezx-*.conf` and run `ldconfig`. This is
the "Linux native" way to add library search paths.

```bash
# /etc/ld.so.conf.d/ezx-openssl-1.1.conf
/opt/openssl-1.1/lib

# /etc/ld.so.conf.d/ezx-openssl-3.0.conf
/opt/openssl-3.0/lib
```

After writing these, run `ldconfig` to update the cache. Now
**any** binary (not just the shimmed ones) can find these
libs.

This is more invasive (touches system config) but more
"Linux native". It's a v1.2+ feature; v1.1 uses the shim
approach only.

### 16.11 The marketplace and isolation

A vendor shipping an isolated package as a plugin:

```yaml
# Plugin: postgresql-ha
apiVersion: ezx/v1
kind: Plugin
metadata:
  name: postgresql-ha
  version: 1.0.0
spec:
  type: sidecar
  # The plugin ships pre-built binaries for:
  #   - postgresql 16.4 (isolated)
  #   - etcd 3.5 (isolated)
  #   - patroni 4.1 (isolated)
  # User just installs the plugin and runs it
```

The plugin's `OnSetup` hook extracts the pre-built binaries
to `/opt/postgresql-16.4/`, `/opt/etcd-3.5/`, etc. The
runtime orchestrator uses the shims in `/usr/local/bin/`.

This is the **same pattern as Docker images** but with
isolation. A user can download a "postgresql HA plugin" and
get a working HA setup without compiling anything.

### 16.12 Integration with existing design

This extends several existing sections:

1. **§4.6 (Sources)**: the `source:` field gets a new
   `install_prefix` field for isolated installs
2. **§15 (Package management)**: the `setup.packages[]` model
   gets a new `isolated: true` field
3. **§15.3 (Resolver)**: handles conflicting version edges
4. **§15.6 (Cleanup)**: knows not to remove isolated packages
5. **§3 (Clean architecture)**: the runtime orchestrator
   doesn't change — `binaryPath` already supports a shim
6. **§11.5 (Plugins)**: plugins can ship pre-built isolated
   packages

The runtime orchestrator doesn't need to change. The
`Process.BinaryPath` field already supports any path, and the
`Process.Environment` field already supports per-process env
vars. The shim is just a static script that the setup phase
generates.

### 16.13 What about Python venvs?

For Python packages (like Patroni), isolation is done via
**virtual environments**:

```bash
# Create a venv
python3 -m venv /opt/patroni-4.1

# Install dependencies
/opt/patroni-4.1/bin/pip install patroni[etcd] psycopg2-binary

# The patroni binary is at /opt/patroni-4.1/bin/patroni
# It uses /opt/patroni-4.1/lib/python3.11/site-packages/
```

The shim for patroni:

```bash
#!/bin/sh
# /usr/local/bin/patroni (auto-generated)
export PATH=/opt/patroni-4.1/bin:$PATH
export VIRTUAL_ENV=/opt/patroni-4.1
exec /opt/patroni-4.1/bin/patroni "$@"
```

This is the **standard Python venv pattern** wrapped in an
ezx-managed shim. The user gets the isolation benefits of
venvs with the convenience of ezx's package management.

### 16.14 v1 scope

| Feature | v1 | v1.1+ |
| --------- | --- | ------ |
| `setup.steps[]` (low-level) | ✅ | ✅ |
| `setup.packages[]` (high-level) | ❌ | ✅ |
| Isolated installs (`isolated: true`) | ❌ | ✅ |
| Versioned install prefixes (`/opt/<name>-<version>/`) | ❌ | ✅ |
| Multiple versions of the same lib | ❌ | ✅ |
| Shim generation (`/usr/local/bin/<name>`) | ❌ | ✅ |
| Build-time isolation (`LDFLAGS`, `LDPATH`) | ❌ | ✅ |
| Runtime isolation (`LD_LIBRARY_PATH`) | ❌ | ✅ |
| rpath in built binaries | ❌ | ✅ |
| Python venv isolation | ❌ | ✅ |
| Plugin-shipped pre-built isolated packages | ❌ | ✅ |
| `/etc/ld.so.conf.d/` integration | ❌ | v1.2 |
| `ezx setup plan --show-isolation` | ❌ | ✅ |

v1 ships with the **low-level `setup.steps[]`** only. The
high-level `setup.packages[]` with isolation is a **v1.1+
feature**.

This is consistent with the v1 philosophy: **v1 is the
smallest possible thing that solves the original problem**
(basic postgresql sandbox, single version, no isolation). The
isolation model is a major feature that requires:

- Resolver changes (handling version edges)
- Shim generation
- Build-time isolation (LDPATH/LDFLAGS)
- rpath support
- Python venv support
- Testing across conflicting versions

All of this is substantial work. v1.1 is the right milestone.

### 16.15 Why this matters

The user's concern is **the defining use case for ezx as a
product**. ezx is designed to replace bash scripts in
container images. Those images are deployed to **single-
container environments** (Cloud Run, Lambda, monoliths, edge).
In those environments, **dependency isolation is the only way
to make the image work**.

Without isolation:

- You're stuck with whatever apt provides
- You can't use a custom curl version
- You can't have postgresql + etcd in the same image (openssl
  conflict)
- You can't have WordPress + exporter with different PHP
  versions

With isolation:

- You can use any version of any library
- Conflicting programs coexist
- Single-container deployment works for any stack
- The user has the same flexibility as a multi-container
  deployment, but with the simplicity of one image

This is **the difference between ezx being a nice-to-have
and ezx being essential**. A user running on Cloud Run with
postgresql + patroni + etcd **needs** this feature. Without
it, they go back to bash.

### 16.16 The phpv parallel

The user referenced phpv's LDPATH/LDFLAGS support. This is
exactly the same pattern, generalized:

| phpv | ezx |
| ------ | ----- |
| PHP versions | Any package |
| `/opt/php/<version>/` | `/opt/<name>/<version>/` |
| `LDPATH`, `LDFLAGS` | Build-time isolation |
| `LD_LIBRARY_PATH` | Runtime isolation (shim) |
| Shims in `~/.phpv/shims/` | Shims in `/usr/local/bin/` |
| `phpv use 8.3` | (not needed — shim picks the right one) |
| Multiple PHP versions | Multiple versions of any package |

Anyone familiar with phpv will recognize the pattern. The
extension is: ezx does this for **any package**, not just
PHP, and **automates the resolver and shim generation** that
phpv does manually.

---
