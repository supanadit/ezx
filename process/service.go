package process

import (
	"context"

	"github.com/supanadit/ezx/domain"
)

// EnvironmentHook allows custom modification of environment variables before process start.
type EnvironmentHook interface {
	ModifyEnv(env []string) []string
}

// StartupHook enables custom setup logic before starting a process.
type StartupHook interface {
	OnStartup(ctx context.Context) error
}

// ShutdownHook enables custom cleanup logic when shutting down a process.
type ShutdownHook interface {
	OnShutdown(ctx context.Context) error
}

// HealthCheckHook provides custom logic to determine if a process is healthy.
type HealthCheckHook interface {
	IsHealthy(ctx context.Context) bool
}

type Service struct {
	envHook    EnvironmentHook // Optional: nil if not provided
	startHook  StartupHook     // Optional: nil if not provided
	shutHook   ShutdownHook    // Optional: nil if not provided
	healthHook HealthCheckHook // Optional: nil if not provided
}

func NewService(envHook EnvironmentHook, startHook StartupHook, shutHook ShutdownHook, healthHook HealthCheckHook) *Service {
	return &Service{
		envHook:    envHook,
		startHook:  startHook,
		shutHook:   shutHook,
		healthHook: healthHook,
	}
}

// Run starts the process, applying startup and environment hooks if provided.
func (s *Service) Run(ctx context.Context, p domain.Process) error {
	// Future: Call startHook.OnStartup(), apply envHook to p.Environment, then spawn process
	return nil // Stub
}

// Shutdown stops the process, calling shutdown hook if provided.
func (s *Service) Shutdown(ctx context.Context) error {
	// Future: Call shutHook.OnShutdown()
	return nil // Stub
}

// HealthCheck determines process health, using health hook if provided.
func (s *Service) HealthCheck(ctx context.Context) error {
	// Future: Use healthHook.IsHealthy() to check
	return nil // Stub
}
