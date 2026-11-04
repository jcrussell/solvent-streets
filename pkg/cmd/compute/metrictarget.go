package compute

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// MetricTarget is where a per-hex metric producer (service coverage,
// population) writes: the latest roads compute snapshot its rows attach to
// — the forecast pattern, so the metric joins the grid the dashboard shows —
// and the boundary-clipped hex grid the pavement passes use, in the city's
// projection.
type MetricTarget struct {
	SnapshotID *int64
	Proj       *geo.UTMProjector
	Grid       []geo.Hex
}

// LoadMetricTarget resolves a MetricTarget for one city. prebuilt is the
// grid `pvmt all compute` already built for the city (nil builds it here).
// producer names the metric family in the missing-snapshot hint ("service
// coverage", "population").
func LoadMetricTarget(ctx context.Context, store db.Store, cfg *config.Config, city *config.CityConfig,
	prebuilt []geo.Hex, errOut io.Writer, producer string) (*MetricTarget, error) {
	roadsResult, err := store.LatestComputeResult(ctx, resource.TypeRoads)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, cmdutil.Hintf(fmt.Errorf("no roads compute snapshot for %s", city.Name),
				"run `pvmt roads compute` (or `pvmt all compute`) first; %s attaches to the roads snapshot", producer)
		}
		return nil, fmt.Errorf("latest roads compute result: %w", err)
	}
	boundaryGJSON, bbox, proj, err := LoadBoundary(ctx, store, city)
	if err != nil {
		return nil, err
	}
	grid := prebuilt
	if grid == nil {
		// Same grid the pavement passes use, so hex ids line up.
		grid = BuildClippedHexGrid(ctx, cfg, city, proj, bbox, boundaryGJSON, errOut)
	}
	return &MetricTarget{SnapshotID: roadsResult.SnapshotID, Proj: proj, Grid: grid}, nil
}
