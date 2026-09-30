package logrotate

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/rotator"
)

// testLogger is a silent logger.Logger.
type testLogger struct{}

func (testLogger) Debug(string, ...any)      {}
func (testLogger) Info(string, ...any)       {}
func (testLogger) Warn(string, ...any)       {}
func (testLogger) Error(string, ...any)      {}
func (testLogger) Enabled(logger.Level) bool { return false }

// fakeTarget records the signals a rotation pass sends.
type fakeTarget struct {
	pid  int
	mu   sync.Mutex
	sigs []os.Signal
}

func (f *fakeTarget) PID() int { return f.pid }
func (f *fakeTarget) Signal(s os.Signal) error {
	f.mu.Lock()
	f.sigs = append(f.sigs, s)
	f.mu.Unlock()
	return nil
}

func (f *fakeTarget) signals() []os.Signal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]os.Signal(nil), f.sigs...)
}

var _ rotator.Target = (*fakeTarget)(nil)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return string(b)
}

func gunzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open gz %q: %v", path, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader %q: %v", path, err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gz %q: %v", path, err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestRotatePassRenamesAndSignals verifies a file over the threshold is renamed
// to .1 and the configured signal is sent once.
func TestRotatePassRenamesAndSignals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	write(t, path, "0123456789ABCDEFGHIJ") // 20 bytes

	cfg := domain.LogRotateConfig{
		Files:      []domain.LogRotateFile{{Include: filepath.Join(dir, "*.log"), MaxBytes: 10}},
		Signal:     "USR1",
		MaxBackups: 3,
	}
	target := &fakeTarget{pid: 42}
	r := New(testLogger{})
	r.rotatePass(cfg, target)

	if got := read(t, path+".1"); got != "0123456789ABCDEFGHIJ" {
		t.Fatalf(".1 = %q, want the original 20 bytes", got)
	}
	if exists(path) {
		t.Fatalf("active file should have been renamed away")
	}
	if sigs := target.signals(); len(sigs) != 1 || sigs[0] != syscall.SIGUSR1 {
		t.Fatalf("signals = %v, want one SIGUSR1", sigs)
	}
}

// TestRotatePassBelowThresholdNoop verifies files under the threshold are left
// alone and no signal is sent.
func TestRotatePassBelowThresholdNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	write(t, path, "small")

	cfg := domain.LogRotateConfig{
		Files:  []domain.LogRotateFile{{Include: filepath.Join(dir, "*.log"), MaxBytes: 10}},
		Signal: "USR1",
	}
	target := &fakeTarget{pid: 42}
	New(testLogger{}).rotatePass(cfg, target)

	if got := read(t, path); got != "small" {
		t.Fatalf("file = %q, want untouched", got)
	}
	if exists(path + ".1") {
		t.Fatalf(".1 should not exist")
	}
	if sigs := target.signals(); len(sigs) != 0 {
		t.Fatalf("signals = %v, want none", sigs)
	}
}

// TestRotatePassSignalOnceAcrossFiles verifies several files sharing a signal
// produce exactly one signal for the pass.
func TestRotatePassSignalOnceAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "access.log"), "0123456789ABCDEF")
	write(t, filepath.Join(dir, "error.log"), "0123456789ABCDEF")

	cfg := domain.LogRotateConfig{
		Files:      []domain.LogRotateFile{{Include: filepath.Join(dir, "*.log"), MaxBytes: 5}},
		Signal:     "USR1",
		MaxBackups: 2,
	}
	target := &fakeTarget{pid: 7}
	New(testLogger{}).rotatePass(cfg, target)

	if sigs := target.signals(); len(sigs) != 1 {
		t.Fatalf("signals = %v, want exactly one for the pass", sigs)
	}
	for _, name := range []string{"access.log", "error.log"} {
		if !exists(filepath.Join(dir, name+".1")) {
			t.Fatalf("%s.1 should exist", name)
		}
	}
}

