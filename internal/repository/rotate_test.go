package repository

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func itoa(i int) string { return strconv.Itoa(i) }

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

// TestParseBackup covers the numbered-backup name parser, including the
// compressed form and the names it must reject.
func TestParseBackup(t *testing.T) {
	cases := []struct {
		name  string
		index int
		ext   string
		ok    bool
	}{
		{"app.log", 0, "", false},
		{"app.log.1", 1, "", true},
		{"app.log.12", 12, "", true},
		{"app.log.1.gz", 1, ".gz", true},
		{"app.log.0", 0, "", false},
		{"app.log.x", 0, "", false},
		{"app.log.1.bak", 0, "", false},
		{"other.log.1", 0, "", false},
	}
	for _, c := range cases {
		idx, ext, ok := parseBackup("app.log", c.name)
		if ok != c.ok || idx != c.index || ext != c.ext {
			t.Errorf("parseBackup(%q) = (%d,%q,%v), want (%d,%q,%v)", c.name, idx, ext, ok, c.index, c.ext, c.ok)
		}
	}
}

// TestShiftAndRename verifies the active file moves to .1 and existing backups
// shift up, for both compressed and uncompressed forms.
func TestShiftAndRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	write(t, path, "active")
	write(t, path+".1", "one")
	write(t, path+".2", "two")
	write(t, path+".3.gz", "three") // a gap at the compressed .1 is fine

	archive, err := ShiftAndRename(path)
	if err != nil {
		t.Fatalf("ShiftAndRename: %v", err)
	}
	if archive != path+".1" {
		t.Fatalf("archive = %q, want %q", archive, path+".1")
	}
	if got := read(t, path+".1"); got != "active" {
		t.Fatalf(".1 = %q, want active", got)
	}
	if got := read(t, path+".2"); got != "one" {
		t.Fatalf(".2 = %q, want one", got)
	}
	if got := read(t, path+".3"); got != "two" {
		t.Fatalf(".3 = %q, want two", got)
	}
	if got := read(t, path+".4.gz"); got != "three" {
		t.Fatalf(".4.gz = %q, want three", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("active file should be gone, stat err = %v", err)
	}
}

// TestCompressArchiveRoundTrip verifies the archive is gzipped and the
// uncompressed file removed.
func TestCompressArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log.1")
	write(t, path, "hello archive")

	gz, err := CompressArchive(path)
	if err != nil {
		t.Fatalf("CompressArchive: %v", err)
	}
	if gz != path+".gz" {
		t.Fatalf("gz = %q, want %q", gz, path+".gz")
	}
	if got := gunzip(t, gz); got != "hello archive" {
		t.Fatalf("gz content = %q, want hello archive", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("uncompressed should be gone, stat err = %v", err)
	}
}

// TestPruneBackups verifies indexes beyond maxBackups are removed (both forms)
// and that a negative limit keeps everything.
func TestPruneBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	for i := 1; i <= 5; i++ {
		write(t, path+"."+itoa(i), "x")
	}
	write(t, path+".3.gz", "x")

	if err := PruneBackups(path, 2); err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := os.Stat(path + "." + itoa(i)); err != nil {
			t.Fatalf(".%d should exist: %v", i, err)
		}
	}
	for i := 3; i <= 5; i++ {
		if _, err := os.Stat(path + "." + itoa(i)); !os.IsNotExist(err) {
			t.Fatalf(".%d should be pruned, stat err = %v", i, err)
		}
	}
	if _, err := os.Stat(path + ".3.gz"); !os.IsNotExist(err) {
		t.Fatalf(".3.gz should be pruned, stat err = %v", err)
	}

	write(t, path+".9", "x")
	if err := PruneBackups(path, -1); err != nil {
		t.Fatalf("PruneBackups(-1): %v", err)
	}
	if _, err := os.Stat(path + ".9"); err != nil {
		t.Fatalf(".9 should be kept with unlimited backups: %v", err)
	}
}

// TestKeepTail verifies only the trailing bytes remain after KeepTail.
func TestKeepTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.log")
	write(t, path, "0123456789ABCDEF")

	if err := KeepTail(path, 6); err != nil {
		t.Fatalf("KeepTail: %v", err)
	}
	if got := read(t, path); got != "ABCDEF" {
		t.Fatalf("tail = %q, want ABCDEF", got)
	}

	// A file already smaller than the keep size is untouched.
	write(t, path, "short")
	if err := KeepTail(path, 100); err != nil {
		t.Fatalf("KeepTail small: %v", err)
	}
	if got := read(t, path); got != "short" {
		t.Fatalf("small file = %q, want short", got)
	}
}

// TestCopyTruncate verifies the copy+truncate path archives the whole file and
// empties the active one.
func TestCopyTruncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	write(t, path, "0123456789")

	archive, err := CopyTruncate(path, 0)
	if err != nil {
		t.Fatalf("CopyTruncate: %v", err)
	}
	if got := read(t, archive); got != "0123456789" {
		t.Fatalf("archive = %q, want 0123456789", got)
	}
	if got := read(t, path); got != "" {
		t.Fatalf("active = %q, want empty", got)
	}
}

// TestCopyTruncateTail verifies the oversized copy+truncate path copies only
// the trailing bytes and still truncates the active file.
func TestCopyTruncateTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.log")
	write(t, path, "0123456789")

	archive, err := CopyTruncate(path, 4)
	if err != nil {
		t.Fatalf("CopyTruncate tail: %v", err)
	}
	if got := read(t, archive); got != "6789" {
		t.Fatalf("archive = %q, want 6789", got)
	}
	if got := read(t, path); got != "" {
		t.Fatalf("active = %q, want empty", got)
	}
}

// TestCompressArchiveEmpty verifies an empty archive compresses to a valid
// (empty) gzip stream rather than failing.
func TestCompressArchiveEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.log.1")
	write(t, path, "")
	gz, err := CompressArchive(path)
	if err != nil {
		t.Fatalf("CompressArchive empty: %v", err)
	}
	if got := gunzip(t, gz); got != "" {
		t.Fatalf("gz content = %q, want empty", got)
	}
}
