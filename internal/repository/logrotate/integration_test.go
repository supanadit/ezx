package logrotate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/supanadit/ezx/domain"
)

// realTarget adapts an *os.Process to rotator.Target for the integration test.
type realTarget struct{ pid int }

func (t realTarget) PID() int { return t.pid }
func (t realTarget) Signal(s os.Signal) error {
	p, err := os.FindProcess(t.pid)
	if err != nil {
		return err
	}
	return p.Signal(s)
}

// TestRunReopensRealProcess is the end-to-end handshake test: a real /bin/sh
// holds the log file open on fd 3 and traps USR1 to close/reopen it. The
// rotator must rename the file, signal once, and the process must create a
// fresh file at the original path (proving the reopen, not just the rename).
func TestRunReopensRealProcess(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	script := fmt.Sprintf(
		`LOG=%q; reopen(){ exec 3>&-; exec 3>>"$LOG"; }; trap reopen USR1; reopen; `+
			`i=0; while :; do i=$((i+1)); echo "line $i 0123456789012345678901234567890" >&3; sleep 0.01; done`,
		logPath,
	)
	cmd := exec.Command("/bin/sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start writer: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	cfg := domain.LogRotateConfig{
		Files:      []domain.LogRotateFile{{Include: filepath.Join(dir, "*.log"), MaxBytes: 200}},
		Signal:     "USR1",
		MaxBackups: 3,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = New(testLogger{}).Run(ctx, cfg, realTarget{pid: cmd.Process.Pid}) }()

	archive := logPath + ".1"
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(archive)
		return err == nil
	}, "rotated archive %s never appeared", archive)

	// The app must have reopened: the active path exists again and is growing.
	waitFor(t, 5*time.Second, func() bool {
		st, err := os.Stat(logPath)
		return err == nil && st.Size() > 0
	}, "active log %s was not recreated by the app after the reopen signal", logPath)

	// The archive holds the pre-rotation lines and the fresh active file holds
	// lines written after the reopen (i.e. the app is no longer on the old
	// inode).
	archiveContent, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(archiveContent) == 0 {
		t.Fatalf("archive is empty")
	}
	active, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	if len(active) == 0 {
		t.Fatalf("active log is empty after reopen")
	}
}

// waitFor polls cond until it is true or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf(format, args...)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
