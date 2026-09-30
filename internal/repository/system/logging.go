package system

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/supanadit/ezx/internal/repository"
)

// Defaults for file-backed log destinations (LogDestFile). A zero MaxBytes
// means defaultMaxBytes; a zero MaxBackups means defaultMaxBackups.
const (
	defaultMaxBytes   = 10 << 20 // 10 MiB
	defaultMaxBackups = 3
)

// rotatingFileWriter is an io.WriteCloser that writes to path and rotates:
// when a write would push the active file past maxBytes, it closes the file,
// shifts path.N → path.N+1 (path → path.1 …), drops backups beyond maxBackups,
// and opens a fresh path (append). Safe for concurrent use (single writer
// goroutine in practice; mutex guards Close vs Write).
type rotatingFileWriter struct {
	path       string
	maxBytes   int64
	maxBackups int
	compress   bool
	f          *os.File
	size       int64
	mu         sync.Mutex
}

// newRotatingFileWriter opens path in append mode (creating parent dirs),
// applying the default MaxBytes/MaxBackups when zero. The active file's current
// size is seeded so the first rotation triggers at the right boundary. When
// compress is true, each rotated file is gzipped (path.1 → path.1.gz).
func newRotatingFileWriter(path string, maxBytes int64, maxBackups int, compress bool) (*rotatingFileWriter, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	if maxBackups == 0 {
		maxBackups = defaultMaxBackups
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir for %q: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat log file %q: %w", path, err)
	}
	return &rotatingFileWriter{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
		compress:   compress,
		f:          f,
		size:       st.Size(),
	}, nil
}

// Write appends p to the active file, rotating first when the write would push
// it past maxBytes. A single write larger than maxBytes is written whole to a
// fresh file (no split marker, no infinite loop); the next write rotates again.
func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close closes the active file. Idempotent; safe to call concurrently with
// Write (the mutex serializes them).
func (w *rotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// rotate closes the active file, shifts backups down (path.1 → path.2 → …),
// renames the active file to path.1, gzips it when compression is enabled,
// drops backups beyond maxBackups, and reopens a fresh path in append mode.
// Caller must hold w.mu.
func (w *rotatingFileWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	w.f = nil

	archive, err := repository.ShiftAndRename(w.path)
	if err != nil {
		return err
	}
	if w.compress {
		if _, err := repository.CompressArchive(archive); err != nil {
			return err
		}
	}
	if err := repository.PruneBackups(w.path, w.maxBackups); err != nil {
		return err
	}

	// Reopen a fresh path (append).
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	w.size = 0
	return nil
}
