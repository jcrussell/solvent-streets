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

// ErrNoMatchingSnapshot signals that no snapshot for this city was computed
// under its current settings (Config.CityHash) — the city was never computed,
// or a setting that affects results (or pvmt's compute logic, ComputeEpoch)
// changed since. The exporter fails loud rather than emitting silent empty
// hex_stats / zero meta totals.
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
// snapshots include none with the city's current hash (Config.CityHash). This
// is the fail-loud signal that the city was never computed, or not since a
// setting that affects results changed — exporting would otherwise produce
// silent empty hex_stats and zero totals.
//
// A city with snapshots matching the current hash but with empty
// hex_stats (e.g. tiny city, all features below the sliver threshold)
// passes — that's a legitimate empty result, not a setup error.
func (entry CityEntry) RequireMatchingSnapshot(ctx context.Context) error {
	if entry.Config == nil {
		return nil
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
			"Run `pvmt --city %s all compute` (or a roads/parking/sidewalks compute) "+
				"under the current settings. The city's snapshot so far holds only %s.", entry.Slug, partNames(s))
	}
	err = fmt.Errorf("%w for %s", ErrNoMatchingSnapshot, entry.City.Name)
	if d := entry.Config.SourceDivergence(&entry.City); len(d) > 0 {
		// An [[include]]d city whose stored data this config would not have
		// computed: CityHash refused the source's stamp. Say why, since the fix
		// is usually in the config rather than a recompute.
		return hint.Withf(err,
			"%s is pulled in via [[include]], so it reads the snapshots its own config computed — "+
				"but this config resolves %s, which changes the stored results, so those snapshots "+
				"do not apply here. Make the including config agree with the city's own, or run "+
				"`pvmt --city %s all compute` under this config.",
			entry.City.Name, strings.Join(d, "; "), entry.Slug)
	}
	if db.StaleFor(snaps, configHash) {
		return hint.With(err, config.StaleSnapshotsHint(entry.Slug, configHash))
	}
	return hint.Withf(err, "%s has never been computed. Run `pvmt --city %s all compute` (after "+
		"`pvmt --city %s all ingest` if its data is not ingested yet).", entry.City.Name, entry.Slug, entry.Slug)
}

// MissingParts reports the parts the export of this city will lack although
// the city once had them: parts of its newest snapshot under OTHER settings
// (a different config hash) that no snapshot under the current hash has ever
// held. That is the trace of a settings change followed by a partial recompute —
// say `pvmt roads compute` after a hex_edge_m edit — which leaves the current
// snapshot without fire coverage, population or the combined total, and the
// export would otherwise publish the city without them, silently. (A part
// dropped under the current settings, because its producer found nothing to
// work from, was held by an earlier current-hash snapshot and is not reported:
// that drop is deliberate.) census is not reported for a city with census =
// false. prev is that other snapshot's id; missing is empty when nothing is
// missing or there is no other snapshot.
func (entry CityEntry) MissingParts(ctx context.Context) (missing []string, prev int64, err error) {
	if entry.Config == nil {
		return nil, 0, nil
	}
	snaps, err := entry.Store.ListSnapshots(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list snapshots for %s: %w", entry.City.Name, err)
	}
	hash := entry.Config.CityHash(&entry.City)
	held := map[string]bool{}
	var other *db.Snapshot
	for i := range snaps { // newest first
		s := &snaps[i]
		if s.ConfigHash == hash {
			for _, p := range s.Parts {
				held[p.Part] = true
			}
		} else if other == nil {
			other = s
		}
	}
	if other == nil || len(held) == 0 {
		return nil, 0, nil
	}
	for _, p := range other.Parts {
		if held[p.Part] || (p.Part == "census" && !entry.City.CensusEnabled()) {
			continue
		}
		missing = append(missing, p.Part)
	}
	return missing, other.ID, nil
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
		return [4]float64{}, 0, 0, fmt.Errorf("%w for %s — run 'pvmt all ingest' first", ErrNoBoundary, entry.City.Name)
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
