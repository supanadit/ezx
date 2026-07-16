## ADDED Requirements

### Requirement: Config loader reads two-stage YAML

The system SHALL read two YAML files: `ezx.setup.yaml` and `ezx.runtime.yaml`.
The setup file declares `setup.steps[]` for build-time execution. The
runtime file declares `processChain`, `files`, `healthcheck`, and
`telemetry` for container execution.

#### Scenario: Both files load successfully

- **WHEN** `ezx run --config ezx.runtime.yaml` is invoked
- **THEN** the system loads and validates both YAML files
- **AND** the system exits with code 0 on valid files

#### Scenario: Missing runtime file

- **WHEN** `ezx run --config missing.yaml` is invoked
- **THEN** the system prints a clear error message
- **AND** the system exits with a non-zero code

### Requirement: Strict env schema validation

The system MUST declare an `envSchema` block in the runtime YAML listing
every env var the configuration accepts. The system SHALL reject any env
var that is not declared in the schema.

#### Scenario: Unknown env var is rejected

- **WHEN** the container is started with an env var not in the envSchema
- **THEN** `ezx validate` exits with a non-zero code
- **AND** the error message lists the unknown env var name

#### Scenario: All env vars are declared

- **WHEN** the container is started with env vars matching the envSchema
- **THEN** `ezx validate` exits with code 0

### Requirement: validate command runs in CI

The system SHALL provide an `ezx validate` command that loads the YAML
and env schema and exits 0 on success or non-zero on validation errors.
The command MUST NOT start any processes or open network ports.

#### Scenario: validate passes

- **WHEN** `ezx validate --config ezx.runtime.yaml` is run
- **THEN** the system loads and validates the file
- **AND** exits 0 if valid

#### Scenario: validate fails on missing required key

- **WHEN** the YAML is missing a required key
- **THEN** the system prints a clear error
- **AND** exits non-zero
