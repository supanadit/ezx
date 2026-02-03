package license

import (
	"context"

	"github.com/supanadit/ezx/domain"
)

type LicenseRepository interface {
	List(ctx context.Context) ([]domain.License, error)
}

type Service struct {
	repoLicense LicenseRepository
}

func NewService(repoLicense LicenseRepository) *Service {
	return &Service{
		repoLicense: repoLicense,
	}
}

func (s *Service) List(ctx context.Context) ([]domain.License, error) {
	return s.repoLicense.List(ctx)
}
