package compute

import (
	"context"
	"io"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
)

// MetricTarget is the shared geometry a per-hex metric producer (service
// coverage, population) computes against: the boundary-clipped hex grid the
// pavement passes use, in the city's projection, so hex ids line up. Each
// producer writes its rows under its own run, independent of any compute
// (solvent-streets-25t6), so recomputing roads never hides them.
type MetricTarget struct {
	Proj *geo.UTMProjector
	Grid []geo.Hex
}

// LoadMetricTarget resolves a MetricTarget for one city. prebuilt is the
// grid `pvmt all compute` already built for the city (nil builds it here).
func LoadMetricTarget(ctx context.Context, store db.Store, cfg *config.Config, city *config.CityConfig,
	prebuilt []geo.Hex, errOut io.Writer) (*MetricTarget, error) {
	boundaryGJSON, bbox, proj, err := LoadBoundary(ctx, store, city)
	if err != nil {
		return nil, err
	}
	grid := prebuilt
	if grid == nil {
		// Same grid the pavement passes use, so hex ids line up.
		grid = BuildClippedHexGrid(ctx, cfg, city, proj, bbox, boundaryGJSON, errOut)
	}
	return &MetricTarget{Proj: proj, Grid: grid}, nil
}
