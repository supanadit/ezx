package system

import (
	"context"
	"os"
	"os/exec"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
)

// ProcessRepository implements the process.ProcessRepository Port for the local
// OS. It is constructed per ProcessNode and is the handle to that process once
// started. It does not perform any tree-walk or lifecycle orchestration — that
// is the orchestrator's responsibility.
type ProcessRepository struct {
	node   domain.ProcessNode
	cmd    *exec.Cmd
	done   chan struct{}
	start  bool
	reaper *Reaper
	code   int
}

// NewProcessRepository creates a handle for the given ProcessNode, reaping its
// exit through the shared reaper (nil disables reaper-based waiting and falls
// back to local cmd.Wait). It has not been started yet.
func NewProcessRepository(node domain.ProcessNode, reaper *Reaper) *ProcessRepository {
	return &ProcessRepository{
		node:   node,
		done:   make(chan struct{}),
		reaper: reaper,
	}
}

// Start launches the configured process. It applies the process's
// FilterEnv/FilterEnvPattern to the supplied parent environment, then appends
// its additive Environment entries. Arguments are supplied by the caller (the
// orchestrator), so this stays a thin one-process adapter.
func (s *ProcessRepository) Start(ctx context.Context, env []string, lc domain.LogConfig) error {
	if s.start {
		return nil
	}
	s.start = true

	procEnv, err := repository.BuildProcessEnv(env, s.node.Process)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, s.node.Process.BinaryPath, s.node.Process.Arguments...)
	cmd.Env = procEnv
	cmd.Dir = s.node.Process.WorkingDir
	if err := repository.ApplyCredential(cmd, s.node.Process); err != nil {
		return err
	}
	repository.SetProcessGroupLeader(cmd)

	switch lc.Stdout {
	case domain.LogDestDiscard:
		cmd.Stdout = nil
	case domain.LogDestStderr:
		cmd.Stdout = os.Stderr
	default: // LogDestStdout
		cmd.Stdout = os.Stdout
	}
	switch lc.Stderr {
	case domain.LogDestDiscard:
		cmd.Stderr = nil
	default: // LogDestStderr
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		close(s.done)
		return err
	}
	s.cmd = cmd

	// Reap through the shared reaper when available (avoids racing wait4);
	// otherwise fall back to a local cmd.Wait goroutine.
	if s.reaper != nil {
		ch := s.reaper.Register(cmd.Process.Pid)
		go func() {
			exit, ok := <-ch
			if ok {
				s.code = exit.code
			}
			close(s.done)
		}()
	} else {
		go func() {
			_ = cmd.Wait()
			close(s.done)
		}()
	}

	return nil
}

// Wait blocks until the process exits and returns its exit code.
func (s *ProcessRepository) Wait() (int, error) {
	<-s.done
	if s.cmd == nil {
		return -1, nil
	}
	if s.code != 0 {
		return s.code, nil
	}
	return s.cmd.ProcessState.ExitCode(), nil
}

// Signal sends a signal to the process.
func (s *ProcessRepository) Signal(sig os.Signal) error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Signal(sig)
}

// Kill force-terminates the process.
func (s *ProcessRepository) Kill() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Kill()
}

// PID returns the running process's PID, or 0 if not started.
func (s *ProcessRepository) PID() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Done closes when the process exits.
func (s *ProcessRepository) Done() <-chan struct{} {
	return s.done
}
