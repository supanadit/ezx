package scriptmodules

import (
	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
	"github.com/supanadit/ezx/orchestrator"
)

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
	orch *orchestrator.Service
}

// NewSchedulerModule returns a SchedulerModule backed by the orchestrator, so
// trigger/status reach the running scheduled nodes. orch may be nil in tests.
func NewSchedulerModule(orch *orchestrator.Service) *SchedulerModule {
	return &SchedulerModule{orch: orch}
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
	if m.orch == nil {
		return false
	}
	return m.orch.Trigger(name)
}

// Status reports whether a scheduled node exists and whether it has a tick in
// flight.
func (m *SchedulerModule) Status(name string) map[string]any {
	if m.orch == nil {
		return map[string]any{"exists": false, "inflight": false}
	}
	return map[string]any{
		"exists":   m.orch.Scheduled(name),
		"inflight": m.orch.InFlight(name),
	}
}
