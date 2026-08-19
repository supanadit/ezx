//go:build !linux

package repository

import (
	"fmt"
	"os/exec"

	"github.com/supanadit/ezx/domain"
)

// BuildProcessEnv is platform-agnostic; the driver reuses it.
func BuildProcessEnv(parentEnv []string, p domain.Process) ([]string, error) {
	base, err := Filter(parentEnv, p.FilterEnv, p.FilterEnvPattern)
	if err != nil {
		return nil, err
	}
	return append(base, p.Environment...), nil
}

// Exec is unsupported on non-Linux platforms (PID 1 replacement is a
// container/Linux concern).
func Exec(p domain.Process, env []string) error {
	return fmt.Errorf("exec (PID 1 replacement) is only supported on linux")
}

// applyCredential is a no-op on non-Linux platforms.
func ApplyCredential(cmd *exec.Cmd, p domain.Process) error {
	if p.User != "" {
		return fmt.Errorf("process user %q is only supported on linux", p.User)
	}
	return nil
}

// SetProcessGroupLeader is a no-op on non-Linux platforms.
func SetProcessGroupLeader(cmd *exec.Cmd) {}
