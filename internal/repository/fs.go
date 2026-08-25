package repository

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// Symlink creates a symbolic link newpath pointing to target.
func Symlink(target, newpath string) error {
	return os.Symlink(target, newpath)
}

// Realpath resolves symlinks and returns the canonical absolute path.
func Realpath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// TempFile creates a new temporary file with the given pattern in dir
// (or the system temp dir if dir is empty) and returns its path.
func TempFile(dir, pattern string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	return name, nil
}

// TempDir creates a new temporary directory with the given pattern and
// returns its path.
func TempDir(dir, pattern string) (string, error) {
	return os.MkdirTemp(dir, pattern)
}

// Umask sets the process umask and returns the previous umask.
func Umask(mask int) int {
	return syscallUmask(mask)
}

// FSInfo describes a filesystem entry for scripts (a JS-friendly view of
// os.FileInfo). Mode is the permission bits as an integer.
type FSInfo struct {
	Name    string
	Size    int64
	Mode    uint32
	IsDir   bool
	ModTime time.Time
}

// Stat returns info about the file or directory at path.
func Stat(path string) (*FSInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return toFSInfo(info), nil
}

// Exists reports whether the path exists (file or directory).
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Mkdir creates a directory with the given permission bits. It errors if the
// directory already exists.
func Mkdir(path string, perm os.FileMode) error {
	return os.Mkdir(path, perm)
}

// MkdirAll creates a directory and any missing parents with the given bits.
func MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// Chmod changes the permission bits of the path.
func Chmod(path string, perm os.FileMode) error {
	return os.Chmod(path, perm)
}

// Chown changes the ownership of the path from an "user:group" or "user"
// specification.
func Chown(path, owner string) error {
	return chown(path, owner)
}

// ChownRecursive recursively changes the ownership of path (and everything
// beneath it) from an "user:group" or "user" specification.
func ChownRecursive(root, owner string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return chown(path, owner)
	})
}

// ChmodRecursive recursively changes the permission bits of path (and
// everything beneath it).
func ChmodRecursive(root string, perm os.FileMode) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chmod(path, perm)
	})
}

// ReadDir returns the sorted names of entries directly under path.
func ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Glob returns the names of paths matching pattern, sorted.
func Glob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// Remove removes a single file or empty directory.
func Remove(path string) error {
	return os.Remove(path)
}

// RemoveAll removes path and everything beneath it.
func RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// Rename renames (or moves) a path.
func Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

// WriteFile writes data to path, creating missing parent directories first. It
// returns the absolute path. The file is created with perm bits.
func WriteFile(path string, data []byte, perm os.FileMode) (string, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return "", err
	}
	return path, nil
}

// Which returns true if cmd resolves via exec.LookPath (command -v).
func Which(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// ResolveUID resolves a user name (or numeric UID) to a UID.
func ResolveUID(name string) (int, error) {
	return lookupUID(name)
}

// ResolveGID resolves a group name (or numeric GID) to a GID.
func ResolveGID(name string) (int, error) {
	return lookupGID(name)
}

func toFSInfo(info os.FileInfo) *FSInfo {
	return &FSInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    uint32(info.Mode().Perm()),
		IsDir:   info.IsDir(),
		ModTime: info.ModTime(),
	}
}
