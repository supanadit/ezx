// Package logrotate is the adapter that rotates log files the supervised
// process writes itself. It watches the configured log directories with
// fsnotify and rotates a file when a write pushes it past its threshold, then
// renames it and sends the configured reopen signal to the process. It
// implements the rotator.Rotator Port; the orchestrator starts one session per
// node for the node's supervised lifetime.
package logrotate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/rotator"
)

// rotateThrottle bounds how often a change event can trigger a rotation pass.
// Events during the window are coalesced: a pass re-stats every matched file,
// so one pass is enough to observe them all. It is a var so tests can shorten
// the window.
var rotateThrottle = time.Second

// Rotator watches one node's app-owned log files and rotates them on size.
type Rotator struct {
	log logger.Logger
}

// New returns a Rotator that logs through log.
func New(log logger.Logger) *Rotator {
	return &Rotator{log: log}
}

// Run performs an initial size check, then watches the include directories and
// rotates on change until ctx is done. The initial pass handles a file that is
// already oversized when the process starts. Rotation errors are logged and
// skipped; Run returns only when ctx is cancelled or the watcher cannot start.
func (r *Rotator) Run(ctx context.Context, cfg domain.LogRotateConfig, target rotator.Target) error {
	if target == nil {
		return nil
	}
	if len(cfg.IncludePatterns()) == 0 {
		return nil
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("logRotate: create watcher: %w", err)
	}
	defer w.Close()

	watched := map[string]bool{}
	// syncWatches reconciles the live watch set with the include directories.
	// A directory that does not exist yet is covered by watching its nearest
	// existing ancestor, so a log directory created at runtime (or by the app
	// itself) is detected and then watched directly. It reports whether the
	// watch set changed.
	syncWatches := func() bool {
		desired := map[string]bool{}
		for _, dir := range watchDirs(cfg) {
			if d := nearestExisting(dir); d != "" {
				desired[d] = true
			}
		}
		changed := false
		for d := range desired {
			if watched[d] {
				continue
			}
			if err := w.Add(d); err != nil {
				r.log.Debug("logRotate: watch %q: %v", d, err)
				continue
			}
			watched[d] = true
			changed = true
		}
		for d := range watched {
			if desired[d] {
				continue
			}
			// Drop the fallback ancestor watch now that the real directory
			// exists (or the include moved).
			_ = w.Remove(d)
			delete(watched, d)
			changed = true
		}
		return changed
	}
	syncWatches()

	// Handle anything already past the threshold before entering the loop.
	r.rotatePass(cfg, target)

	var timer *time.Timer
	var timerC <-chan time.Time
	arm := func() {
		// Throttle to at most one pass per window; while one is pending the
		// pass re-stats everything, so coalescing is lossless.
		if timer != nil {
			return
		}
		timer = time.NewTimer(rotateThrottle)
		timerC = timer.C
	}

	var pollC <-chan time.Time
	if cfg.Interval > 0 {
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		pollC = ticker.C
	}

	r.log.Info("logRotate: watching %d pattern(s) in %d dir(s)", len(cfg.IncludePatterns()), len(watchDirs(cfg)))

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			// Reconcile on every event so a log directory created at runtime
			// starts being watched; a pass too, in case the file appeared with
			// content already written.
			if syncWatches() {
				arm()
			}
			if r.relevant(cfg, ev.Name) {
				arm()
			}
		case werr, ok := <-w.Errors:
			if !ok {
				return nil
			}
			if errors.Is(werr, fsnotify.ErrEventOverflow) {
				// The kernel dropped events: rotate now and re-add every watch.
				r.log.Warn("logRotate: watch event overflow; forcing a pass")
				r.rotatePass(cfg, target)
				for d := range watched {
					_ = w.Remove(d)
					delete(watched, d)
				}
				syncWatches()
				continue
			}
			r.log.Warn("logRotate: watch error: %v", werr)
		case <-timerC:
			timer = nil
			timerC = nil
			r.rotatePass(cfg, target)
			syncWatches()
		case <-pollC:
			r.rotatePass(cfg, target)
			syncWatches()
		}
	}
}

// nearestExisting returns dir itself when it is an existing directory,
// otherwise the closest existing ancestor. It returns "" when none exists.
func nearestExisting(dir string) string {
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// relevant reports whether a changed path is one of the configured log files
// (or a candidate for one), so unrelated activity in a watched directory does
// not trigger a pass.
func (r *Rotator) relevant(cfg domain.LogRotateConfig, name string) bool {
	if excluded(cfg.Exclude, name) {
		return false
	}
	for _, inc := range cfg.IncludePatterns() {
		if pathMatch(inc, name) {
			return true
		}
	}
	return false
}

// watchDirs returns the unique absolute directories that contain the include
// patterns.
func watchDirs(cfg domain.LogRotateConfig) []string {
	seen := map[string]bool{}
	var out []string
	for _, inc := range cfg.IncludePatterns() {
		dir := filepath.Dir(inc)
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}
