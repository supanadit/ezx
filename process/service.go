package process

import (
	"context"
	"os/exec"
)

type ProcessNodeRepository interface {
	Execute(ctx context.Context) (*exec.Cmd, error)
}

type Service struct {
	processNodeRepository ProcessNodeRepository
}

func NewService(processNode ProcessNodeRepository) *Service {
	return &Service{}
}
