package system

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeAll writes p to w, failing the test on error.
func writeAll(t *testing.T, w *rotatingFileWriter, p string) {
	t.Helper()
	if _, err := w.Write([]byte(p)); err != nil {
		t.Fatalf("Write(%q): %v", p, err)
	}
}

// readFile reads path, failing the test on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(b)
}

// TestRotatingWriterCreatesFileAndParentDirs verifies a write creates the file
// and any missing parent directories.
func TestRotatingWriterCreatesFileAndParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "app.log")
	w, err := newRotatingFileWriter(path, 100, 3)
	if err != nil {
		t.Fatalf("newRotatingFileWriter: %v", err)
	}
	defer w.Close()

	writeAll(t, w, "hello")
	if got := readFile(t, path); got != "hello" {
		t.Fatalf("file content = %q, want %q", got, "hello")
	}
}

// TestRotatingWriterRotationTriggersAtMaxBytes verifies rotation happens when a
// write would push the active file past maxBytes.
func TestRotatingWriterRotationTriggersAtMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := newRotatingFileWriter(path, 100, 3)
	if err != nil {
		t.Fatalf("newRotatingFileWriter: %v", err)
	}
	defer w.Close()

	// 60 bytes fits under 100.
	writeAll(t, w, strings.Repeat("a", 60))
	// 60 more would push to 120 > 100 → rotate; the 60 bytes land in a fresh file.
	writeAll(t, w, strings.Repeat("b", 60))

	if got := readFile(t, path); got != strings.Repeat("b", 60) {
		t.Fatalf("active file = %d bytes, want 60 (fresh after rotation)", len(got))
	}
	if got := readFile(t, path+".1"); got != strings.Repeat("a", 60) {
		t.Fatalf("backup .1 = %d bytes, want 60 (old active)", len(got))
	}
}

// TestRotatingWriterBackupShiftOrder verifies backups shift down on successive
// rotations: newest in .1, oldest in .N.
func TestRotatingWriterBackupShiftOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := newRotatingFileWriter(path, 10, 3)
	if err != nil {
		t.Fatalf("newRotatingFileWriter: %v", err)
	}
	defer w.Close()

	// Each 10-byte write fills the file exactly; the next write rotates.
	writeAll(t, w, "0000000000") // active: 10 bytes
	writeAll(t, w, "1111111111") // rotate → .1=0000, active=1111
	writeAll(t, w, "2222222222") // rotate → .2=0000, .1=1111, active=2222
	writeAll(t, w, "3333333333") // rotate → .3=0000, .2=1111, .1=2222, active=3333

	if got := readFile(t, path); got != "3333333333" {
		t.Fatalf("active = %q, want 3333333333", got)
	}
	if got := readFile(t, path+".1"); got != "2222222222" {
		t.Fatalf(".1 = %q, want 2222222222", got)
	}
	if got := readFile(t, path+".2"); got != "1111111111" {
		t.Fatalf(".2 = %q, want 1111111111", got)
	}
	if got := readFile(t, path+".3"); got != "0000000000" {
		t.Fatalf(".3 = %q, want 0000000000", got)
	}
}

// TestRotatingWriterDropsBackupsBeyondMax verifies backups beyond maxBackups are
// dropped (oldest removed).
func TestRotatingWriterDropsBackupsBeyondMax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := newRotatingFileWriter(path, 10, 2)
	if err != nil {
		t.Fatalf("newRotatingFileWriter: %v", err)
	}
	defer w.Close()

	writeAll(t, w, "0000000000")
	writeAll(t, w, "1111111111") // .1=0000
	writeAll(t, w, "2222222222") // .2=0000, .1=1111
	writeAll(t, w, "3333333333") // drop .2, .2=1111, .1=2222, active=3333

	if got := readFile(t, path); got != "3333333333" {
		t.Fatalf("active = %q, want 3333333333", got)
	}
	if got := readFile(t, path+".1"); got != "2222222222" {
		t.Fatalf(".1 = %q, want 2222222222", got)
	}
	if got := readFile(t, path+".2"); got != "1111111111" {
		t.Fatalf(".2 = %q, want 1111111111", got)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf(".3 should not exist, stat err = %v", err)
	}
}

