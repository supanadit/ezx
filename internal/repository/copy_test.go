package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "sub", "dst.txt")
	if err := os.WriteFile(src, []byte("hello copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Copy(src, dst, CopyOptions{}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello copy" {
		t.Fatalf("content = %q", data)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCopyTreePreservesStructureAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(filepath.Join(src, "a/b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a/f1.txt"), []byte("1"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a/b/f2.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink recreated by default (cp -a behavior).
	if err := os.Symlink("f1.txt", filepath.Join(src, "a/ln")); err != nil {
		t.Fatal(err)
	}

	if err := CopyTree(src, dst, CopyOptions{}); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	for _, p := range []string{"a/f1.txt", "a/b/f2.txt", "a/ln"} {
		if _, err := os.Lstat(filepath.Join(dst, p)); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
	linkInfo, err := os.Lstat(filepath.Join(dst, "a/ln"))
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected recreated symlink, got regular file")
	}
	target, err := os.Readlink(filepath.Join(dst, "a/ln"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "f1.txt" {
		t.Fatalf("symlink target = %q", target)
	}
	fi, err := os.Stat(filepath.Join(dst, "a/f1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", fi.Mode().Perm())
	}
}

func TestCopyTreeDereference(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(src, "ln")); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(src, dst, CopyOptions{Dereference: true}); err != nil {
		t.Fatalf("CopyTree(dereference): %v", err)
	}
	info, err := os.Lstat(filepath.Join(dst, "ln"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected dereferenced regular file")
	}
	data, err := os.ReadFile(filepath.Join(dst, "ln"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data" {
		t.Fatalf("content = %q", data)
	}
}

func TestCopyOverwriteFalseSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Copy(src, dst, CopyOptions{Overwrite: false}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "old" {
		t.Fatalf("existing dest overwritten: %q", data)
	}
}