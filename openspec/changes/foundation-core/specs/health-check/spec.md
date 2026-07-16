## ADDED Requirements

### Requirement: healthcheck command is a single binary invocation

The system SHALL provide an `ezx healthcheck` command that runs all
configured health checks in sequence. The command SHALL exit 0 when all
checks pass and 1 when any check fails. The command MUST NOT start any
long-running process or daemon.

#### Scenario: All health checks pass

- **WHEN** `ezx healthcheck --config ezx.runtime.yaml` is run
- **AND** all configured health checks pass
- **THEN** the command exits 0

#### Scenario: One health check fails

- **WHEN** `ezx healthcheck --config ezx.runtime.yaml` is run
- **AND** one configured health check fails
- **THEN** the command exits 1
- **AND** the failing check name is printed to stderr

### Requirement: health checks are configurable

The system SHALL allow the user to declare one or more health checks in
the runtime YAML. Each check declares its `type` (matching a readiness
probe type) and its arguments.

#### Scenario: Single TCP health check

- **WHEN** the runtime YAML declares a TCP health check on port 8080
- **THEN** the healthcheck command runs the TCP probe
- **AND** exits 0 if the port is open

#### Scenario: Multiple health checks

- **WHEN** the runtime YAML declares two health checks
- **THEN** the healthcheck command runs both in sequence
- **AND** exits 0 only if both pass

### Requirement: Health checks are integrated with Docker HEALTHCHECK

The example Dockerfiles SHALL use the `ezx healthcheck` command as the
container's `HEALTHCHECK CMD`. The orchestrator MUST NOT poll health
checks itself; Docker's `HEALTHCHECK` is the source of truth for
container health.

#### Scenario: Example Dockerfile uses healthcheck

- **WHEN** the example image is built
- **THEN** its Dockerfile declares `HEALTHCHECK CMD ezx healthcheck ...`
- **AND** Docker's `docker ps` shows the container as `healthy` when
  all checks pass
