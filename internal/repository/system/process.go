package system

import (
	"context"
	"os"
	"os/exec"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/envutil"
)

type ProcessNodeRepository struct {
	ProcessNode domain.ProcessNode
}

func NewProcessNodeRepository(node domain.ProcessNode) *ProcessNodeRepository {
	return &ProcessNodeRepository{ProcessNode: node}
}

func (s *ProcessNodeRepository) Execute(ctx context.Context) (*exec.Cmd, error) {
	// Execute children that do NOT need parent ready before starting parent
	for _, child := range s.ProcessNode.Children {
		if !child.NeedParentReady {
			childRepo := NewProcessNodeRepository(child)
			_, err := childRepo.Execute(ctx)
			if err != nil {
				return nil, err
			}
		}
	}

	// Provision files before starting the process (env-to-file conversions)
	if err := provisionFiles(s.ProcessNode.Files); err != nil {
		return nil, err
	}

	// Build CLI arguments from the environment (env-to-arguments conversions).
	// Runs after file provisioning so arg callbacks see the full environment.
	args, err := buildArgs(s.ProcessNode.Process, os.Environ())
	if err != nil {
		return nil, err
	}

	env, err := buildProcessEnv(os.Environ(), s.ProcessNode.Process)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, s.ProcessNode.Process.BinaryPath, args...)
	cmd.Env = env
	cmd.Dir = s.ProcessNode.Process.WorkingDir

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	println("Process Started:", s.ProcessNode.Name)

	// Execute children that need parent ready after parent started
	for _, child := range s.ProcessNode.Children {
		if child.NeedParentReady {
			childRepo := NewProcessNodeRepository(child)
			_, err := childRepo.Execute(ctx)
			if err != nil {
				return nil, err
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		return nil, err
	}

	println("Process Finished:", s.ProcessNode.Name)

	return cmd, nil
}

// buildProcessEnv assembles the environment for a spawned process: the parent
// environment filtered by the process's FilterEnv/FilterEnvPattern, with the
// process's additive Environment entries appended last so they override any
// inherited or filtered values.
func buildProcessEnv(parentEnv []string, p domain.Process) ([]string, error) {
	base, err := envutil.Filter(parentEnv, p.FilterEnv, p.FilterEnvPattern)
	if err != nil {
		return nil, err
	}
	return append(base, p.Environment...), nil
}