// TestRotatingWriterUnlimitedBackups verifies maxBackups < 0 keeps every backup.
func TestRotatingWriterUnlimitedBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := newRotatingFileWriter(path, 10, -1)
	if err != nil {
		t.Fatalf("newRotatingFileWriter: %v", err)
	}
	defer w.Close()

	writeAll(t, w, "0000000000")
	writeAll(t, w, "1111111111")
	writeAll(t, w, "2222222222")
	writeAll(t, w, "3333333333")
	writeAll(t, w, "4444444444")

	if got := readFile(t, path); got != "4444444444" {
		t.Fatalf("active = %q, want 4444444444", got)
	}
	if got := readFile(t, path+".1"); got != "3333333333" {
		t.Fatalf(".1 = %q, want 3333333333", got)
	}
	if got := readFile(t, path+".4"); got != "0000000000" {
		t.Fatalf(".4 = %q, want 0000000000", got)
	}
}

// TestRotatingWriterAppendOnReopen verifies a reopened (rotated) file appends to
// prior content rather than truncating.
func TestRotatingWriterAppendOnReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := newRotatingFileWriter(path, 10, 3)
	if err != nil {
		t.Fatalf("newRotatingFileWriter: %v", err)
	}
	defer w.Close()

	writeAll(t, w, "0000000000") // active: 10 bytes
	writeAll(t, w, "1111111111") // rotate → .1=0000, active=1111
	writeAll(t, w, "2222222222") // rotate → .2=0000, .1=1111, active=2222

	// Reopen a fresh writer in append mode (simulates a restart).
	w2, err := newRotatingFileWriter(path, 10, 3)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer w2.Close()
	writeAll(t, w2, "3333333333") // appends to active=2222 → 20 bytes → rotate

	if got := readFile(t, path); got != "3333333333" {
		t.Fatalf("active after reopen = %q, want 3333333333", got)
	}
	// The reopened writer seeded size from the existing 10-byte file, so the
	// first write rotated: .1 = 2222 (prior active), .2 = 1111, .3 = 0000.
	if got := readFile(t, path+".1"); got != "2222222222" {
		t.Fatalf(".1 = %q, want 2222222222", got)
	}
}

// TestRotatingWriterLargeSingleWriteDoesNotSplit verifies a single write larger
// than maxBytes is written whole to a fresh file (no split marker, no loop).
func TestRotatingWriterLargeSingleWriteDoesNotSplit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := newRotatingFileWriter(path, 10, 3)
	if err != nil {
		t.Fatalf("newRotatingFileWriter: %v", err)
	}
	defer w.Close()

	writeAll(t, w, "0123456789") // active: 10 bytes
	big := strings.Repeat("x", 100)
	writeAll(t, w, big) // oversized write → rotate, then write whole to fresh file

	if got := readFile(t, path); got != big {
		t.Fatalf("active file = %d bytes, want %d (whole write, not split)", len(got), len(big))
	}
	// The prior 10 bytes rotated to .1.
	if got := readFile(t, path+".1"); got != "0123456789" {
		t.Fatalf(".1 = %q, want 0123456789", got)
	}
}

// TestRotatingWriterConcurrentWriteClose verifies Write and Close can race
// without deadlock or panic (run with -race).
func TestRotatingWriterConcurrentWriteClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	w, err := newRotatingFileWriter(path, 10, 3)
	if err != nil {
		t.Fatalf("newRotatingFileWriter: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_, _ = w.Write([]byte("abcdefghij"))
		}
	}()
	go func() {
		defer wg.Done()
		_ = w.Close()
	}()
	wg.Wait()
}
