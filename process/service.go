package process

import (
	"context"
	"os"
	"os/exec"

	"github.com/supanadit/ezx/domain"
)

type Service interface {
	Execute(ctx context.Context, node domain.ProcessNode) (*exec.Cmd, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

func (s *service) Execute(ctx context.Context, node domain.ProcessNode) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, node.Process.BinaryPath, node.Process.Arguments...)
	cmd.Env = node.Process.Environment
	cmd.Dir = node.Process.WorkingDir

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return cmd, nil
}
