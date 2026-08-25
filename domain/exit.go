package domain

import "fmt"

// ExitError carries a process exit code so the app can exit the container with
// the supervised main process's code. A nil error from Wait() must not be
// treated as "exit 0" when the process actually exited non-zero.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("process exited with code %d", e.Code)
}
