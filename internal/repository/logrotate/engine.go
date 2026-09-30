package logrotate

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
	"github.com/supanadit/ezx/rotator"
)

// Defaults for LogRotateConfig when the group and per-file values are unset.
// They mirror the stream rotator's defaults so the two features stay symmetric.
const (
	defaultMaxBytes   = 10 << 20 // 10 MiB
	defaultMaxBackups = 3
)

// matchedFile pairs a matched log file with the include entry that selected it,
// so per-file overrides (maxBytes/maxBackups/signal) resolve deterministically:
// the first matching entry in declaration order wins.
type matchedFile struct {
	path  string
	entry domain.LogRotateFile
}

// crossedFile is a matched file that has crossed its threshold in this pass,
// carrying the archive it was rotated to.
type crossedFile struct {
	path    string
	entry   domain.LogRotateFile
	size    int64
	archive string
}

// matches expands the include patterns, drops excluded paths, dedupes by
// resolved path, and skips non-regular files. Declaration order determines
// which entry's overrides apply when several patterns match one file.
func (r *Rotator) matches(cfg domain.LogRotateConfig) []matchedFile {
	entries := includeEntries(cfg)
	seen := map[string]bool{}
	var out []matchedFile
	for _, e := range entries {
		glob, err := repository.Glob(e.Include)
		if err != nil {
			r.log.Warn("logRotate: bad include pattern %q: %v", e.Include, err)
			continue
		}
		for _, p := range glob {
			fi, err := os.Stat(p)
			if err != nil || fi.IsDir() {
				continue
			}
			if excluded(cfg.Exclude, p) {
				continue
			}
			key := resolveKey(p)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, matchedFile{path: p, entry: e})
		}
	}
	return out
}

// includeEntries returns the configured include entries; the single-file
// shorthand becomes one entry with no overrides.
func includeEntries(cfg domain.LogRotateConfig) []domain.LogRotateFile {
	if len(cfg.Files) > 0 {
		out := make([]domain.LogRotateFile, 0, len(cfg.Files))
		for _, f := range cfg.Files {
			if f.Include != "" {
				out = append(out, f)
			}
		}
		return out
	}
	if cfg.FilePath != "" {
		return []domain.LogRotateFile{{Include: cfg.FilePath}}
	}
	return nil
}

// resolveKey returns a stable identity for a file so the same file listed by
// two patterns is rotated once. It prefers a symlink-resolved absolute path and
// falls back to the absolute path when resolution fails.
func resolveKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// excluded reports whether path matches any exclude pattern. A pattern without a
// path separator is matched against the base name; a pattern with one is matched
// against the full path. Both are tried.
func excluded(patterns []string, path string) bool {
	for _, p := range patterns {
		if pathMatch(p, path) {
			return true
		}
	}
	return false
}

// pathMatch applies filepath.Match semantics the way the include list is
// interpreted: separator-free patterns match basenames, others match the path.
// It also compares absolute-normalized forms so a relative pattern matches the
// absolute paths fsnotify reports.
func pathMatch(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	if !strings.ContainsRune(pattern, os.PathSeparator) {
		if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok {
			return true
		}
	}
	if ok, _ := filepath.Match(pattern, path); ok {
		return true
	}
	absPattern, perr := filepath.Abs(pattern)
	absPath, aerr := filepath.Abs(path)
	if perr == nil && aerr == nil {
		if ok, _ := filepath.Match(absPattern, absPath); ok {
			return true
		}
	}
	return false
}

// effectiveMaxBytes resolves the rotation threshold for a matched file.
func effectiveMaxBytes(cfg domain.LogRotateConfig, e domain.LogRotateFile) int64 {
	if e.MaxBytes > 0 {
		return e.MaxBytes
	}
	if cfg.MaxBytes > 0 {
		return cfg.MaxBytes
	}
	return defaultMaxBytes
}

// effectiveMaxBackups resolves the backup count (<0 = unlimited).
func effectiveMaxBackups(cfg domain.LogRotateConfig, e domain.LogRotateFile) int {
	if e.MaxBackups != 0 {
		return e.MaxBackups
	}
	if cfg.MaxBackups != 0 {
		return cfg.MaxBackups
	}
	return defaultMaxBackups
}

// effectiveSignal resolves the reopen signal for a matched file.
func effectiveSignal(cfg domain.LogRotateConfig, e domain.LogRotateFile) string {
	if e.Signal != "" {
		return e.Signal
	}
	return cfg.Signal
}

// rotateThreshold is the size at which a file rotates. An oversized trigger
// (when configured) also rotates, so an already-oversized file is handled on
// the first pass even if the normal threshold is larger.
func rotateThreshold(cfg domain.LogRotateConfig, e domain.LogRotateFile) int64 {
	limit := effectiveMaxBytes(cfg, e)
	if o := cfg.Oversized; o != nil && o.MaxBytes < limit {
		limit = o.MaxBytes
	}
	return limit
}

