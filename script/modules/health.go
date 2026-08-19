package scriptmodules

import (
	"github.com/supanadit/ezx/domain"
)

// HealthModule exposes ezx.health: setReady(true/false) drives the readiness
// state surfaced by the process-wide /readyz and /healthz endpoints. The HTTP
// server itself is owned by the app (DI), not by scripts.
type HealthModule struct {
	svc domain.HealthService
}

// NewHealthModule returns a HealthModule backed by the given health service.
func NewHealthModule(svc domain.HealthService) *HealthModule {
	return &HealthModule{svc: svc}
}

// SetReady flips the readiness state exposed by /readyz and /healthz.
func (m *HealthModule) SetReady(ready bool) {
	m.svc.SetReady(ready)
}

// Ready reports the current readiness state.
func (m *HealthModule) Ready() bool {
	return m.svc.Ready()
}