// TestRotatePassCompress verifies compression is applied after the rotation.
func TestRotatePassCompress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	write(t, path, "compress me")

	cfg := domain.LogRotateConfig{
		Files:    []domain.LogRotateFile{{Include: path, MaxBytes: 4}},
		Signal:   "USR1",
		Compress: true,
	}
	New(testLogger{}).rotatePass(cfg, &fakeTarget{pid: 9})

	if got := gunzip(t, path+".1.gz"); got != "compress me" {
		t.Fatalf(".1.gz = %q, want compress me", got)
	}
	if exists(path + ".1") {
		t.Fatalf("uncompressed .1 should be gone after compression")
	}
}

// TestRotatePassExcludeAndDedupe verifies exclude patterns are honoured and a
// file matched by two patterns is rotated once.
func TestRotatePassExcludeAndDedupe(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "app.log.1.gz")
	write(t, archive, "already an archive")

	cfg := domain.LogRotateConfig{
		Files: []domain.LogRotateFile{
			{Include: filepath.Join(dir, "*.log*"), MaxBytes: 1},
			{Include: filepath.Join(dir, "*.log*"), MaxBytes: 1},
		},
		Exclude: []string{"*.gz"},
		Signal:  "USR1",
	}
	New(testLogger{}).rotatePass(cfg, &fakeTarget{pid: 3})

	if got := read(t, archive); got != "already an archive" {
		t.Fatalf("excluded archive changed: %q", got)
	}
	if exists(filepath.Join(dir, "app.log.1.1")) {
		t.Fatalf("excluded archive was rotated")
	}
}

// TestRotatePassPrune verifies backups beyond maxBackups are dropped.
func TestRotatePassPrune(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	write(t, path, "1234567890")
	write(t, path+".1", "old")
	write(t, path+".2", "older")

	cfg := domain.LogRotateConfig{
		Files:  []domain.LogRotateFile{{Include: path, MaxBytes: 5, MaxBackups: 1}},
		Signal: "USR1",
	}
	New(testLogger{}).rotatePass(cfg, &fakeTarget{pid: 3})

	if !exists(path + ".1") {
		t.Fatalf(".1 should exist")
	}
	if exists(path + ".2") {
		t.Fatalf(".2 should be pruned")
	}
}

// TestRotatePassOversizedKeepsTail verifies a legacy file far over the
// oversized threshold keeps only its tail and never reads the head.
func TestRotatePassOversizedKeepsTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.log")
	write(t, path, "HEAD-0123456789") // 15 bytes

	cfg := domain.LogRotateConfig{
		Files:  []domain.LogRotateFile{{Include: path}},
		Signal: "USR1",
		Oversized: &domain.OversizedConfig{
			MaxBytes:      8,
			KeepTailBytes: 5,
			Compress:      true,
		},
	}
	New(testLogger{}).rotatePass(cfg, &fakeTarget{pid: 3})

	if got := gunzip(t, path+".1.gz"); got != "56789" {
		t.Fatalf("oversized tail = %q, want last 5 bytes 56789", got)
	}
}

// TestRotatePassCopyTruncate verifies the no-signal copy+truncate fallback.
func TestRotatePassCopyTruncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	write(t, path, "0123456789")

	cfg := domain.LogRotateConfig{
		Files:  []domain.LogRotateFile{{Include: path, MaxBytes: 4}},
		Reopen: domain.ReopenCopyTruncate,
	}
	target := &fakeTarget{pid: 3}
	New(testLogger{}).rotatePass(cfg, target)

	if got := read(t, path+".1"); got != "0123456789" {
		t.Fatalf(".1 = %q, want the copied content", got)
	}
	if got := read(t, path); got != "" {
		t.Fatalf("active = %q, want truncated empty", got)
	}
	if sigs := target.signals(); len(sigs) != 0 {
		t.Fatalf("copytruncate should not signal, got %v", sigs)
	}
}

