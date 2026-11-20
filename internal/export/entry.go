package export

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
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

// ErrNoSnapshot signals that a city has no snapshot to publish: it was never
// computed, or its latest compute was never frozen with `pvmt snapshots
// create`. Export refuses the whole site up front (PinNewest) rather than
// publish a city as an empty grid with zero totals.
var ErrNoSnapshot = errors.New("no snapshot")

// CityEntry holds the config and store for a single city.
type CityEntry struct {
	Config *config.Config
	City   config.CityConfig
	Store  db.Store
	Slug   string
	// Latest is the city's latest compute as serve read it at startup: what
	// its ?snapshot=latest view, and a city with no snapshot, show. nil when
	// not read (export).
	Latest []db.LatestPart
	// Snapshots is the city's snapshot list, newest first, as serve read it
	// at startup alongside the pin, so the picker names the snapshot it
	// serves. nil when not read (export).
	Snapshots []db.SnapshotCity
	// readsLatest marks an entry whose Store reads Latest
	// (LatestComputeEntry) rather than a snapshot.
	readsLatest bool
}

// ReadsLatest reports whether the entry reads its latest compute
// (LatestComputeEntry) rather than a snapshot.
func (entry CityEntry) ReadsLatest() bool { return entry.readsLatest }

// WithSnapshot returns a copy of this CityEntry whose Store is pinned to
// the given snapshot id. Snapshot-aware reads (compute results, hex stats,
// cohorts, forecasts, and the boundary, which reads the one the snapshot
// froze) on the returned store filter to that snapshot, and fail with
// db.ErrSnapshotGone once it is deleted; unaware reads (features, snapshot
// list) are unchanged.
func (entry CityEntry) WithSnapshot(snapshotID int64) CityEntry {
	entry.Store = entry.Store.WithSnapshot(snapshotID)
	return entry
}

// BuildCityEntries creates CityEntry values for the given cities. The returned
// stores read the latest compute under cfg.CityHash(city) until they are
// pinned (PinNewest, or WithSnapshot), so unpinned reads (per-hex coverage,
// cohort splits, city totals) only see runs written by the config that owns
// that city's data — preventing slug-sharing examples (e.g. Livermore in both
// livermore-ca and the bay-area-ca metro) from reading each other's
// incompatible hex_id namespace.
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
			Store:  rootDB.ForCity(id).WithLatestCompute(cfg.CityHash(&city)),
			Slug:   city.Slug(),
		})
	}
	if len(entries) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("no cities loaded: %s", errs[0])
	}
	return entries, nil
}

// NewestSnapshot returns the city's newest snapshot, whatever config hash it
// was created under: the one export and serve publish. nil when it has none.
func (entry CityEntry) NewestSnapshot(ctx context.Context) (*db.SnapshotCity, error) {
	snaps, err := entry.Store.ListSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("list snapshots for %s: %w", entry.City.Name, err)
	}
	if len(snaps) == 0 {
		return nil, nil //nolint:nilnil // nil snapshot = none
	}
	return &snaps[0], nil // newest first
}

// PinNewestSnapshot returns entry pinned to its newest snapshot
// (NewestSnapshot), or unchanged when it has none.
func (entry CityEntry) PinNewestSnapshot(ctx context.Context) (CityEntry, error) {
	snap, err := entry.NewestSnapshot(ctx)
	if err != nil || snap == nil {
		return entry, err
	}
	return entry.WithSnapshot(snap.ID), nil
}

