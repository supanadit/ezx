package domain

// SignalForwardError reports an unrecognized signal name in a node's
// ForwardSignals list.
type SignalForwardError struct {
	Name string
}

func (e SignalForwardError) Error() string {
	return "unknown signal name: " + e.Name
}

// HealthService is the Port for the readiness/liveness use-case. The delivery
// layer (internal/rest) depends on it; the orchestrator drives it. It carries
// no HTTP concern.
type HealthService interface {
	// Live reports whether the process is alive.
	Live() bool
	// Ready reports whether the supervised process is ready.
	Ready() bool
	// SetReady flips the readiness state.
	SetReady(ready bool)
}

// HealthConfig declares the readiness gate for a ProcessNode. The HTTP server
// itself is process-wide (owned by the app), so this only carries the probe.
type HealthConfig struct {
	// ReadyProbe gates readiness: ready is true only while the probe passes.
	// If nil, readiness is driven by explicit SetReady calls.
	ReadyProbe *Probe
}
