package domain

import "time"

// CronSchedule is a 5-field cron expression ("minute hour day month weekday")
// with an optional timezone. Empty Timezone means the process's local time.
// Field grammar matches the shell backup-scheduler conventions: "*", "*/N",
// "N", "N-M", "N,M,K", and "N-M/S".
type CronSchedule struct {
	// Expression is the 5-field cron expression, e.g. "0 2 * * *".
	Expression string
	// Timezone is an IANA name (e.g. "UTC", "Asia/Jakarta") applied when
	// evaluating the expression. Empty means the process's local time.
	Timezone string
}

// SchedulerConfig turns a ProcessNode into a scheduled (cron-driven) process:
// the orchestrator runs the node's Process on each tick instead of once at
// start. It is generic — any process can be scheduled. The optional Gate is a
// readiness probe consulted each tick; while it fails, the tick is skipped
// (e.g. postgres scripts gate backups on primary-role).
type SchedulerConfig struct {
	// Schedule is the cron expression driving ticks.
	Schedule CronSchedule
	// InitialDelay is how long to wait before the first tick evaluation.
	// Zero means start evaluating immediately.
	InitialDelay time.Duration
	// MinInterval dedupes ticks: a job never runs twice within this window.
	// Zero defaults to one minute, matching the per-minute dedup of the shell
	// scheduler it replaces.
	MinInterval time.Duration
	// Gate, when set, is checked before each tick. A failing probe skips the
	// tick (but the loop continues). Nil means the tick always runs.
	Gate *Probe
	// MaxRetries caps consecutive per-tick restarts of the tick process;
	// <=0 means unlimited (the tick process is restarted on failure).
	MaxRetries int
}

// Trigger is a handle to fire a scheduled node's tick action immediately,
// outside its cron schedule (e.g. from a user-defined API route). It is safe
// to call concurrently with the node's ticker loop; calls are serialized
// per-node so at most one tick is in-flight at a time.
type Trigger struct {
	fire     chan struct{}
	done     chan struct{}
	inflight chan struct{}
}

// NewTrigger returns a Trigger that is live until done is closed. inflight is
// a 1-slot semaphore held while a tick runs; fire requests are dropped while
// it is held (fire-and-forget, skip-if-in-flight).
func NewTrigger(done chan struct{}) *Trigger {
	return &Trigger{
		fire:     make(chan struct{}, 1),
		done:     done,
		inflight: make(chan struct{}, 1),
	}
}

// Fire requests an immediate tick. It is non-blocking: if a tick is already
// running, the request is dropped (skip-if-in-flight); otherwise it is queued
// for the next loop iteration (at most one pending request). Returns false if
// the scheduler has been drained.
func (t *Trigger) Fire() bool {
	select {
	case <-t.done:
		return false
	default:
	}
	if t.InFlight() {
		return false
	}
	select {
	case t.fire <- struct{}{}:
		return true
	default:
		return false
	}
}

// C returns a channel that receives when an immediate tick is requested.
// It is consumed by the ticker loop.
func (t *Trigger) C() <-chan struct{} { return t.fire }

// Acquire claims the in-flight slot. Returns false if a tick is already
// running. The caller must Release when the tick completes.
func (t *Trigger) Acquire() bool {
	select {
	case t.inflight <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release frees the in-flight slot.
func (t *Trigger) Release() { <-t.inflight }

// InFlight reports whether a tick is currently running.
func (t *Trigger) InFlight() bool {
	return len(t.inflight) > 0
}
