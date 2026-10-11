//go:build linux

package system

import (
	"fmt"
	"os"
	"os/exec"
	osuser "os/user"
	"strconv"
	"syscall"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
)

// ExecErr wraps an exec failure with the requested binary path so the operator
// can see which program could not be started. syscall.Exec returns a bare ENOENT
// for both a missing binary and a missing interpreter, which is impossible to
// diagnose without this context.
type ExecErr struct {
	Binary string
	Err    error
}

func (e *ExecErr) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exec %q failed", e.Binary)
	}
	return fmt.Sprintf("exec %q: %v", e.Binary, e.Err)
}

func (e *ExecErr) Unwrap() error { return e.Err }

// BuildProcessEnv assembles the environment for a spawned or exec'd process:
// the parent environment filtered by the process's FilterEnv/FilterEnvPattern,
// with the process's additive Environment entries appended last so they
// override any inherited or filtered values.
func BuildProcessEnv(parentEnv []string, p domain.Process) ([]string, error) {
	base, err := repository.Filter(parentEnv, p.FilterEnv, p.FilterEnvPattern)
	if err != nil {
		return nil, err
	}
	return append(base, p.Environment...), nil
}

// Exec replaces the current process image with the given process via
// syscall.Exec. The environment is built exactly as for a spawned process
// (filtering + additive entries apply identically). The caller (the
// orchestrator) disappears and the target becomes PID 1.
//
// When the process declares a User, ezx drops privileges on itself (setgroups,
// setgid, setuid) before exec'ing — the same technique gosu/su-exec use. This
// keeps `user:` meaningful on exec nodes instead of silently running the
// application as root, and the target still becomes PID 1 (no wrapper process).
func Exec(p domain.Process, env []string) error {
	procEnv, err := BuildProcessEnv(env, p)
	if err != nil {
		return err
	}

	// Fail fast with an actionable message instead of a bare ENOENT.
	resolved, err := resolveBinary(p.BinaryPath)
	if err != nil {
		return &ExecErr{Binary: p.BinaryPath, Err: err}
	}

	if p.User != "" {
		if err := dropPrivileges(p.User, p.Group); err != nil {
			return &ExecErr{Binary: p.BinaryPath, Err: err}
		}
	}

	argv := append([]string{p.BinaryPath}, p.Arguments...)
	if err := syscall.Exec(resolved, argv, procEnv); err != nil {
		return &ExecErr{Binary: p.BinaryPath, Err: err}
	}
	return nil
}

// dropPrivileges irreversibly switches the calling process (ezx, PID 1) to the
// requested user/group. Order matters: supplementary groups, then gid, then
// uid — once setuid succeeds the process can no longer change its gid.
func dropPrivileges(user, group string) error {
	uid, gid, err := resolveCred(user, group)
	if err != nil {
		return err
	}
	if uid == 0 {
		// Root is the current identity; nothing to drop.
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("cannot switch to user %q: ezx is not running as root", user)
	}

	gids, err := supplementaryGroups(user, gid)
	if err != nil {
		return err
	}
	if err := syscall.Setgroups(gids); err != nil {
		return fmt.Errorf("setgroups for %q: %w", user, err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid(%d) for %q: %w", gid, user, err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid(%d) for %q: %w", uid, user, err)
	}
	// Verify the drop actually took effect; a silent partial drop is worse than
	// a hard failure.
	if os.Geteuid() != uid || os.Getegid() != gid {
		return fmt.Errorf("privilege drop for %q did not take effect (uid=%d gid=%d)", user, os.Geteuid(), os.Getegid())
	}
	return nil
}

// supplementaryGroups returns the group list for a user (its primary gid plus
// every group it belongs to), so a dropped process keeps the access its account
// is granted. Falls back to just the primary gid when the lookup is
// unavailable.
func supplementaryGroups(user string, primaryGID int) ([]int, error) {
	if user == "" {
		return []int{primaryGID}, nil
	}
	u, err := osuser.Current()
	if err != nil {
		return nil, fmt.Errorf("lookup user %q: %w", user, err)
	}
	if u.Uid == strconv.Itoa(os.Getuid()) {
		return []int{primaryGID}, nil
	}
	ids, err := u.GroupIds()
	if err != nil {
		return []int{primaryGID}, nil
	}
	out := make([]int, 0, len(ids))
	for _, s := range ids {
		if n, err := strconv.Atoi(s); err == nil {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		out = []int{primaryGID}
	}
	return out, nil
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
	uid, err = repository.ResolveUID(user)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve user %q: %w", user, err)
	}
	if group != "" {
		gid, err = repository.ResolveGID(group)
		if err != nil {
			return 0, 0, fmt.Errorf("resolve group %q: %w", group, err)
		}
		return uid, gid, nil
	}
	gid = uid
	return uid, gid, nil
}

// resolveBinary reports whether the binary exists and is executable, without
// executing it. It mirrors exec.LookPath for bare names.
func resolveBinary(binary string) (string, error) {
	if binary == "" {
		return "", fmt.Errorf("empty binary path")
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", err
	}
	return path, nil
}
