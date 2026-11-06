package export

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// ErrNoBoundary signals that a city has no boundary stored. Callers that
// iterate over multiple cities (the multi-city exporter and live server)
// use errors.Is to skip the city rather than failing the whole export.
var ErrNoBoundary = errors.New("no boundary stored")

// ErrNoMatchingSnapshot signals that no snapshot for this city was written
// by the current config — typically because `pvmt compute` was skipped or
// run with a different config. The exporter fails loud rather than emitting
// silent empty hex_stats / zero meta totals.
var ErrNoMatchingSnapshot = errors.New("no snapshot matches current config hash")

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

// BuildCityEntries creates CityEntry values for the given cities. The returned
// stores are auto-pinned to cfg.CityHash(city) so unpinned reads (ListHexStats,
// ListCohortStats, LatestComputeResult) only see snapshots written by the
// config that owns that city's data — preventing slug-sharing examples (e.g.
// Livermore in both livermore-ca and the bay-area-ca metro) from reading each
// other's incompatible hex_id namespace. Callers that legitimately need
// cross-config reads can call entry.Store.WithConfigHash("") to clear the pin.
//
// "The config that owns the data" is per city, not per Config: a city pulled in
// through [[include]] is owned by the file that declared it. See
// Config.CityHash / Config.CityConfigID.
func BuildCityEntries(ctx context.Context, rootDB db.RootStorer, cfg *config.Config, cities []config.CityConfig) ([]CityEntry, error) {
	var entries []CityEntry
	var errs []string
	for _, city := range cities {
		// Both keys are resolved PER CITY, not once for the config. A city
		// pulled in through [[include]] belongs to the file that declared it,
		// so it reads the rows and snapshots that file's own ingest/compute
		// wrote. Hoisting either out of this loop is what made `make site`
		// impossible: a union config would look for its ~277 cities in a
		// namespace nothing had ever written to.
		id, err := rootDB.EnsureCity(ctx, city.Slug(), city.Name, cfg.CityConfigID(&city))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", city.Name, err))
			continue
		}
		entries = append(entries, CityEntry{
			Config: cfg,
			City:   city,
			Store:  rootDB.ForCity(id).WithConfigHash(cfg.CityHash(&city)),
			Slug:   city.Slug(),
		})
	}
	if len(entries) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("no cities loaded: %s", errs[0])
	}
	return entries, nil
}

// PinCurrentSnapshot returns entry pinned to the newest snapshot for its
// current config hash, so every later read sees that one snapshot even if a
// compute commits another meanwhile. Without such a snapshot (or a config) the
// entry is returned unchanged, and RequireMatchingSnapshot reports why.
func (entry CityEntry) PinCurrentSnapshot(ctx context.Context) (CityEntry, error) {
	if entry.Config == nil {
		return entry, nil
	}
	snaps, err := entry.Store.ListSnapshots(ctx)
	if err != nil {
		return entry, fmt.Errorf("list snapshots for %s: %w", entry.City.Name, err)
	}
	hash := entry.Config.CityHash(&entry.City)
	for _, s := range snaps { // newest first
		if s.ConfigHash == hash {
			return entry.WithSnapshot(s.ID), nil
		}
	}
	return entry, nil
}

// RequireMatchingSnapshot returns ErrNoMatchingSnapshot when the city's
// snapshots include none with the current config's hash. This is the
// fail-loud signal that `pvmt compute` was skipped (or run with a
// different config) for this city — exporting would otherwise produce
// silent empty hex_stats and zero totals.
//
// A city with snapshots matching the current hash but with empty
// hex_stats (e.g. tiny city, all features below the sliver threshold)
// passes — that's a legitimate empty result, not a setup error.
func (entry CityEntry) RequireMatchingSnapshot(ctx context.Context) error {
	if entry.Config == nil {
		return nil
	}
	if err := entry.RequireMatchingHexEdge(); err != nil {
		return err
	}
	configHash := entry.Config.CityHash(&entry.City)
	snaps, err := entry.Store.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots for %s: %w", entry.City.Name, err)
	}
	for _, s := range snaps { // newest first: the snapshot the export pins
		if s.ConfigHash != configHash {
			continue
		}
		if hasPavement(s) {
			return nil
		}
		// Only metric parts (coverage, census) under this config: exporting
		// would publish a city with no pavement at all, at exit 0.
		return hint.Withf(fmt.Errorf("%w for %s: snapshot %d has no pavement compute", ErrNoMatchingSnapshot, entry.City.Name, s.ID),
			"Run `pvmt all compute` (or a roads/parking/sidewalks compute) for %s under "+
				"the current config. The config's snapshot so far holds only %s.", entry.Slug, partNames(s))
	}
	return hint.Withf(fmt.Errorf("%w for %s", ErrNoMatchingSnapshot, entry.City.Name),
		"Run `pvmt all compute` (or `pvmt compute --city %s`) to produce a snapshot "+
			"matching the current config hash %s. If snapshots exist with other hashes "+
			"(e.g. after editing hex_edge_m or a forecast knob), `pvmt snapshots ls --city %s` "+
			"will show them.", entry.Slug, configHash, entry.Slug)
}