// PinNewest pins every entry in place to its newest snapshot, so one export
// reads each city from that one snapshot throughout even if another is created
// meanwhile. It writes nothing and pins nothing unless every city has a
// snapshot: otherwise it returns one ErrNoSnapshot naming every city without
// one and the commands that make one. A city with neither a snapshot nor a
// stored boundary is left unpinned rather than refused when skipNoBoundary is
// set (the multi-city export skips such a city: its ingest failed). It warns
// on warn about every snapshot created under other settings than the current
// ones (SnapshotSettingsWarning) and notes a latest compute with parts not yet
// snapshotted.
func PinNewest(ctx context.Context, entries []CityEntry, skipNoBoundary bool, warn io.Writer) error {
	snaps := make([]*db.SnapshotCity, len(entries))
	var missing []string
	for i, entry := range entries {
		snap, refuse, err := entry.exportableSnapshot(ctx, skipNoBoundary)
		if err != nil {
			return err
		}
		snaps[i] = snap
		if refuse != "" {
			missing = append(missing, refuse)
		}
	}
	if len(missing) > 0 {
		noun := "city has"
		if len(missing) > 1 {
			noun = "cities have"
		}
		return hint.Withf(fmt.Errorf("%w to export: %d %s none", ErrNoSnapshot, len(missing), noun),
			"Export publishes each city's newest snapshot. Create one for each first:\n  %s",
			strings.Join(missing, "\n  "))
	}
	for i, entry := range entries {
		if snaps[i] == nil {
			continue
		}
		if err := entry.warnPublished(ctx, snaps[i], warn); err != nil {
			return err
		}
		entries[i] = entry.WithSnapshot(snaps[i].ID)
	}
	return nil
}

// PinSnapshot pins every entry in place to the snapshot named name, like
// PinNewest: it writes nothing and pins nothing unless the snapshot covers
// every city, and otherwise returns one ErrNoSnapshot naming each city it
// lacks. A city with neither the snapshot nor a stored boundary is left
// unpinned rather than refused when skipNoBoundary is set, as in PinNewest.
// It warns on warn about a city frozen under other settings than the current
// ones (SnapshotSettingsWarning). An unknown name is an error naming
// `pvmt snapshots list`.
func PinSnapshot(ctx context.Context, entries []CityEntry, name string, skipNoBoundary bool, warn io.Writer) error {
	snaps := make([]*db.SnapshotCity, len(entries))
	var missing []string
	for i, entry := range entries {
		snap, err := entry.namedSnapshot(ctx, name)
		if err != nil {
			return err
		}
		snaps[i] = snap
		if snap != nil {
			continue
		}
		skip, err := entry.skippable(ctx, skipNoBoundary)
		if err != nil {
			return err
		}
		if !skip {
			missing = append(missing, entry.City.Name)
		}
	}
	if err := notCovered(name, missing, len(entries)); err != nil {
		return err
	}
	for i, entry := range entries {
		if snaps[i] == nil {
			continue
		}
		if w := entry.SnapshotSettingsWarning(snaps[i]); w != "" {
			fmt.Fprintf(warn, "warning: %s; exporting it as created\n", w)
		}
		entries[i] = entry.WithSnapshot(snaps[i].ID)
	}
	return nil
}

// skippable reports whether a multi-city export (skipNoBoundary) leaves the
// city out rather than refusing it: it has no stored boundary, so its ingest
// failed.
func (entry CityEntry) skippable(ctx context.Context, skipNoBoundary bool) (bool, error) {
	if !skipNoBoundary {
		return false, nil
	}
	gj, err := entry.Store.GetBoundary(ctx)
	if err != nil {
		return false, fmt.Errorf("boundary for %s: %w", entry.City.Name, err)
	}
	return gj == "", nil
}

// notCovered is PinSnapshot's refusal when snapshot name lacks the missing
// cities (of total), nil when it lacks none.
func notCovered(name string, missing []string, total int) error {
	switch {
	case len(missing) == 0:
		return nil
	case len(missing) == total:
		return hint.Withf(fmt.Errorf("%w named %s covers any configured city", ErrNoSnapshot, name),
			"check 'pvmt snapshots list' for available names")
	}
	noun := "city"
	if len(missing) > 1 {
		noun = "cities"
	}
	return hint.Withf(fmt.Errorf("snapshot %s does not cover %d %s (%s): %w", name, len(missing), noun, strings.Join(missing, ", "), ErrNoSnapshot),
		"Snapshot %s does not cover every configured city. Export the cities it covers with --city, "+
			"or create a snapshot covering them all with `pvmt snapshots create <name>`.", name)
}

