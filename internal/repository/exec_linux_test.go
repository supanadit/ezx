//go:build linux

package repository

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/supanadit/ezx/domain"
)

func TestBuildProcessEnv_FilterAndAppend(t *testing.T) {
	parent := []string{"KEEP=1", "DROP=2", "OVERRIDE=old"}
	p := domain.Process{
		FilterEnv:   []string{"DROP"},
		Environment: []string{"OVERRIDE=new", "ADDED=3"},
	}
	got, err := BuildProcessEnv(parent, p)
	if err != nil {
		t.Fatalf("BuildProcessEnv: %v", err)
	}
	// Filter removes only DROP; the additive OVERRIDE=new is appended after
	// the inherited OVERRIDE=old (later entries win in exec semantics).
	want := []string{"KEEP=1", "OVERRIDE=old", "OVERRIDE=new", "ADDED=3"}
	if len(got) != len(want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("env[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestBuildProcessEnv_NoFilter(t *testing.T) {
	parent := []string{"A=1", "B=2"}
	p := domain.Process{Environment: []string{"C=3"}}
	got, err := BuildProcessEnv(parent, p)
	if err != nil {
		t.Fatalf("BuildProcessEnv: %v", err)
	}
	want := []string{"A=1", "B=2", "C=3"}
	if len(got) != len(want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
}

func TestApplyCredential_NoUserNoop(t *testing.T) {
	cmd := &exec.Cmd{}
	if err := ApplyCredential(cmd, domain.Process{}); err != nil {
		t.Fatalf("ApplyCredential with no user: %v", err)
	}
	if cmd.SysProcAttr != nil {
		t.Fatalf("SysProcAttr should be nil when no user, got %+v", cmd.SysProcAttr)
	}
}

func TestApplyCredential_NumericUser(t *testing.T) {
	// Numeric UID/GID resolve without touching /etc/passwd or /etc/group.
	cmd := &exec.Cmd{}
	p := domain.Process{User: "1000", Group: "1000"}
	if err := ApplyCredential(cmd, p); err != nil {
		t.Fatalf("ApplyCredential: %v", err)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatalf("expected SysProcAttr.Credential, got %+v", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.Credential.Uid != 1000 || cmd.SysProcAttr.Credential.Gid != 1000 {
		t.Fatalf("credential = %+v, want uid/gid 1000", cmd.SysProcAttr.Credential)
	}
}

func TestApplyCredential_UserOnlyDefaultsGidToUid(t *testing.T) {
	cmd := &exec.Cmd{}
	p := domain.Process{User: "1000"}
	if err := ApplyCredential(cmd, p); err != nil {
		t.Fatalf("ApplyCredential: %v", err)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatalf("expected SysProcAttr.Credential, got %+v", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.Credential.Uid != 1000 || cmd.SysProcAttr.Credential.Gid != 1000 {
		t.Fatalf("credential = %+v, want uid=gid=1000", cmd.SysProcAttr.Credential)
	}
}

func TestSetProcessGroupLeader(t *testing.T) {
	cmd := &exec.Cmd{}
	SetProcessGroupLeader(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("expected Setpgid=true, got %+v", cmd.SysProcAttr)
	}
	// Idempotent: preserves existing SysProcAttr.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: false}
	SetProcessGroupLeader(cmd)
	if !cmd.SysProcAttr.Setpgid {
		t.Fatalf("Setpgid should be true after second call")
	}
}

func TestResolveCred_Numeric(t *testing.T) {
	uid, gid, err := resolveCred("1000", "1000")
	if err != nil || uid != 1000 || gid != 1000 {
		t.Fatalf("resolveCred(1000,1000) = %d,%d,%v; want 1000,1000,nil", uid, gid, err)
	}
	// Group omitted -> gid defaults to uid.
	uid, gid, err = resolveCred("1000", "")
	if err != nil || uid != 1000 || gid != 1000 {
		t.Fatalf("resolveCred(1000,\"\") = %d,%d,%v; want 1000,1000,nil", uid, gid, err)
	}
}
