package domain

import "time"

// ReopenMode enumerates how a rotated file is handed back to the writing
// process. The process holds the file descriptor, so renaming the file is not
// enough: the process must be told to reopen, or the file must be copied and
// truncated in place.
type ReopenMode string

const (
	// ReopenSignal renames the active file and then sends Signal to the
	// supervised process, which closes and reopens its descriptor (Traefik,
	// Apache, Nginx all document this). This is the default and the only
	// lossless mode.
	ReopenSignal ReopenMode = "signal"
	// ReopenCopyTruncate copies the active file to its archive and then
	// truncates it to zero, without signaling. It is the documented-lossy
	// fallback for processes that cannot reopen: lines written between the copy
	// and the truncate are lost, and a process that keeps its file offset
	// (rather than seeking) leaves a sparse hole after the truncate. Prefer
	// ReopenSignal whenever the process supports it.
	ReopenCopyTruncate ReopenMode = "copytruncate"
)

// OnMissingPidMode enumerates what to do when a rotation pass cannot resolve a
// reopen target (the supervised process is not running). The safe default is
// Skip: renaming or deleting a file without a reopen target leaves the writer
// pointing at an unlinked inode, so space is not reclaimed and the file
// disappears from the directory.
type OnMissingPidMode string

const (
	// OnMissingPidSkip leaves every file untouched for this pass and logs a
	// warning. This is the default.
	OnMissingPidSkip OnMissingPidMode = "skip"
	// OnMissingPidFail reports the missing target as a rotation error. The
	// error is logged (rotation never aborts the supervised process).
	OnMissingPidFail OnMissingPidMode = "fail"
)

// LogRotateFile is one include pattern with optional per-file overrides of the
// group defaults. A zero MaxBytes/MaxBackups (or an empty Signal) inherits the
// group value, so only the fields that differ need to be spelled out.
type LogRotateFile struct {
	// Include is a glob pattern (filepath.Match syntax) selecting log files,
	// e.g. "/var/log/apache2/*.log". A pattern without a path separator matches
	// the base name of files in the working directory. There is no recursive
	// "**"; each pattern covers one directory level.
	Include string
	// MaxBytes overrides the group threshold for files matched by this entry.
	MaxBytes int64
	// MaxBackups overrides the group backup count for this entry.
	MaxBackups int
	// Signal overrides the group reopen signal for this entry. A pass dedupes
	// signals, so the same signal is sent once even when several files use it.
	Signal string
}

// OversizedConfig handles a legacy file that is already far past any sane
// threshold when the process starts (e.g. a multi-gigabyte sparse file left by
// an old truncate). Renaming it is cheap (metadata only), but compressing it
// means reading everything, including holes. Setting this makes the rotator
// keep only the tail of such a file instead of reading the head.
type OversizedConfig struct {
	// MaxBytes is the size at or above which the tail-only path applies. It
	// also becomes a rotation trigger, so an already-oversized file is rotated
	// on the first pass.
	MaxBytes int64
	// KeepTailBytes is how many trailing bytes to preserve. The head is
	// discarded (with a warning); it is never read.
	KeepTailBytes int64
	// Compress gzips the preserved tail (producing the usual .1.gz archive).
	Compress bool
}

// LogRotateConfig rotates log files that the supervised process writes itself,
// as opposed to LogConfig's stdout/stderr streams, whose descriptors ezx owns
// and can rotate unconditionally. The process keeps its own descriptor, so
// rotation is a rename followed by a reopen signal (or copy+truncate). It is a
// node-level concern because the reopen signal must reach the node's own
// supervised process.
//
// Rotation is size-driven: the node watches its log directories and rotates a
// file exactly when a write pushes it past its threshold, with no schedule and
// no periodic process spawn. interval adds an optional low-frequency poll for
// filesystems where change events are unreliable (network mounts written from
// another host).
type LogRotateConfig struct {
	// FilePath is the single-file shorthand: rotate this one file. Mutually
	// exclusive with Files.
	FilePath string
	// Files lists include patterns with optional per-file overrides. New files
	// created at runtime (per-vhost logs) are picked up without a restart
	// because the containing directories are watched.
	Files []LogRotateFile
	// Exclude lists glob patterns that must never be rotated, matched against
	// both the base name and the full path. Use it to exclude the rotator's own
	// archives (e.g. "*.gz", "*.1") when an include pattern is broad enough to
	// match them.
	Exclude []string
	// MaxBytes is the group threshold in bytes: a file at or above it is
	// rotated. Overridable per file. Zero means the default (10 MiB).
	MaxBytes int64
	// MaxBackups is how many rotated files (file.1 … file.N) to keep;
	// <0 = unlimited, 0 = default 3. Overridable per file.
	MaxBackups int
	// Compress gzips each new archive (file.1 → file.1.gz) group-wide.
	Compress bool
	// Signal is the reopen signal sent to the supervised process after the
	// rename (e.g. "USR1"). Required for the signal reopen mode unless every
	// file entry supplies its own.
	Signal string
	// Reopen selects the reopen mechanism. Empty defaults to "signal".
	Reopen ReopenMode
	// Interval, when > 0, enables a safety-net poll at this frequency in
	// addition to the change-event watcher. Zero (the default) means
	// event-driven only.
	Interval time.Duration
	// OnMissingPid decides what happens when the reopen target cannot be
	// resolved. Empty defaults to "skip".
	OnMissingPid OnMissingPidMode
	// Oversized handles a legacy file that is already far past the threshold.
	Oversized *OversizedConfig
}

// ReopenModeOrDefault returns the configured reopen mode, defaulting to
// ReopenSignal.
func (c LogRotateConfig) ReopenModeOrDefault() ReopenMode {
	if c.Reopen == "" {
		return ReopenSignal
	}
	return c.Reopen
}

// OnMissingPidOrDefault returns the configured missing-PID policy, defaulting
// to OnMissingPidSkip.
func (c LogRotateConfig) OnMissingPidOrDefault() OnMissingPidMode {
	if c.OnMissingPid == "" {
		return OnMissingPidSkip
	}
	return c.OnMissingPid
}

// IncludePatterns returns the effective include list: Files when set, otherwise
// the single FilePath shorthand.
func (c LogRotateConfig) IncludePatterns() []string {
	if len(c.Files) > 0 {
		out := make([]string, 0, len(c.Files))
		for _, f := range c.Files {
			if f.Include != "" {
				out = append(out, f.Include)
			}
		}
		return out
	}
	if c.FilePath != "" {
		return []string{c.FilePath}
	}
	return nil
}
