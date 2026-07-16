## ADDED Requirements

### Requirement: Template rendering with env substitution

The system SHALL render template files declared in the runtime YAML
`files[]` block. Each file declaration MUST specify `from` (source
template), `to` (destination path), and `mode` (file permissions). The
template engine MUST support Go `text/template` syntax with env var
substitution via `{{ .Env.NAME }}`.

#### Scenario: Template renders with env value

- **WHEN** the template is `listen = {{ .Env.LISTEN_PORT }}`
- **AND** `LISTEN_PORT=8080` is set
- **THEN** the rendered file contains `listen = 8080`

#### Scenario: Missing env var causes render error

- **WHEN** the template references `.Env.MISSING`
- **AND** `MISSING` is not set
- **THEN** the system prints a clear error
- **AND** the file is not written

### Requirement: Atomic file writes

The system SHALL write rendered files atomically. The implementation
MUST write to a temporary file in the same directory, fsync, and rename
over the destination. A partial write MUST NOT leave the destination
in a half-written state.

#### Scenario: Atomic write succeeds

- **WHEN** a file is rendered and written
- **THEN** the destination is either the old content or the new content
- **AND** never a partial mix

#### Scenario: Write failure does not corrupt destination

- **WHEN** the rename fails (e.g. disk full)
- **THEN** the original destination is unchanged

### Requirement: File permissions are honored

The system SHALL set the rendered file's mode to the value declared in
the YAML. The mode MUST be applied atomically with the rename.

#### Scenario: Mode 0600 is applied

- **WHEN** a file is declared with `mode: 0600`
- **THEN** the rendered file has permissions `0600`

#### Scenario: Mode 0644 is applied

- **WHEN** a file is declared with `mode: 0644`
- **THEN** the rendered file has permissions `0644`
