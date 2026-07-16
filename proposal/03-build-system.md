# Proposal 03 — Build System (v0.3)

## Goal

Make image builds fast, reproducible, and declarative. Replace hand-written
install scripts with typed source strategies and per-step Docker layer
caching.

## In scope

### Source strategies

- `apt` — install Debian packages.
- `autotools` — download tarball, `./configure && make && make install`.
- `git` — clone repo, build, install.
- `binary` — download pre-built tarball and extract.
- `script` — arbitrary shell escape hatch.

Each strategy declares the YAML keys it consumes so ezx can split inputs for
per-step caching.

### Registry use case

- `registry/` package defining `RegistryResolver` interface.
- `internal/repository/builtin/` implementations:
  - `go`, `node`, `postgresql`, `pgbouncer`, `pgbackrest`, `github`, `pypi`,
    `http`.
- Custom registries in YAML for private mirrors.

### Per-step Dockerfile emission

- `ezx setup emit-dockerfile --config ezx.setup.yaml`
- Generates one `RUN` per `setup.steps[]` entry.
- `--with-inputs` generates `steps/<name>.input` files so Docker layer
  caching works per step.
- BuildKit cache mounts for apt and build directories.

### In-step caching

- `ezx setup run-step --name <name> --input <path>`
- SHA-256 input hash cache in `${EZX_BUILD_DIR}/.ezx/cache/`.
- If inputs are unchanged, the step skips even if Docker invalidated the
  layer.

### Package management (basic)

- `setup.packages[]` high-level block.
- Dependency resolver produces a build plan mixing apt and source builds.
- Build-time vs runtime dependency declaration.

### Dependency isolation

- Versioned install prefixes (`/usr/local/pgsql-16.4`, etc.).
- `shim` generation so multiple versions can coexist.
- `rpath` / `LD_LIBRARY_PATH` handling.

## Out of scope

- Marketplace (distribution).
- Plugin / extension system.
- HA / replication.
- Operational API.

## Deliverables

1. `registry/` use case + `internal/repository/builtin/` resolvers.
2. `setup/` source strategy implementations: apt, autotools, git, binary,
   script.
3. `ezx setup emit-dockerfile` and `ezx setup run-step` commands.
4. Per-step input generation and in-step caching.
5. Basic `setup.packages[]` resolver.
6. Dependency isolation for versioned prefixes.
7. CI test that bumps one package version and verifies only one step re-runs.

## Acceptance criteria

- `ezx setup emit-dockerfile` produces a valid Dockerfile from
  `ezx.setup.yaml`.
- Bumping a package version only rebuilds the affected step.
- A source step whose inputs are unchanged is skipped via the in-step cache.
- A package declared in `setup.packages[]` resolves to apt or source build
  automatically.
- Two versions of the same library can coexist in the same image without
  collision.

## Depends on

- [Proposal 01 — Foundation Core](./01-foundation-core.md)
- [Proposal 02 — Runtime Extensions](./02-runtime-extensions.md) for scheduler
  / reconciler extension points used by later build steps.

## Open questions

- Should the resolver prefer apt over source builds by default?
- Should dependency isolation be opt-in or default?
- Should the build cache live inside the build stage or be a host-mounted
  BuildKit cache?

## Risks

- Versioned prefixes can break assumptions in downstream steps. Careful
  `PATH` and `pkg-config` path management is required.
- Custom `script` sources bypass reproducibility. Keep them as an explicit
  escape hatch and warn in validation.
