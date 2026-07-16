## ADDED Requirements

### Requirement: Process orchestrator runs a DAG

The system SHALL run a process DAG declared in `processChain.roots[]`.
Roots start in parallel. Each child process declares its parent via
`needParentReady: true`; the orchestrator SHALL start the child only
after the parent's readiness probe passes.

#### Scenario: Single root starts and stays running

- **WHEN** the runtime YAML declares one root process with no children
- **THEN** the orchestrator starts the root
- **AND** waits for its readiness probe
- **AND** keeps it running until shutdown

#### Scenario: Two roots start in parallel

- **WHEN** the runtime YAML declares two root processes
- **THEN** the orchestrator starts both concurrently
- **AND** waits for both readiness probes to pass

#### Scenario: Child starts only after parent ready

- **WHEN** a child process declares `needParentReady: true`
- **THEN** the orchestrator starts the child only after the parent's
  readiness probe passes
- **AND** the child's logs are not emitted before the parent is ready

### Requirement: Reverse-DAG drain on shutdown

When the system receives `SIGTERM` or `SIGINT`, the orchestrator SHALL
stop processes in reverse topological order (children first, parents
last). If processes have not exited within `shutdownTimeoutSeconds`
(default 30), the orchestrator SHALL send `SIGKILL`.

#### Scenario: Graceful shutdown within timeout

- **WHEN** `SIGTERM` is received and all processes exit within 10s
- **THEN** the orchestrator returns exit code 0
- **AND** `ezx_shutdown_duration_seconds` records the elapsed time

#### Scenario: Forced shutdown after timeout

- **WHEN** `SIGTERM` is received and a process does not exit within 30s
- **THEN** the orchestrator sends `SIGKILL` to the stuck process
- **AND** returns exit code 0 (forced shutdown is not a failure)

### Requirement: Process control uses pure I/O

The system SHALL implement process start, stop, wait, and signal as
pure I/O in `internal/repository/system/process.go`. The orchestrator
MUST NOT call exec or signal directly; it MUST go through the
repository interface.

#### Scenario: Start forks and tracks the PID

- **WHEN** the orchestrator calls `Start("echo", ["hello"])`
- **THEN** the system forks the process
- **AND** returns a process handle with the child PID

#### Scenario: Signal sends to the PID

- **WHEN** the orchestrator calls `Signal(pid, SIGTERM)`
- **THEN** the system sends the signal to that PID
- **AND** returns an error if the PID is not tracked

### Requirement: Readiness probes

The system SHALL support three readiness probe types: `tcp`, `exec`, and
`http`. A probe is considered passing when its underlying check succeeds
within `timeoutSeconds` (default 5) and `periodSeconds` (default 10).

#### Scenario: TCP probe passes when port is open

- **WHEN** a process listens on `127.0.0.1:8080`
- **THEN** a `tcp` probe to `127.0.0.1:8080` passes

#### Scenario: TCP probe fails when port is closed

- **WHEN** no process listens on `127.0.0.1:8080`
- **THEN** a `tcp` probe to `127.0.0.1:8080` fails after `timeoutSeconds`

#### Scenario: HTTP probe passes on 2xx

- **WHEN** an HTTP endpoint returns 200 OK
- **THEN** an `http` probe to that URL passes

#### Scenario: Exec probe passes on exit code 0

- **WHEN** the configured command exits with code 0
- **THEN** an `exec` probe passes