// hasPavement reports whether the snapshot holds at least one pavement
// resource's compute, the data every page is built from.
func hasPavement(s db.Snapshot) bool {
	for _, p := range s.Parts {
		for _, rt := range resource.All {
			if p.Part == string(rt.Type()) {
				return true
			}
		}
	}
	return false
}

// partNames lists a snapshot's parts for a hint ("service:fire, census").
func partNames(s db.Snapshot) string {
	names := make([]string, len(s.Parts))
	for i, p := range s.Parts {
		names[i] = p.Part
	}
	return strings.Join(names, ", ")
}

// RequireMatchingHexEdge catches the one merge outcome that corrupts an export
// without erroring anywhere.
//
// An included city reads the snapshot its SOURCE config computed, but its
// effective calibration is resolved by the config doing the reading, and the
// [[include]] merge unions calibration PER FIELD with first-to-set winning. So
// a city contributed by one include can take hex_edge_m from a different one,
// or inherit a union's top-level [grid] the source never had.
//
// That specific field is unforgiving. Hex ids are derived from the grid, and
// the exporter rebuilds the grid from config and joins the stored hex_stats to
// it BY ID (buildHexFeature). A different edge produces ids that match nothing,
// so every feature is dropped and the city exports an empty heatmap — no error,
// no warning, just a blank map that looks like a city with no data.
//
// The snapshot-hash check cannot see this: the hash is the SOURCE's, and it
// matches. So compare the resolved edges directly. Zero means unstamped (a
// directly-declared city), which is not an included city and needs no check.
//
// Exported because it is NOT specific to the export path: it is orthogonal to
// the snapshot-hash question, applies to the latest snapshot as much as to a
// pinned one, and `pvmt serve` needs it too — serve rebuilds the same grid from
// the same config and would otherwise answer HTTP 200 with an empty hexgrid.
func (entry CityEntry) RequireMatchingHexEdge() error {
	// Nil Config means nothing to resolve against (some tests, and any entry
	// not built by BuildCityEntries). Match RequireMatchingSnapshot and
	// snapshotMatchesConfig, which both treat that as "no opinion" — this is
	// now called directly by the server, ahead of both of those guards.
	if entry.Config == nil {
		return nil
	}
	want := entry.City.SourceHexEdgeM
	if want == 0 {
		return nil
	}
	got := entry.Config.ResolvedHexEdge(&entry.City)
	if got == want {
		return nil
	}
	return hint.Withf(
		fmt.Errorf("%w for %s: hex_edge_m resolves to %g here but its data was computed at %g",
			ErrNoMatchingSnapshot, entry.City.Name, got, want),
		"%s is pulled in via [[include]], so it reads the snapshots its own config computed — "+
			"but this config resolves a different hex edge, and hex ids are derived from the grid, "+
			"so every stored hex would fail to match and the city would export a blank map. "+
			"Either drop the conflicting hex_edge_m (a top-level [grid] in the including file "+
			"applies to every city that has no override of its own), or recompute %s under this config.",
		entry.City.Name, entry.Slug)
}

// BBoxAndCenter derives bbox and center from the stored boundary polygon.
//
// A genuinely-missing boundary (GetBoundary returns "", nil) is reported as
// ErrNoBoundary so multi-city callers can skip the city. A real GetBoundary
// error (e.g. a transient SQLITE_BUSY) is propagated as-is rather than being
// masked as ErrNoBoundary — otherwise a transient DB hiccup at first render
// would be indistinguishable from an unconfigured city and get baked into the
// lifetime cache (see buildIndexData's dropdown loop and serveBoundaryGeoJSON).
func (entry CityEntry) BBoxAndCenter(ctx context.Context) ([4]float64, float64, float64, error) {
	boundaryGJSON, err := entry.Store.GetBoundary(ctx)
	if err != nil {
		return [4]float64{}, 0, 0, fmt.Errorf("boundary for %s: %w", entry.City.Name, err)
	}
	if boundaryGJSON == "" {
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
		Slug:   entry.Slug,
		Name:   entry.City.Name,
		BBox:   geoJSONBBox(bbox),
		Center: [2]float64{lon, lat},
		// EffectiveTags adds the derived state:XX tag; this is the one place
		// the derivation is applied, so cities.json, PVMT_CONFIG.cities, the
		// selector groups and /api/cities all see the same tags.
		Tags: entry.City.EffectiveTags(),
	}, nil
}
