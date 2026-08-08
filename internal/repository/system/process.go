package system

import (
	"context"
	"os"
	"os/exec"

	"github.com/supanadit/ezx/domain"
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

	cmd := exec.CommandContext(ctx, s.ProcessNode.Process.BinaryPath, s.ProcessNode.Process.Arguments...)
	cmd.Env = append(os.Environ(), s.ProcessNode.Process.Environment...)
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
