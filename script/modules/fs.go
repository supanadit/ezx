package scriptmodules

import (
	"os"

	"github.com/supanadit/ezx/internal/repository"
)

// FSModule exposes ezx.fs: filesystem helpers for scripts. It enables the
// container-entrypoint patterns the official docker entrypoints rely on —
// first-run detection (PG_VERSION exists), directory permission setup
// (chmod/chown -R), and init-script enumeration (readDir/glob).
type FSModule struct{}

// NewFSModule returns an FSModule.
func NewFSModule() *FSModule {
	return &FSModule{}
}

// Stat returns info about the file or directory at path.
func (m *FSModule) Stat(path string) (*repository.FSInfo, error) {
	return repository.Stat(path)
}

// Exists reports whether the path exists (file or directory).
func (m *FSModule) Exists(path string) bool {
	return repository.Exists(path)
}

// Mkdir creates a directory with the given permission bits (e.g. 0o700).
func (m *FSModule) Mkdir(path string, perm uint32) error {
	return repository.Mkdir(path, os.FileMode(perm))
}

// MkdirAll creates a directory and any missing parents with the given bits.
func (m *FSModule) MkdirAll(path string, perm uint32) error {
	return repository.MkdirAll(path, os.FileMode(perm))
}

// Chmod changes the permission bits of a path (e.g. 0o700).
func (m *FSModule) Chmod(path string, perm uint32) error {
	return repository.Chmod(path, os.FileMode(perm))
}

// ChmodRecursive recursively changes permission bits beneath path.
func (m *FSModule) ChmodRecursive(path string, perm uint32) error {
	return repository.ChmodRecursive(path, os.FileMode(perm))
}

// Chown changes the ownership of a path from "user:group" or "user".
func (m *FSModule) Chown(path, owner string) error {
	return repository.Chown(path, owner)
}

// ChownRecursive recursively changes ownership beneath path.
func (m *FSModule) ChownRecursive(path, owner string) error {
	return repository.ChownRecursive(path, owner)
}

// ReadDir returns the sorted names of entries directly under path.
func (m *FSModule) ReadDir(path string) ([]string, error) {
	return repository.ReadDir(path)
}

// Glob returns the names of paths matching pattern, sorted.
func (m *FSModule) Glob(pattern string) ([]string, error) {
	return repository.Glob(pattern)
}

// Remove removes a single file or empty directory.
func (m *FSModule) Remove(path string) error {
	return repository.Remove(path)
}

// RemoveAll removes path and everything beneath it.
func (m *FSModule) RemoveAll(path string) error {
	return repository.RemoveAll(path)
}

// Symlink creates a symbolic link named newpath pointing to target.
func (m *FSModule) Symlink(target, newpath string) error {
	return repository.Symlink(target, newpath)
}

// Realpath resolves symlinks and returns the canonical absolute path.
func (m *FSModule) Realpath(path string) (string, error) {
	return repository.Realpath(path)
}

// TempFile creates a temporary file with the given pattern (dir optional).
func (m *FSModule) TempFile(dir, pattern string) (string, error) {
	return repository.TempFile(dir, pattern)
}

// TempDir creates a temporary directory with the given pattern (dir optional).
func (m *FSModule) TempDir(dir, pattern string) (string, error) {
	return repository.TempDir(dir, pattern)
}

// Umask sets the process umask and returns the previous umask.
func (m *FSModule) Umask(mask int) int {
	return repository.Umask(mask)
}

// Rename renames (or moves) a path.
func (m *FSModule) Rename(oldPath, newPath string) error {
	return repository.Rename(oldPath, newPath)
}