// namedSnapshot returns the city in the snapshot named name, nil when the
// snapshot does not cover it.
func (entry CityEntry) namedSnapshot(ctx context.Context, name string) (*db.SnapshotCity, error) {
	id, err := entry.Store.ResolveSnapshot(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil snapshot = not covered
	}
	if err != nil {
		return nil, fmt.Errorf("resolve snapshot %s for %s: %w", name, entry.City.Name, err)
	}
	snap, err := entry.Store.WithSnapshot(id).ViewSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s for %s: %w", name, entry.City.Name, err)
	}
	return snap, nil
}

// exportableSnapshot returns the city's newest snapshot or, when it has
// none, the line PinNewest's refusal lists for it ("" for a city the export
// skips for want of a boundary, when skipNoBoundary is set).
func (entry CityEntry) exportableSnapshot(ctx context.Context, skipNoBoundary bool) (snap *db.SnapshotCity, refuse string, err error) {
	if snap, err = entry.NewestSnapshot(ctx); err != nil || snap != nil {
		return snap, "", err
	}
	if skipNoBoundary {
		gj, err := entry.Store.GetBoundary(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("boundary for %s: %w", entry.City.Name, err)
		}
		if gj == "" {
			return nil, "", nil
		}
	}
	cmd, err := entry.createCommand(ctx)
	if err != nil {
		return nil, "", err
	}
	return nil, fmt.Sprintf("%s: %s", entry.City.Name, cmd), nil
}

// warnPublished writes the other-settings warning and the not-yet-snapshotted
// note for snap, the snapshot the city is published from.
func (entry CityEntry) warnPublished(ctx context.Context, snap *db.SnapshotCity, warn io.Writer) error {
	if w := entry.SnapshotSettingsWarning(snap); w != "" {
		fmt.Fprintf(warn, "warning: %s; exporting it as created\n", w)
	}
	note, err := entry.UnsnapshottedNote(ctx, snap)
	if err != nil {
		return err
	}
	if note != "" {
		fmt.Fprintf(warn, "note: %s\n", note)
	}
	return nil
}

// createCommand is the command that gives a city with no snapshot one:
// `snapshots create <name>` (which covers every city), after `all compute`
// when its latest compute under the current settings is empty too.
func (entry CityEntry) createCommand(ctx context.Context) (string, error) {
	create := "pvmt snapshots create <name>"
	if entry.Config == nil {
		return create, nil
	}
	latest, err := entry.Store.LatestCompute(ctx, entry.Config.CityHash(&entry.City))
	if err != nil {
		return "", fmt.Errorf("latest compute for %s: %w", entry.City.Name, err)
	}
	if len(latest) == 0 {
		return fmt.Sprintf("pvmt --city %s all compute && %s", entry.Slug, create), nil
	}
	return create, nil
}

// SnapshotSettingsWarning explains, in one line, that snap (a snapshot of this
// city) was created under other settings than the current ones (another
// Config.CityHash), naming the setting when an [[include]] is the cause
// (Config.SourceDivergence). "" when snap matches the current settings.
func (entry CityEntry) SnapshotSettingsWarning(snap *db.SnapshotCity) string {
	if entry.Config == nil || snap == nil {
		return ""
	}
	hash := entry.Config.CityHash(&entry.City)
	if snap.ConfigHash == hash {
		return ""
	}
	why := "a setting that affects results (hex_edge_m, [[services]] turnout_s, facilities, census, " +
		"a PVMT_HEX_EDGE_M override) or pvmt's compute logic changed since"
	if d := entry.Config.SourceDivergence(&entry.City); len(d) > 0 {
		why = "this config resolves " + strings.Join(d, "; ") + " differently from the city's own config"
	}
	return fmt.Sprintf("%s: snapshot %s was created under other settings (hash %s, now %s): %s; "+
		"run `pvmt --city %s all compute` and `pvmt snapshots create <name>` to publish the current ones",
		entry.City.Name, snap.Name, snap.ConfigHash, hash, why, entry.Slug)
}