// isOversized reports whether a file of the given size should take the
// tail-only path (its head is never read).
func isOversized(cfg domain.LogRotateConfig, size int64) bool {
	return cfg.Oversized != nil && size >= cfg.Oversized.MaxBytes
}

// targetAvailable reports whether a reopen signal can be delivered.
func targetAvailable(target rotator.Target) bool {
	return target != nil && target.PID() > 0
}

// rotatePass performs one size evaluation and rotation over every matched file.
// Order is deliberate: check the reopen target, rename (or copy+truncate) every
// crossed file, send the reopen signal once per distinct signal, and only then
// compress and prune — so a rename+compress can never archive bytes the app
// writes before it reopens (compressing first is the classic bug).
func (r *Rotator) rotatePass(cfg domain.LogRotateConfig, target rotator.Target) {
	reopen := cfg.ReopenModeOrDefault()
	if reopen == domain.ReopenSignal && !targetAvailable(target) {
		if cfg.OnMissingPidOrDefault() == domain.OnMissingPidFail {
			r.log.Error("logRotate: no reopen target (process not running); aborting pass without touching files")
		} else {
			r.log.Warn("logRotate: no reopen target (process not running); skipping pass")
		}
		return
	}

	var crossed []crossedFile
	for _, m := range r.matches(cfg) {
		st, err := os.Stat(m.path)
		if err != nil {
			continue
		}
		if st.Size() < rotateThreshold(cfg, m.entry) {
			continue
		}
		crossed = append(crossed, crossedFile{path: m.path, entry: m.entry, size: st.Size()})
	}
	if len(crossed) == 0 {
		return
	}

	// Phase 1: rename / copy+truncate every crossed file.
	for i := range crossed {
		cf := &crossed[i]
		var archive string
		var err error
		if reopen == domain.ReopenCopyTruncate {
			var keep int64
			if isOversized(cfg, cf.size) {
				keep = cfg.Oversized.KeepTailBytes
			}
			archive, err = repository.CopyTruncate(cf.path, keep)
		} else {
			archive, err = repository.ShiftAndRename(cf.path)
		}
		if err != nil {
			r.log.Warn("logRotate: rotate %q: %v", cf.path, err)
			continue
		}
		cf.archive = archive
		r.log.Info("logRotate: rotated %q -> %q (%d bytes)", cf.path, archive, cf.size)
	}

	// Phase 2: one signal per distinct signal, only once something rotated.
	if reopen == domain.ReopenSignal {
		r.signalOnce(cfg, crossed, target)
	}

	// Phase 3: compress and prune, after the signal.
	for i := range crossed {
		cf := &crossed[i]
		if cf.archive == "" {
			continue
		}
		if isOversized(cfg, cf.size) {
			if err := repository.KeepTail(cf.archive, cfg.Oversized.KeepTailBytes); err != nil {
				r.log.Warn("logRotate: keep tail of %q: %v", cf.archive, err)
			}
			if cfg.Oversized.Compress {
				r.compress(cf.archive)
			}
		} else if cfg.Compress {
			r.compress(cf.archive)
		}
		if err := repository.PruneBackups(cf.path, effectiveMaxBackups(cfg, cf.entry)); err != nil {
			r.log.Warn("logRotate: prune %q: %v", cf.path, err)
		}
	}
}

// signalOnce sends the reopen signal for each distinct signal among the
// successfully rotated files. Several logs usually belong to the same process,
// so a shared signal is delivered once for the whole pass.
func (r *Rotator) signalOnce(cfg domain.LogRotateConfig, crossed []crossedFile, target rotator.Target) {
	sent := map[string]bool{}
	for _, cf := range crossed {
		if cf.archive == "" {
			continue
		}
		sig := effectiveSignal(cfg, cf.entry)
		if sig == "" || sent[sig] {
			continue
		}
		sent[sig] = true
		osig, ok := repository.SignalName(sig)
		if !ok {
			r.log.Warn("logRotate: unknown signal %q", sig)
			continue
		}
		if err := target.Signal(osig); err != nil {
			r.log.Warn("logRotate: signal %s to pid %d failed: %v", sig, target.PID(), err)
			continue
		}
		r.log.Info("logRotate: sent %s to pid %d to reopen logs", sig, target.PID())
	}
}

// compress gzips an archive, logging (but not failing) on error.
func (r *Rotator) compress(path string) {
	gz, err := repository.CompressArchive(path)
	if err != nil {
		r.log.Warn("logRotate: compress %q: %v", path, err)
		return
	}
	r.log.Info("logRotate: compressed %q", gz)
}
