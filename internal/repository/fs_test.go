package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFSStatExistsMkdirChmod(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "file.txt")

	if Exists(target) {
		t.Fatalf("Exists(%q) = true before creation", target)
	}
	if err := MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if !Exists(filepath.Dir(target)) {
		t.Fatalf("Exists(%q) = false after MkdirAll", filepath.Dir(target))
	}
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	info, err := Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name != "file.txt" {
		t.Fatalf("Name = %q, want file.txt", info.Name)
	}
	if info.IsDir {
		t.Fatalf("IsDir = true, want false")
	}
	if info.Size != 5 {
		t.Fatalf("Size = %d, want 5", info.Size)
	}

	if err := Chmod(target, 0o600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	info, _ = Stat(target)
	if info.Mode != 0o600 {
		t.Fatalf("Mode = %o, want 600", info.Mode)
	}

	if err := Mkdir(filepath.Join(dir, "newdir"), 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := Mkdir(filepath.Join(dir, "newdir"), 0o700); err == nil {
		t.Fatalf("Mkdir on existing dir should error")
	}
}

func TestFSReadDirSortedGlobRemove(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.txt", "a.txt", "c.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}

	names, err := ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	want := []string{"a.txt", "b.txt", "c.log"}
	if len(names) != len(want) {
		t.Fatalf("ReadDir = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("ReadDir[%d] = %q, want %q (not sorted)", i, names[i], want[i])
		}
	}

	globs, err := Glob(filepath.Join(dir, "*.txt"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(globs) != 2 {
		t.Fatalf("Glob *.txt = %v, want 2", globs)
	}

	if err := Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if Exists(filepath.Join(dir, "a.txt")) {
		t.Fatalf("a.txt still exists after Remove")
	}
}

func TestFSRename(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Rename(a, b); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if !Exists(b) {
		t.Fatalf("b does not exist after Rename")
	}
}

func TestChownRecursive(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root to change ownership")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := ChownRecursive(dir, "0:0"); err != nil {
		t.Fatalf("ChownRecursive: %v", err)
	}
}
