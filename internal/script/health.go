package script

// Readiness is the local Port for flipping the process readiness state. It is
// satisfied structurally by *health.Service; declared here (R10) so delivery
// never depends on a concrete service type.
type Readiness interface {
	// SetReady flips the readiness state.
	SetReady(ready bool)
	// Ready reports the current readiness state.
	Ready() bool
}

// HealthModule exposes ezx.health: setReady(true/false) drives the readiness
// state surfaced by the process-wide /readyz and /healthz endpoints. The HTTP
// server itself is owned by the app (DI), not by scripts.
type HealthModule struct {
	svc Readiness
}

// NewHealthModule returns a HealthModule backed by the given readiness port.
func NewHealthModule(svc Readiness) *HealthModule {
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
