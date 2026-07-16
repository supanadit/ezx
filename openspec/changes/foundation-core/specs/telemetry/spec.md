## ADDED Requirements

### Requirement: Structured JSON logs

The system SHALL emit structured JSON logs to stdout. Each log line
MUST include a `time`, `level`, `msg`, and any structured key-value
fields. The system MUST NOT emit unstructured or multi-line log
records.

#### Scenario: Log line is valid JSON

- **WHEN** the orchestrator logs an event
- **THEN** the log line is a single JSON object
- **AND** parses with `encoding/json`

#### Scenario: Log level is configurable

- **WHEN** `EZX_LOG_LEVEL=debug` is set
- **THEN** debug-level messages are emitted
- **WHEN** `EZX_LOG_LEVEL=error` is set
- **THEN** only error-level messages are emitted

### Requirement: Prometheus metrics on port 9090

The system SHALL expose Prometheus metrics at
`http://localhost:9090/metrics`. The endpoint MUST be scrapable by a
standard Prometheus scraper. The metrics port SHALL be configurable via
`telemetry.port` in the runtime YAML, with default 9090.

#### Scenario: Metrics endpoint responds

- **WHEN** `curl http://localhost:9090/metrics` is run
- **THEN** the response is `text/plain` Prometheus format
- **AND** includes the documented ezx metrics

#### Scenario: Metrics port is configurable

- **WHEN** `telemetry.port: 9091` is set in the runtime YAML
- **THEN** the metrics endpoint is on port 9091
- **AND** not on port 9090

#### Scenario: Metrics can be disabled

- **WHEN** `telemetry.enabled: false` is set
- **THEN** no metrics endpoint is started
- **AND** no port 9090 is bound

### Requirement: Documented baseline metrics

The system SHALL emit the following metrics at minimum:

- `ezx_process_up` (gauge, per process): 1 if running, 0 otherwise.
- `ezx_process_ready` (gauge, per process): 1 if readiness probe passed.
- `ezx_healthcheck_ok` (gauge): 1 if last healthcheck passed.
- `ezx_shutdown_duration_seconds` (histogram): time spent in shutdown.

#### Scenario: Process up metric reflects state

- **WHEN** a process is running
- **THEN** `ezx_process_up{process="..."}` is 1
- **WHEN** the process exits
- **THEN** the metric is 0

#### Scenario: Shutdown duration is recorded

- **WHEN** the orchestrator shuts down
- **THEN** `ezx_shutdown_duration_seconds` records the elapsed time
