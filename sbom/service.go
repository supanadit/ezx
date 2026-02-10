// Package bom is a service for bill of material
package sbom

import (
	"context"

	"github.com/supanadit/ezx/domain"
)

type SBOMRepository interface {
	List(ctx context.Context) ([]domain.SBOM, error)
}

type Service struct {
	repoSBOM SBOMRepository
}

func NewService(repoSBOM SBOMRepository) *Service {
	return &Service{
		repoSBOM: repoSBOM,
	}
}

func (s *Service) List(ctx context.Context) ([]domain.SBOM, error) {
	return s.repoSBOM.List(ctx)
}
