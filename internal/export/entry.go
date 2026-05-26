package export

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
)

// ErrNoBoundary signals that a city has no boundary stored. Callers that
// iterate over multiple cities (e.g. gensite, the multi-city exporter)
// use errors.Is to skip the city rather than failing the whole export.
var ErrNoBoundary = errors.New("no boundary stored")

// CityEntry holds the config and store for a single city.
type CityEntry struct {
	Config *config.Config
	City   config.CityConfig
	Store  db.Store
	Slug   string
}

// WithSnapshot returns a copy of this CityEntry whose Store is pinned to
// the given snapshot id. Snapshot-aware reads (compute results, hex stats,
// cohorts, forecasts) on the returned store will filter to that snapshot;
// unaware reads (features, boundary, snapshot list) are unchanged.
func (entry CityEntry) WithSnapshot(snapshotID int64) CityEntry {
	entry.Store = entry.Store.WithSnapshot(snapshotID)
	return entry
}

// BuildCityEntries creates CityEntry values for the given cities. The
// returned stores are auto-pinned to cfg.Hash() so unpinned reads
// (ListHexStats, ListCohortStats, ListForecastResults,
// LatestComputeResult) only see snapshots written by this same config
// — preventing slug-sharing examples (e.g. austin in both single-city
// and city-nerd) from reading each other's incompatible hex_id
// namespace. Callers that legitimately need cross-config reads can
// call entry.Store.WithConfigHash("") to clear the pin.
func BuildCityEntries(ctx context.Context, rootDB db.RootStorer, cfg *config.Config, cities []config.CityConfig) ([]CityEntry, error) {
	configHash := cfg.Hash()
	var entries []CityEntry
	var errs []string
	for _, city := range cities {
		id, err := rootDB.EnsureCity(ctx, city.Slug(), city.Name)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", city.Name, err))
			continue
		}
		entries = append(entries, CityEntry{
			Config: cfg,
			City:   city,
			Store:  rootDB.ForCity(id).WithConfigHash(configHash),
			Slug:   city.Slug(),
		})
	}
	if len(entries) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("no cities loaded: %s", errs[0])
	}
	return entries, nil
}

// BBoxAndCenter derives bbox and center from the stored boundary polygon.
func (entry CityEntry) BBoxAndCenter(ctx context.Context) ([4]float64, float64, float64, error) {
	boundaryGJSON, err := entry.Store.GetBoundary(ctx)
	if err != nil || boundaryGJSON == "" {
		return [4]float64{}, 0, 0, fmt.Errorf("%w for %s — run 'pvmt ingest' first", ErrNoBoundary, entry.City.Name)
	}
	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return [4]float64{}, 0, 0, err
	}
	lon, lat := geo.CenterFromBBox(bbox)
	return bbox, lon, lat, nil
}

// Info returns the frontend-facing metadata for this city. Callers decide
// whether to skip or fail when the boundary is missing.
func (entry CityEntry) Info(ctx context.Context) (CityInfo, error) {
	bbox, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return CityInfo{}, err
	}
	return CityInfo{
		Slug:      entry.Slug,
		Name:      entry.City.Name,
		BBox:      bbox,
		CenterLon: lon,
		CenterLat: lat,
	}, nil
}
