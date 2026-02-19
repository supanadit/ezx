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
	cmd := exec.CommandContext(ctx, s.ProcessNode.Process.BinaryPath, s.ProcessNode.Process.Arguments...)
	cmd.Env = append(os.Environ(), s.ProcessNode.Process.Environment...)
	cmd.Dir = s.ProcessNode.Process.WorkingDir

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	println("Process Started")

	if err := cmd.Wait(); err != nil {
		return nil, err
	}

	println("Process Finished")

	return cmd, nil
}
