## ADDED Requirements

### Requirement: setup command runs build steps serially

The system SHALL provide an `ezx setup` command that reads
`ezx.setup.yaml` and runs each `setup.steps[]` entry in declaration
order. Each step MUST complete (exit code 0) before the next step
starts.

#### Scenario: Steps run in order

- **WHEN** `ezx setup --config ezx.setup.yaml` is run
- **AND** the YAML declares three steps
- **THEN** the system runs step 1, then step 2, then step 3
- **AND** the total exit code is 0 if all steps succeed

#### Scenario: Step failure aborts the run

- **WHEN** step 2 exits with non-zero
- **THEN** step 3 is not run
- **AND** `ezx setup` exits with a non-zero code
- **AND** the failing step name is printed to stderr

### Requirement: Step declaration shape

The system SHALL accept `setup.steps[]` entries with a `name` and
`run` (a shell command string). The shell command MUST be executed
via `sh -c`.

#### Scenario: Step with name and run

- **WHEN** a step declares `name: "install-deps"` and `run: "apt-get install -y curl"`
- **THEN** the system runs `sh -c "apt-get install -y curl"`
- **AND** the step name is used in log lines

### Requirement: Setup is independent from runtime

The system MUST NOT require `ezx.runtime.yaml` to exist when running
`ezx setup`. The setup command is intended to run inside a Dockerfile
build stage and must work with only the setup file present.

#### Scenario: setup runs without runtime file

- **WHEN** `ezx setup --config ezx.setup.yaml` is run
- **AND** `ezx.runtime.yaml` does not exist
- **THEN** the system runs all steps successfully
- **AND** does not error on missing runtime file

### Requirement: Setup environment inherits container env

Each step MUST inherit the container's environment variables. The
system MUST NOT clear or filter the environment before running a step.

#### Scenario: Step sees container env

- **WHEN** the container has `DEBIAN_FRONTEND=noninteractive` set
- **THEN** a step's `run` command sees `DEBIAN_FRONTEND=noninteractive`