// UnsnapshottedNote is a one-line note when the city's latest compute under
// the current settings holds parts its newest snapshot snap does not
// (db.UnsnapshottedParts), "" otherwise.
func (entry CityEntry) UnsnapshottedNote(ctx context.Context, snap *db.SnapshotCity) (string, error) {
	if entry.Config == nil {
		return "", nil
	}
	latest, err := entry.Store.LatestCompute(ctx, entry.Config.CityHash(&entry.City))
	if err != nil {
		return "", fmt.Errorf("latest compute for %s: %w", entry.City.Name, err)
	}
	parts := db.UnsnapshottedParts(latest, snap)
	if len(parts) == 0 {
		return "", nil
	}
	return fmt.Sprintf("%s: the latest compute has %s not yet snapshotted; run `pvmt snapshots create <name>` to publish it",
		entry.City.Name, strings.Join(parts, ", ")), nil
}

// RequirePavement fails when snap holds no pavement resource's compute, the
// data every page is built from. `pvmt snapshots create` refuses such a
// snapshot, so this is a defensive check, not a user-facing path.
func RequirePavement(entry CityEntry, snap *db.SnapshotCity) error {
	if snap == nil || hasPavement(*snap) {
		return nil
	}
	return fmt.Errorf("%s: snapshot %s has no pavement compute (it holds %s)", entry.City.Name, snap.Name, partNames(*snap))
}

// StaleSettings reports whether city has results that its current settings
// (cfg.CityHash) cannot read: nothing is computed under them (latest is the
// latest compute under the current hash) although the city has snapshots, or
// it is an [[include]]d city whose stored data this config would not have
// computed (CityHash refused the source's stamp). Config.StaleHint explains
// either case, and `pvmt status` and `pvmt forecast` decide whether to show it
// here.
func StaleSettings(cfg *config.Config, city *config.CityConfig, snaps []db.SnapshotCity, latest []db.LatestPart) bool {
	if len(latest) > 0 {
		return false
	}
	return len(snaps) > 0 || len(cfg.SourceDivergence(city)) > 0
}

// hasPavement reports whether the snapshot holds at least one pavement
// resource's compute, the data every page is built from.
func hasPavement(s db.SnapshotCity) bool {
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
func partNames(s db.SnapshotCity) string {
	names := make([]string, len(s.Parts))
	for i, p := range s.Parts {
		names[i] = p.Part
	}
	return strings.Join(names, ", ")
}

// HexEdge returns the hex edge, in meters, the city's grid is drawn at: the
// edge of the snapshot its store reads (the one it was computed on, so a
// snapshot keeps rendering on its own lattice after a hex_edge_m edit), or
// the config's resolved edge when the store reads no snapshot. A pinned
// snapshot that is gone is db.ErrSnapshotGone.
func (entry CityEntry) HexEdge(ctx context.Context) (float64, error) {
	snap, err := entry.Store.ViewSnapshot(ctx)
	if err != nil {
		return 0, fmt.Errorf("hex edge for %s: %w", entry.City.Name, err)
	}
	if snap != nil && snap.HexEdgeM > 0 {
		return snap.HexEdgeM, nil
	}
	if entry.Config == nil {
		return config.DefaultHexEdgeM, nil
	}
	return entry.Config.ResolvedHexEdge(&entry.City), nil
}

// HexEdges returns HexEdge for every entry, keyed by slug: the edges the
// Config tab shows (ResolvedTOMLWithEdges).
func HexEdges(ctx context.Context, entries []CityEntry) (map[string]float64, error) {
	edges := make(map[string]float64, len(entries))
	for _, entry := range entries {
		edge, err := entry.HexEdge(ctx)
		if err != nil {
			return nil, err
		}
		edges[entry.Slug] = edge
	}
	return edges, nil
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

// LatestComputeEntry returns entry pinned to latest, a latest compute read a
// moment ago (Store.WithRuns), in place of any snapshot pin: what serve shows
// for a city with no snapshot, and for ?snapshot=latest. Its reads fail with
// db.ErrSnapshotGone once gc sweeps any of those runs.
func LatestComputeEntry(entry CityEntry, latest []db.LatestPart) CityEntry {
	runs := make([]int64, len(latest))
	for i, p := range latest {
		runs[i] = p.RunID
	}
	entry.Store = entry.Store.WithSnapshot(0).WithRuns(runs)
	entry.Latest, entry.readsLatest = latest, true
	return entry
}
