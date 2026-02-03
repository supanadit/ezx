package memory

import (
	"context"

	"github.com/supanadit/ezx/domain"
)

type LicenseRepository struct {
	// Nothing here because from memory
}

func NewLicenseRepository() *LicenseRepository {
	return &LicenseRepository{}
}

func (b *LicenseRepository) List(ctx context.Context) ([]domain.License, error) {
	return []domain.License{
		{
			Name: "MIT",
			Link: "https://opensource.org/license/mit",
		},
		{
			Name: "GPLv2",
			Link: "https://www.gnu.org/licenses/old-licenses/gpl-2.0.html",
		},
		{
			Name: "GPLv3",
			Link: "https://www.gnu.org/licenses/gpl-3.0.html",
		},
		{
			Name: "AGPLv3",
			Link: "https://www.gnu.org/licenses/agpl-3.0.html",
		},
		{
			Name: "Apache License 2.0",
			Link: "https://www.apache.org/licenses/LICENSE-2.0",
		},
	}, nil
}
