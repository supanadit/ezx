// Package bom is a service for bill of material
package bom

import (
	"context"

	"github.com/supanadit/ezx/domain"
)

type BOMRepository interface {
	List(ctx context.Context) ([]domain.BOM, error)
}

type Service struct {
	repoBOM BOMRepository
}

func NewService(repoBOM BOMRepository) *Service {
	return &Service{
		repoBOM: repoBOM,
	}
}

func (s *Service) List(ctx context.Context) ([]domain.BOM, error) {
	return s.repoBOM.List(ctx)
}
