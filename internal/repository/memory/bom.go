package memory

import (
	"context"

	"github.com/supanadit/ezx/domain"
)

type BOMRepository struct {
	// Nothing here because from memory
}

func NewBOMRepository() *BOMRepository {
	return &BOMRepository{}
}

func (b *BOMRepository) List(ctx context.Context) ([]domain.BOM, error) {
	return []domain.BOM{
		{
			Name:    "ZIG",
			Site:    "https://ziglang.org",
			License: "MIT",
			Version: "0.15.2",
		},
		{
			Name:    "GNU Make",
			Site:    "https://www.gnu.org/software/make/",
			License: "GPLv3",
			Version: "4.4",
		},
		{
			Name:    "OpenSSL",
			Site:    "https://openssl-library.org/",
			License: "Apache License 2.0",
			Version: "3.6",
		},
		{
			Name:    "BusyBox",
			Site:    "https://www.busybox.net/",
			License: "GPLv2",
			Version: "1.37.0",
		},
		{
			Name:    "CURL",
			Site:    "https://curl.se/",
			License: "MIT",
		},
		{
			Name:    "Automake",
			Site:    "https://www.gnu.org/software/automake/",
			License: "GPLv2+",
		},
		{
			Name:    "Autoconf",
			Site:    "https://www.gnu.org/software/autoconf/",
			License: "GPLv2+",
		},
	}, nil
}
