package script

import (
	"fmt"
	"time"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
	"github.com/supanadit/ezx/runtime"
)

// SchedulerControl is the local Port for triggering and observing scheduled
// nodes (R10). It is satisfied structurally by *orchestrator.Service.
type SchedulerControl interface {
	// Trigger fires a scheduled node's tick immediately.
	Trigger(name string) bool
	// InFlight reports whether the node currently has a tick running.
	InFlight(name string) bool
	// Scheduled reports whether a scheduled node with the name exists.
	Scheduled(name string) bool
}

// SchedulerModule exposes ezx.scheduler: a generic cron-driven scheduler.
// Scripts attach a scheduler to a ProcessNode (node.scheduler = {...}) so the
// orchestrator runs that node's Process on each cron tick; they can also fire
// a tick manually by name, e.g. from a user-defined API route.
//
//	const { scheduler } = require("ezx");
//	node.scheduler = scheduler.build({
//	  schedule: { expression: "0 2 * * *", timezone: "UTC" },
//	  initialDelay: 120e9,   // nanoseconds (120s)
//	  minInterval: 60e9,
//	  gate: { type: "http", http: { url: "http://localhost:8008/master" } },
//	});
//	scheduler.trigger("backup-full");   // manual fire-and-forget
//	scheduler.status("backup-full");    // { exists, inflight }
type SchedulerModule struct {
	svc SchedulerControl
	inv runtime.Invoker
}

// NewSchedulerModule returns a SchedulerModule backed by the given control
// port, so trigger/status reach the running scheduled nodes. svc may be nil
// in tests. inv delivers JS callbacks for scheduler.every; it may be nil when
// the engine cannot call back.
func NewSchedulerModule(svc SchedulerControl, inv runtime.Invoker) *SchedulerModule {
	return &SchedulerModule{svc: svc, inv: inv}
}

// Build validates the cron expression and returns the SchedulerConfig. It is
// a convenience constructor for assigning to ProcessNode.Scheduler.
func (m *SchedulerModule) Build(cfg domain.SchedulerConfig) (*domain.SchedulerConfig, error) {
	if _, err := repository.ParseCron(cfg.Schedule.Expression); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Parse validates a 5-field cron expression, returning it unchanged on success.
func (m *SchedulerModule) Parse(expr string) (string, error) {
	if _, err := repository.ParseCron(expr); err != nil {
		return "", err
	}
	return expr, nil
}

// Trigger fires a scheduled node's tick immediately (fire-and-forget, skip if
// a tick is already running). Returns false if no such node exists or it has
// been drained.
func (m *SchedulerModule) Trigger(name string) bool {
	if m.svc == nil {
		return false
	}
	return m.svc.Trigger(name)
}

// Status reports whether a scheduled node exists and whether it has a tick in
// flight.
func (m *SchedulerModule) Status(name string) map[string]any {
	if m.svc == nil {
		return map[string]any{"exists": false, "inflight": false}
	}
	return map[string]any{
		"exists":   m.svc.Scheduled(name),
		"inflight": m.svc.InFlight(name),
	}
}

// everyOpts holds the options for scheduler.every.
type everyOpts struct {
	// Timezone is an IANA name applied when evaluating the cron expression.
	Timezone string
	// InitialDelay is how long (ns) before the first tick evaluation.
	InitialDelay int64
	// MinInterval dedupes ticks (ns); <=0 defaults to one minute.
	MinInterval int64
	// Gate is a readiness probe consulted each tick; a failing probe skips it.
	Gate *domain.Probe
}

// Every registers a JS callback to run on each cron tick. It returns the
// SchedulerConfig to assign to a node's scheduler field. expr is a 5-field cron
// expression. The callback is invoked from the orchestrator's scheduled-tick
// path while the script is blocked in chain.run — goja is single-threaded, so
// the callback must only fire while the script is idle. Keep it short and
// non-blocking. This replaces the curl→HTTP→handler hack.
func (m *SchedulerModule) Every(expr string, fn any, opts ...everyOpts) (*domain.SchedulerConfig, error) {
	if _, err := repository.ParseCron(expr); err != nil {
		return nil, err
	}
	if m.inv == nil || fn == nil {
		return nil, fmt.Errorf("scheduler.every requires a scripting engine with callback support")
	}
	var o everyOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	cfg := &domain.SchedulerConfig{
		Schedule: domain.CronSchedule{Expression: expr, Timezone: o.Timezone},
		Gate:     o.Gate,
		Tick: func() {
			_, _ = m.inv.Call(fn)
		},
	}
	if o.InitialDelay > 0 {
		cfg.InitialDelay = int64ToDuration(o.InitialDelay)
	}
	if o.MinInterval > 0 {
		cfg.MinInterval = int64ToDuration(o.MinInterval)
	}
	return cfg, nil
}

func int64ToDuration(ns int64) time.Duration {
	return time.Duration(ns)
}
