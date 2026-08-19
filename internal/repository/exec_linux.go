//go:build linux

package repository

import (
	"fmt"
	"os/exec"
	"syscall"

	"github.com/supanadit/ezx/domain"
)

// BuildProcessEnv assembles the environment for a spawned or exec'd process:
// the parent environment filtered by the process's FilterEnv/FilterEnvPattern,
// with the process's additive Environment entries appended last so they
// override any inherited or filtered values.
func BuildProcessEnv(parentEnv []string, p domain.Process) ([]string, error) {
	base, err := Filter(parentEnv, p.FilterEnv, p.FilterEnvPattern)
	if err != nil {
		return nil, err
	}
	return append(base, p.Environment...), nil
}

// Exec replaces the current process image with the given process via
// syscall.Exec. The environment is built exactly as for a spawned process
// (filtering + additive entries apply identically). The caller (the
// orchestrator) disappears and the target becomes PID 1.
func Exec(p domain.Process, env []string) error {
	procEnv, err := BuildProcessEnv(env, p)
	if err != nil {
		return err
	}
	argv := append([]string{p.BinaryPath}, p.Arguments...)
	return syscall.Exec(p.BinaryPath, argv, procEnv)
}

// ApplyCredential sets the process's runtime user/group on the command when
// the process specifies a User.
func ApplyCredential(cmd *exec.Cmd, p domain.Process) error {
	if p.User == "" {
		return nil
	}
	uid, gid, err := resolveCred(p.User, p.Group)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
	}
	return nil
}

// SetProcessGroupLeader places the child in its own process group so signals
// can be forwarded to the group safely (pgid == child pid).
func SetProcessGroupLeader(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func resolveCred(user, group string) (uid, gid int, err error) {
	uid, err = ResolveUID(user)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve user %q: %w", user, err)
	}
	if group != "" {
		gid, err = ResolveGID(group)
		if err != nil {
			return 0, 0, fmt.Errorf("resolve group %q: %w", group, err)
		}
		return uid, gid, nil
	}
	gid = uid
	return uid, gid, nil
}
