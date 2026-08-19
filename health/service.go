// Package health implements the readiness/liveness use-case. The Service holds
// the readiness state and contains no HTTP concern. The delivery layer
// (internal/rest) exposes it, and the orchestrator drives it via SetReady.
package health

import "sync/atomic"

// Service implements domain.HealthService.
type Service struct {
	ready atomic.Bool
}

// NewService returns a Service with readiness initially false.
func NewService() *Service {
	return &Service{}
}

// Live reports that the process is alive.
func (s *Service) Live() bool { return true }

// Ready reports the current readiness state.
func (s *Service) Ready() bool { return s.ready.Load() }

// SetReady flips the readiness state.
func (s *Service) SetReady(ready bool) { s.ready.Store(ready) }
