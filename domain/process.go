package domain

// Process holds the configuration needed to run a program and spawn a process.
// It encapsulates the binary path, command-line arguments, environment variables, and optional working directory.
// Tradeoffs: Designed as a simple data model to avoid complexity (e.g., no validation methods here);
// extensibility is prioritized for future logic (e.g., spawning code can add checks or defaults).
// Fields are designed for optionality: slices can be nil/empty, strings default to empty (meaning "use system default" in logic).
type Process struct {
	// BinaryPath is the absolute or relative path to the executable binary (required; empty means invalid).
	BinaryPath string
	// Arguments is a slice of command-line arguments (optional; nil or empty slice means no args).
	Arguments []string
	// Environment is a slice of environment variables in "KEY=VALUE" format (optional; nil or empty means inherit from parent process).
	Environment []string
	// WorkingDir is the optional working directory (optional; empty string means use current directory).
	WorkingDir string
}
