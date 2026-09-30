package repository

// This file implements numbered log-archive rotation (the file.1 / file.2 /
// file.N.gz form) used by both the stream rotator (log destinations ezx owns)
// and the app-owned file rotator. It is deliberately split into the steps a
// caller can order differently: shift+rename, signal, then compress and prune —
// so a reopen signal lands between the rename and the compression and new bytes
// cannot be archived.

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// backupExts are the archive extensions probed when shifting/pruning numbered
// backups. Uncompressed archives are file.N; compressed ones are file.N.gz.
var backupExts = []string{"", ".gz"}

// parseBackup reports whether name is a numbered backup of base (base.N or
// base.N.gz) and returns its index and extension. The active file (base) and
// non-numeric suffixes do not match.
func parseBackup(base, name string) (index int, ext string, ok bool) {
	if name == base {
		return 0, "", false
	}
	rest, found := strings.CutPrefix(name, base+".")
	if !found {
		return 0, "", false
	}
	for _, e := range backupExts {
		if e != "" && strings.HasSuffix(rest, e) {
			rest = strings.TrimSuffix(rest, e)
			ext = e
			break
		}
	}
	if rest == "" {
		return 0, "", false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return 0, "", false
		}
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, "", false
	}
	return n, ext, true
}

// highestBackup returns the largest N such that path.N or path.N.gz exists, or
// 0 when there are none. It scans the directory rather than probing upward so a
// gap (e.g. a manually removed file.2) does not hide higher backups.
func highestBackup(path string) int {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	highest := 0
	for _, e := range entries {
		if n, _, ok := parseBackup(base, e.Name()); ok && n > highest {
			highest = n
		}
	}
	return highest
}

// shiftBackups renames path.i and path.i.gz to path.(i+1) and path.(i+1).gz for
// i from highest down to 1, so the indexes free up for the new archive.
func shiftBackups(path string, highest int) error {
	for i := highest; i >= 1; i-- {
		for _, ext := range backupExts {
			from := fmt.Sprintf("%s.%d%s", path, i, ext)
			if _, err := os.Stat(from); err != nil {
				continue
			}
			to := fmt.Sprintf("%s.%d%s", path, i+1, ext)
			if err := os.Rename(from, to); err != nil {
				return fmt.Errorf("shift backup %q -> %q: %w", from, to, err)
			}
		}
	}
	return nil
}

// ShiftAndRename moves the active file at path to its next numbered archive
// (path.1), first shifting every existing path.N / path.N.gz up by one. It is
// the "rename" half of a signal-driven rotation: the caller sends the reopen
// signal, then calls CompressArchive/PruneBackups. Returns the archive path
// (always the uncompressed path.1).
func ShiftAndRename(path string) (string, error) {
	if err := shiftBackups(path, highestBackup(path)); err != nil {
		return "", err
	}
	archive := path + ".1"
	if err := os.Rename(path, archive); err != nil {
		return "", fmt.Errorf("rename active log %q -> %q: %w", path, archive, err)
	}
	return archive, nil
}

// CopyTruncate is the lossy "copytruncate" half of a rotation: it copies the
// active file to path.1 (shifting backups first) and then truncates the active
// file to zero. When keepTailBytes > 0 only that many trailing bytes are
// copied, so a legacy oversized file is never read in full. The caller follows
// with CompressArchive/PruneBackups. Returns the archive path.
func CopyTruncate(path string, keepTailBytes int64) (string, error) {
	if err := shiftBackups(path, highestBackup(path)); err != nil {
		return "", err
	}
	archive := path + ".1"
	if err := copyActive(path, archive, keepTailBytes); err != nil {
		return "", err
	}
	if err := os.Truncate(path, 0); err != nil {
		return "", fmt.Errorf("truncate active log %q: %w", path, err)
	}
	return archive, nil
}

// KeepTail reduces path to its last keepTailBytes bytes, in place. It is the
// oversized-file escape hatch: the head (which for a sparse legacy file is
// mostly holes) is never read, only discarded. It is a no-op when the file is
// already small enough.
func KeepTail(path string, keepTailBytes int64) error {
	if keepTailBytes <= 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %q for tail: %w", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %q for tail: %w", path, err)
	}
	size := st.Size()
	if size <= keepTailBytes {
		return nil
	}
	if _, err := f.Seek(size-keepTailBytes, io.SeekStart); err != nil {
		return fmt.Errorf("seek tail of %q: %w", path, err)
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read tail of %q: %w", path, err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate %q for tail: %w", path, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind %q for tail: %w", path, err)
	}
	if _, err := f.Write(tail); err != nil {
		return fmt.Errorf("write tail of %q: %w", path, err)
	}
	return nil
}

// CompressArchive gzips path into path.gz (atomically, via a temp file in the
// same directory) and removes path. Returns the .gz path.
func CompressArchive(path string) (string, error) {
	src, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open archive %q: %w", path, err)
	}
	defer src.Close()

	dst := path + ".gz"
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.gz")
	if err != nil {
		return "", fmt.Errorf("create temp for %q: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	gw := gzip.NewWriter(tmp)
	if _, err := io.Copy(gw, src); err != nil {
		gw.Close()
		tmp.Close()
		return "", fmt.Errorf("gzip %q: %w", path, err)
	}
	if err := gw.Close(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("gzip %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close gzip temp for %q: %w", path, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", fmt.Errorf("install gzip %q: %w", dst, err)
	}
	tmpName = "" // ownership transferred to dst
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("remove uncompressed %q: %w", path, err)
	}
	return dst, nil
}

// PruneBackups removes numbered backups of path whose index exceeds maxBackups.
// maxBackups < 0 means unlimited (nothing is removed). Both path.N and
// path.N.gz are pruned.
func PruneBackups(path string, maxBackups int) error {
	if maxBackups < 0 {
		return nil
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir for prune %q: %w", path, err)
	}
	for _, e := range entries {
		n, _, ok := parseBackup(base, e.Name())
		if !ok || n <= maxBackups {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune backup %q: %w", full, err)
		}
	}
	return nil
}

// copyActive copies path to dst, or only its last keepTailBytes bytes when
// keepTailBytes > 0 (seeking past the head so a huge sparse file is not read).
func copyActive(path, dst string, keepTailBytes int64) error {
	src, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open active log %q: %w", path, err)
	}
	defer src.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create archive %q: %w", dst, err)
	}
	defer out.Close()

	if keepTailBytes > 0 {
		st, err := src.Stat()
		if err != nil {
			return fmt.Errorf("stat active log %q: %w", path, err)
		}
		if st.Size() > keepTailBytes {
			if _, err := src.Seek(st.Size()-keepTailBytes, io.SeekStart); err != nil {
				return fmt.Errorf("seek active log %q: %w", path, err)
			}
		}
	}
	if _, err := io.Copy(out, src); err != nil {
		return fmt.Errorf("copy %q -> %q: %w", path, dst, err)
	}
	return out.Sync()
}
