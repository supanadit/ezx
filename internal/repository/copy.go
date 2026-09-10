package repository

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// CopyOptions controls fs.Copy semantics (a cp -a/-r replacement).
type CopyOptions struct {
	// Overwrite, when true (default), replaces existing dest entries. When
	// false, existing dest files/dirs are left untouched.
	Overwrite bool
	// Dereference, when true, copies the targets of symlinks instead of
	// recreating the symlinks themselves.
	Dereference bool
	// PreserveOwner, when true, copies uid/gid from the source entries. Only
	// meaningful when running as root; best-effort otherwise.
	PreserveOwner bool
}

// Copy copies src (a file or directory) to dest. Directories are copied
// recursively with modes preserved (cp -a style: -dR --preserve=mode,times).
// Symlinks are recreated by default; pass Dereference to follow them.
func Copy(src, dest string, opts CopyOptions) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("copy: stat %q: %w", src, err)
	}
	if info.IsDir() {
		return copyTree(src, dest, info, opts)
	}
	if err := copyEntry(src, dest, info, opts); err != nil {
		return fmt.Errorf("copy %q: %w", src, err)
	}
	return nil
}

// CopyTree copies the directory src into dest recursively, creating dest if
// needed. Equivalent to Copy for a directory source; kept as a distinct name
// for script callers that want to be explicit about recursive semantics.
func CopyTree(src, dest string, opts CopyOptions) error {
	info, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("copy: stat %q: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("copy: source %q is not a directory", src)
	}
	if err := copyTree(src, dest, info, opts); err != nil {
		return fmt.Errorf("copy %q: %w", src, err)
	}
	return nil
}

// copyTree walks src and reproduces its structure under dest, preserving
// directory modes, file modes, and modtimes.
func copyTree(src, dest string, info os.FileInfo, opts CopyOptions) error {
	if !opts.Overwrite {
		if _, err := os.Lstat(dest); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(dest, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chmod(dest, info.Mode().Perm()); err != nil {
		return err
	}
	if opts.PreserveOwner {
		_ = copyOwner(src, dest, info)
	}
	if err := os.Chtimes(dest, info.ModTime(), info.ModTime()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		// Guard against copying dest into itself (source nested in dest).
		if strings.HasPrefix(dest, filepath.Join(src, e.Name())) {
			continue
		}
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dest, e.Name())
		child, err := os.Lstat(from)
		if err != nil {
			return err
		}
		if err := copyEntry(from, to, child, opts); err != nil {
			return err
		}
	}
	return nil
}

// copyEntry copies a single filesystem entry (file, dir, or symlink).
func copyEntry(src, dest string, info os.FileInfo, opts CopyOptions) error {
	if info.IsDir() {
		return copyTree(src, dest, info, opts)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if !opts.Dereference {
			return copySymlink(target, dest, src, opts)
		}
		// Follow the symlink: resolve its target and copy that.
		resolved := target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(src), resolved)
		}
		tgtInfo, err := os.Stat(resolved)
		if err != nil {
			return err
		}
		if tgtInfo.IsDir() {
			return copyTree(resolved, dest, tgtInfo, opts)
		}
		return copyRegular(resolved, dest, tgtInfo, opts)
	}
	if info.Mode().IsRegular() {
		return copyRegular(src, dest, info, opts)
	}
	// Other node types (fifo, socket, device): recreate with the same mode
	// where the OS permits, best-effort.
	return copySpecial(src, dest, info, opts)
}

// copySymlink recreates a symlink at dest pointing to the same target.
func copySymlink(target, dest, src string, opts CopyOptions) error {
	if !opts.Overwrite {
		if _, err := os.Lstat(dest); err == nil {
			return nil
		}
	}
	if err := removeExisting(dest); err != nil {
		return err
	}
	return os.Symlink(target, dest)
}

// copyRegular copies a regular file, preserving mode and modtime.
func copyRegular(src, dest string, info os.FileInfo, opts CopyOptions) error {
	if !opts.Overwrite {
		if _, err := os.Lstat(dest); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := removeExisting(dest); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(dest, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chtimes(dest, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	if opts.PreserveOwner {
		_ = copyOwner(src, dest, info)
	}
	return nil
}

// copySpecial recreates device/fifo/socket nodes when running as root;
// best-effort (returns nil on failure).
func copySpecial(src, dest string, info os.FileInfo, opts CopyOptions) error {
	if !opts.Overwrite {
		if _, err := os.Lstat(dest); err == nil {
			return nil
		}
	}
	if err := removeExisting(dest); err != nil {
		return err
	}
	return replicateMode(info, dest)
}

// copyOwner copies uid/gid from a source entry to dest (best-effort).
func copyOwner(src, dest string, info os.FileInfo) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return os.Chown(dest, int(st.Uid), int(st.Gid))
	}
	return nil
}

// removeExisting removes an existing dest entry so it can be replaced. A
// regular file is removed; a directory is left in place (copyTree merges).
func removeExisting(dest string) error {
	info, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return nil
	}
	return os.Remove(dest)
}

// replicateMode recreates an entry's permissions via chmod.
func replicateMode(info os.FileInfo, path string) error {
	if err := os.Chmod(path, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(path, info.ModTime(), info.ModTime())
}