// TestRotatePassSkipsWithoutTarget verifies no file is touched when the reopen
// target cannot be resolved.
func TestRotatePassSkipsWithoutTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	write(t, path, "0123456789")

	cfg := domain.LogRotateConfig{
		Files:  []domain.LogRotateFile{{Include: path, MaxBytes: 4}},
		Signal: "USR1",
	}
	New(testLogger{}).rotatePass(cfg, &fakeTarget{pid: 0})

	if got := read(t, path); got != "0123456789" {
		t.Fatalf("file = %q, want untouched when no target", got)
	}
	if exists(path + ".1") {
		t.Fatalf(".1 should not exist when no target")
	}
}

// TestEffectiveResolutions verifies per-file overrides fall back to group
// values and defaults.
func TestEffectiveResolutions(t *testing.T) {
	group := domain.LogRotateConfig{MaxBytes: 100, MaxBackups: 5, Signal: "USR1"}
	override := domain.LogRotateFile{MaxBytes: 10, MaxBackups: 2, Signal: "HUP"}

	if got := effectiveMaxBytes(group, domain.LogRotateFile{}); got != 100 {
		t.Errorf("maxBytes group = %d, want 100", got)
	}
	if got := effectiveMaxBytes(group, override); got != 10 {
		t.Errorf("maxBytes override = %d, want 10", got)
	}
	if got := effectiveMaxBackups(group, override); got != 2 {
		t.Errorf("maxBackups override = %d, want 2", got)
	}
	if got := effectiveSignal(group, override); got != "HUP" {
		t.Errorf("signal override = %q, want HUP", got)
	}
	if got := effectiveMaxBytes(domain.LogRotateConfig{}, domain.LogRotateFile{}); got != defaultMaxBytes {
		t.Errorf("maxBytes default = %d, want %d", got, defaultMaxBytes)
	}
	if got := effectiveMaxBackups(domain.LogRotateConfig{}, domain.LogRotateFile{}); got != defaultMaxBackups {
		t.Errorf("maxBackups default = %d, want %d", got, defaultMaxBackups)
	}
}

// TestRunWatchesCreatedDirectory verifies the watcher recovers when the log
// directory does not exist when Run starts: it watches the nearest existing
// ancestor and switches to the real directory once it is created.
func TestRunWatchesCreatedDirectory(t *testing.T) {
	old := rotateThrottle
	rotateThrottle = 20 * time.Millisecond
	defer func() { rotateThrottle = old }()

	parent := t.TempDir()
	dir := filepath.Join(parent, "logs") // does not exist yet

	cfg := domain.LogRotateConfig{
		Files:  []domain.LogRotateFile{{Include: filepath.Join(dir, "*.log"), MaxBytes: 10}},
		Signal: "USR1",
	}
	target := &fakeTarget{pid: 11}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(testLogger{}).Run(ctx, cfg, target) }()

	time.Sleep(150 * time.Millisecond)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.log"), []byte("0123456789012345678901234567890"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	archive := filepath.Join(dir, "app.log.1")
	deadline := time.Now().Add(3 * time.Second)
	for !exists(archive) {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("rotation did not happen after the log directory was created")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

// TestRunWatchesAndRotates verifies the fsnotify watcher path end to end: a
// write that pushes a file past the threshold triggers a rotation without a
// schedule.
func TestRunWatchesAndRotates(t *testing.T) {
	old := rotateThrottle
	rotateThrottle = 20 * time.Millisecond
	defer func() { rotateThrottle = old }()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	write(t, path, "seed") // under the threshold

	cfg := domain.LogRotateConfig{
		Files:  []domain.LogRotateFile{{Include: filepath.Join(dir, "*.log"), MaxBytes: 20}},
		Signal: "USR1",
	}
	target := &fakeTarget{pid: 99}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(testLogger{}).Run(ctx, cfg, target) }()

	// Wait for the watcher to install its watch, then push the file over.
	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(path, []byte("0123456789012345678901234567890"), 0o644); err != nil {
		t.Fatalf("write over threshold: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for !exists(path + ".1") {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("rotation did not happen: %s missing", path+".1")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if sigs := target.signals(); len(sigs) == 0 {
		t.Fatalf("expected a reopen signal after rotation")
	}
}
