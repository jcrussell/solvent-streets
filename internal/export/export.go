package export

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/fsutil"
	"github.com/jcrussell/solvent-streets/internal/geo"
)

// --- Exporter (static site generation) ---

type Exporter struct {
	entries    []CityEntry
	cfg        *config.Config
	outputDir  string
	unitSystem string
	errOut     io.Writer // progress/warning sink; nil means discard
	assets     *Assets   // site-root browser files written beside index.html
	pinned     bool      // Pin has run
	snapshot   string    // SetSnapshot: the snapshot to publish; "" = each city's newest
}

// SetErrOut routes the exporter's progress and warning output (chatter, not
// data). Unset, warnings are discarded.
func (e *Exporter) SetErrOut(w io.Writer) { e.errOut = w }

// warnOut returns the warning sink, defaulting to io.Discard when unset so
// build tools and tests stay silent without special-casing.
func (e *Exporter) warnOut() io.Writer {
	if e.errOut == nil {
		return io.Discard
	}
	return e.errOut
}

// New returns an Exporter that writes entries' site into outputDir, with
// assets (EmbeddedAssets in production) at its root.
func New(entries []CityEntry, cfg *config.Config, outputDir, unitSystem string, assets *Assets) *Exporter {
	return &Exporter{entries: entries, cfg: cfg, outputDir: outputDir, unitSystem: unitSystem, assets: assets}
}

// SetSnapshot makes the export publish the snapshot named name
// (PinSnapshot) instead of each city's newest. "" restores the newest.
func (e *Exporter) SetSnapshot(name string) { e.snapshot = name }

// Pin pins every city for the whole export: to the snapshot SetSnapshot
// named (PinSnapshot), else to its newest (PinNewest), warning on the
// exporter's warning sink, and refuses the export, before anything is
// written, when any city has none. Run calls it when the caller has not, so
// a caller that must not touch the output directory before the refusal (pvmt
// export's --clean) calls it first.
func (e *Exporter) Pin(ctx context.Context) error {
	if e.pinned {
		return nil
	}
	// One snapshot per city: each read would otherwise resolve on its own,
	// and a snapshot created mid-export could leave a city's files (and the
	// multi-city totals) built from two different snapshots.
	skip := len(e.entries) > 1
	pin := func() error { return PinNewest(ctx, e.entries, skip, e.warnOut()) }
	if e.snapshot != "" {
		pin = func() error { return PinSnapshot(ctx, e.entries, e.snapshot, skip, e.warnOut()) }
	}
	if err := pin(); err != nil {
		return err
	}
	e.pinned = true
	return nil
}

func (e *Exporter) Run(ctx context.Context) error {
	if err := e.Pin(ctx); err != nil {
		return err
	}
	if len(e.entries) == 1 {
		return e.runSingleCity(ctx)
	}
	return e.runMultiCity(ctx)
}

func (e *Exporter) runSingleCity(ctx context.Context) error {
	entry := e.entries[0]
	dataDir := filepath.Join(e.outputDir, DataDirName)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := e.writeExportMarker(); err != nil {
		return err
	}
	if err := e.writeNojekyll(); err != nil {
		return err
	}

	// exportCityData already builds the MetaJSON and forecast seed it writes to
	// data/meta.json + data/forecast_seed.json; reuse them for index.html rather
	// than recomputing both (a second round of DB queries and boundary parses).
	meta, seed, err := e.exportCityData(ctx, entry, dataDir)
	if err != nil {
		return err
	}

	if err := e.assets.WriteTo(e.outputDir); err != nil {
		return err
	}

	return e.renderHTML(ctx, meta, seed, nil)
}

// exportOneCity writes one sub-city's data directory and returns its
// CityInfo. Skips (kept=false, no error) WITHOUT creating the directory when
// the city has no boundary stored — typically because ingest tripped a hard error like NYC's
// water-strip backstop or Nominatim returning a Point. The regional
// aggregation helpers (regionBBox, summedBoundaryArea, etc.) already
// tolerate missing boundaries via continue-on-error.
func (e *Exporter) exportOneCity(ctx context.Context, entry CityEntry) (CityInfo, bool, error) {
	// Decide whether to skip BEFORE creating the directory, not after. If
	// MkdirAll ran first, a skipped city would leave cities/<slug>/data/
	// behind, empty — and checksite's discover.go enumerates every directory
	// under cities/ regardless of what cities.json lists, so `pvmt check-site`
	// would fail on a city the exporter had deliberately dropped. Ordering
	// prevents this rather than a RemoveAll in the skip branch, so a future
	// early return cannot leave a ghost directory by forgetting to clean up.
	//
	// Info is the same BBoxAndCenter lookup exportCityData does first, and it
	// is where ErrNoBoundary originates (entry.go:178) — before anything is
	// written. Doing it up front also means ErrNoBoundary is handled in one
	// branch.
	info, listed, err := entry.listedInfo(ctx)
	if err != nil {
		return CityInfo{}, false, err
	}
	if !listed {
		fmt.Fprintf(e.warnOut(), "  skipping %s: no boundary stored (ingest failed earlier)\n", entry.Slug)
		return CityInfo{}, false, nil
	}

	cityDataDir := CityDataDir(e.outputDir, entry.Slug)
	if err := os.MkdirAll(cityDataDir, 0o755); err != nil {
		return CityInfo{}, false, fmt.Errorf("create city dir %s: %w", entry.Slug, err)
	}
	// Multi-city discards the per-city meta/seed: the dashboard renders
	// config-wide aggregates (BuildMultiCityMeta / BuildMultiCityForecastSeed),
	// and exportOneCity only needs the per-city bbox/center via entry.Info.
	if _, _, err := e.exportCityData(ctx, entry, cityDataDir); err != nil {
		return CityInfo{}, false, fmt.Errorf("export %s: %w", entry.Slug, err)
	}
	return info, true, nil
}

// writeCompareRollup writes compare.json for the exported cities, built from
// the files just written so each value is the one its city's file carries.
func (e *Exporter) writeCompareRollup(cities []CityInfo) error {
	compare := CompareJSON{V: CompareFileVersion, Cities: make([]CompareCity, 0, len(cities))}
	for _, c := range cities {
		cc, err := compareCityFromDir(c.Slug, CityDataDir(e.outputDir, c.Slug))
		if err != nil {
			return fmt.Errorf("build %s for %s: %w", CompareFileName, c.Slug, err)
		}
		compare.Cities = append(compare.Cities, cc)
	}
	if err := writeJSON(filepath.Join(e.outputDir, CompareFileName), compare); err != nil {
		return fmt.Errorf("write %s: %w", CompareFileName, err)
	}
	return nil
}

func (e *Exporter) runMultiCity(ctx context.Context) error {
	if err := os.MkdirAll(e.outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := e.writeExportMarker(); err != nil {
		return err
	}
	if err := e.writeNojekyll(); err != nil {
		return err
	}

	var cities []CityInfo
	for _, entry := range e.entries {
		info, kept, err := e.exportOneCity(ctx, entry)
		if err != nil {
			return err
		}
		if kept {
			cities = append(cities, info)
		}
	}

	// Write cities.json, in the order and with the [] (never null) that
	// /api/cities serves through CityInfos.
	if cities == nil {
		cities = []CityInfo{}
	}
	sortCitiesByName(cities)
	if err := writeJSON(filepath.Join(e.outputDir, CitiesFileName), cities); err != nil {
		return fmt.Errorf("write %s: %w", CitiesFileName, err)
	}
	if err := e.writeCompareRollup(cities); err != nil {
		return err
	}

	if err := e.assets.WriteTo(e.outputDir); err != nil {
		return err
	}

	// Render the dashboard index against the config-wide meta and forecast
	// seed (PageMetaAndSeed).
	meta, seed, err := PageMetaAndSeed(ctx, e.cfg, e.entries, SiteTitle(e.cfg, filepath.Base(e.outputDir)))
	if err != nil {
		return err
	}
	return e.renderHTML(ctx, meta, seed, cities)
}

// listedInfo is the one rule for which cities a multi-city site lists: a city
// with no boundary stored is left out (listed=false), and any other failure
// to read it is an error, so a transient database error fails the export or
// the request rather than quietly publishing a site without the city.
func (entry CityEntry) listedInfo(ctx context.Context) (info CityInfo, listed bool, err error) {
	info, err = entry.Info(ctx)
	switch {
	case errors.Is(err, ErrNoBoundary):
		return CityInfo{}, false, nil
	case err != nil:
		return CityInfo{}, false, fmt.Errorf("city %s info: %w", entry.Slug, err)
	}
	return info, true, nil
}

// CityInfos returns the cities a multi-city site lists, in selector order
// (sortCitiesByName): cities.json, the pages' CITIES array and /api/cities all
// carry this list. unlisted, when non-nil, is told about each city left out
// for want of a boundary. The result is never nil, so it marshals as [].
func CityInfos(ctx context.Context, entries []CityEntry, unlisted func(CityEntry)) ([]CityInfo, error) {
	cities := make([]CityInfo, 0, len(entries))
	for _, entry := range entries {
		info, listed, err := entry.listedInfo(ctx)
		if err != nil {
			return nil, err
		}
		if !listed {
			if unlisted != nil {
				unlisted(entry)
			}
			continue
		}
		cities = append(cities, info)
	}
	sortCitiesByName(cities)
	return cities, nil
}

// sortCitiesByName orders the city selector alphabetically by Name
// (compareCityNames). Stable, so exact ties keep config order.
func sortCitiesByName(cities []CityInfo) {
	slices.SortStableFunc(cities, func(a, b CityInfo) int { return compareCityNames(a.Name, b.Name) })
}

// compareCityNames orders city names the way a reader alphabetizes them:
// case-insensitively (a byte sort would put "apple" after "Banana") by the
// place, the name before its last ", " ("Bell" of "Bell, CA"), then by what
// follows it (the state). Comparing whole names would put "Bell Gardens, CA"
// before "Bell, CA", since ' ' sorts before ','. selectors.js byName is the
// same rule for the client's rebuilt selector.
func compareCityNames(a, b string) int {
	ap, as := splitCityName(strings.ToLower(a))
	bp, bs := splitCityName(strings.ToLower(b))
	if c := cmp.Compare(ap, bp); c != 0 {
		return c
	}
	return cmp.Compare(as, bs)
}

// splitCityName splits "place, ST" at its last ", " into place and state; a
// name with none is all place.
func splitCityName(name string) (place, state string) {
	if place, state, ok := strings.CutLast(name, ", "); ok {
		return place, state
	}
	return name, ""
}

// exportCityData writes a city's data directory and returns the MetaJSON and
// forecast seed it built along the way, so a caller rendering index.html for
// the same city (runSingleCity) reuses them instead of recomputing — each
// rebuild is a fresh round of DB queries plus boundary re-parsing.
func (e *Exporter) exportCityData(ctx context.Context, entry CityEntry, dataDir string) (MetaJSON, template.JS, error) {
	snap, err := entry.Store.ViewSnapshot(ctx)
	if err != nil {
		return MetaJSON{}, "", fmt.Errorf("snapshot for %s: %w", entry.City.Name, err)
	}
	if err := RequirePavement(entry, snap); err != nil {
		return MetaJSON{}, "", err
	}
	bbox, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return MetaJSON{}, "", fmt.Errorf("city bbox: %w", err)
	}
	proj := geo.NewUTMProjector(lon, lat)
	if err := e.warnGrid(ctx, entry, bbox, proj); err != nil {
		return MetaJSON{}, "", err
	}
	// One lattice and clip for the three files that describe the drawn grid:
	// hexgrid.geojson draws it, and meta.json's metric summaries and
	// hex-metrics.json cover exactly its hexes, reading each stored metric's
	// rows once between them (keepRows).
	grid := NewDrawnGrid(ctx, entry, proj)
	grid.keepRows()

	meta, err := BuildMetaOn(ctx, entry, 0, grid)
	if err != nil {
		return MetaJSON{}, "", err
	}

	// The per-city data files written below (and in exportScenariosForCity)
	// are enumerated for the publish-readiness checker by AllDataFileNames
	// (DataFileNames + OptionalDataFileNames) in checkassets.go — keep the two in sync when adding or removing a file.
	//
	if err := e.writeBoundary(ctx, entry, proj, dataDir); err != nil {
		return MetaJSON{}, "", err
	}

	// Export hex grid — a single multi-scope file, one feature per hex with
	// nested {all, city|city_same} coverage. This is the largest file the
	// export writes; a feature with NEITHER "city" nor "city_same" signals
	// "hide the scope toggle" to the client.
	//
	// The /play board reads its road area and decay rate (k) from this file
	// too, so there is no separate board file.
	hexFC, err := BuildHexGeoJSONOn(ctx, entry, grid)
	if err != nil {
		return MetaJSON{}, "", fmt.Errorf("build hexgrid: %w", err)
	}
	if hexFC != nil {
		if err := writeJSON(filepath.Join(dataDir, HexGridFileName), hexFC); err != nil {
			return MetaJSON{}, "", fmt.Errorf("write hexgrid: %w", err)
		}
	}

	// Write meta.json
	if err := writeJSON(filepath.Join(dataDir, MetaFileName), meta); err != nil {
		return MetaJSON{}, "", fmt.Errorf("write meta: %w", err)
	}

	if err := writeHexMetrics(ctx, entry, grid, dataDir); err != nil {
		return MetaJSON{}, "", err
	}

	// Export forecast and scenario data
	if err := exportScenariosForCity(ctx, entry, dataDir); err != nil {
		return MetaJSON{}, "", fmt.Errorf("export scenarios: %w", err)
	}

	// Export forecast seed for interactive WASM controls (per-city)
	forecastCfg := entry.Config.ResolvedForecast(&entry.City)
	seed, err := BuildForecastSeed(ctx, &forecastCfg, entry.Store)
	if err != nil {
		return MetaJSON{}, "", fmt.Errorf("build forecast seed: %w", err)
	}
	if err := fsutil.WriteFile(filepath.Join(dataDir, ForecastSeedFileName), []byte(seed), 0o644); err != nil {
		return MetaJSON{}, "", fmt.Errorf("write forecast_seed.json: %w", err)
	}

	return meta, seed, nil
}

// writeHexMetrics writes the optional hex-metrics.json (fire response, …):
// only when a metric has rows (OptionalDataFileNames), so a city that never
// ran `pvmt services compute` publishes without the file and check-site is
// content either way.
func writeHexMetrics(ctx context.Context, entry CityEntry, grid *DrawnGrid, dataDir string) error {
	hm, err := BuildHexMetricsOn(ctx, entry, grid)
	if err != nil {
		return fmt.Errorf("build hex metrics: %w", err)
	}
	if hm == nil {
		return nil
	}
	if err := writeJSON(filepath.Join(dataDir, HexMetricsFileName), hm); err != nil {
		return fmt.Errorf("write hex-metrics: %w", err)
	}
	return nil
}

func exportScenariosForCity(ctx context.Context, entry CityEntry, dataDir string) error {
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := ConvertCostTiers(&fc)

	forecasts, err := BuildForecastsForCity(ctx, entry, &fc, costTiers)
	if err != nil {
		return fmt.Errorf("build forecasts: %w", err)
	}

	if len(forecasts) > 0 {
		// Round for emission only, on a copy: BuildHexCostSummary below derives
		// from `forecasts` and must see full precision.
		if err := writeJSON(filepath.Join(dataDir, ForecastFileName), RoundForecastsForEmission(forecasts)); err != nil {
			return fmt.Errorf("write forecast.json: %w", err)
		}

		hexCostSummary := BuildHexCostSummary(forecasts)
		if err := writeJSON(filepath.Join(dataDir, HexCostSummaryFileName), hexCostSummary); err != nil {
			return fmt.Errorf("write hex-cost-summary.json: %w", err)
		}

		scenariosOut, err := BuildScenariosData(ctx, entry, &fc)
		if err != nil {
			return fmt.Errorf("build scenarios: %w", err)
		}
		if err := writeJSON(filepath.Join(dataDir, ScenariosFileName), RoundScenariosForEmission(scenariosOut)); err != nil {
			return fmt.Errorf("write scenarios.json: %w", err)
		}
	}

	return nil
}

// warnGrid warns about a city whose map will be distorted or empty.
//
// A single UTM zone is picked from the bbox center; a bbox much wider than
// one 6° zone gets distorted areas near its edges. Warn so an oversized city
// isn't silently exported with skewed metrics (bbox is [south,west,north,
// east], so the lon span is east-west = bbox[3]-bbox[1]). And min_hex_area is
// checked against the edge the snapshot is drawn at (sliverThresholdWarning).
func (e *Exporter) warnGrid(ctx context.Context, entry CityEntry, bbox [4]float64, proj *geo.UTMProjector) error {
	if geo.BBoxExceedsUTMZone(bbox) {
		fmt.Fprintf(e.warnOut(),
			"warning: city %q bbox spans %.1f° of longitude; a single UTM zone (%d) is used, so exported areas near the edges may be distorted\n",
			entry.City.Name, bbox[3]-bbox[1], proj.Zone)
	}
	edge, err := entry.HexEdge(ctx)
	if err != nil {
		return err
	}
	fmt.Fprint(e.warnOut(), sliverThresholdWarning(entry, edge))
	return nil
}

// ResolvedTOML returns the config serialized as TOML with all defaults filled in.
func ResolvedTOML(cfg *config.Config) string { return ResolvedTOMLWithEdges(cfg, nil) }

// ResolvedTOMLWithEdges is ResolvedTOML with each city's hex_edge_m taken
// from hexEdges (slug -> edge; see HexEdges) where it has one: the edge of the
// snapshot the site publishes, which is what its map is drawn at, rather than
// the edge the config has now.
func ResolvedTOMLWithEdges(cfg *config.Config, hexEdges map[string]float64) string {
	resolved := resolvedConfig(cfg, hexEdges)
	return encodeResolvedTOML(resolved)
}

// CityResolvedTOML returns the settings one city's figures are computed
// under: its [[cities]] entry as ResolvedTOMLWithEdges shows it (resolved hex
// edge, sliver threshold and forecast), with hexEdge the edge its published
// snapshot is drawn at, plus the display units and any service overrides. A
// multi-city site's Config tab shows this for the city in view (meta.json
// settings_toml) instead of the whole combined file.
func CityResolvedTOML(cfg *config.Config, city config.CityConfig, hexEdge float64) string {
	one := *cfg
	one.Cities = []config.CityConfig{city}
	var edges map[string]float64
	if hexEdge > 0 {
		edges = map[string]float64{city.Slug(): hexEdge}
	}
	resolved := resolvedConfig(&one, edges)
	view := struct {
		Display struct {
			Units string `toml:"units"`
		} `toml:"display"`
		Services []config.ServiceOverride `toml:"services,omitempty"`
		Cities   []config.CityConfig      `toml:"cities"`
	}{Services: resolved.Services, Cities: resolved.Cities}
	view.Display.Units = resolved.Display.Units
	return encodeResolvedTOML(view)
}

// encodeResolvedTOML encodes a resolved config view for the Config tab.
func encodeResolvedTOML(v any) string {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return "# error encoding config"
	}
	return stripZeroCurrentBudget(buf.String())
}

// resolvedConfig is cfg with every default filled in, as the Config tab shows
// it (ResolvedTOMLWithEdges). cfg is not mutated.
func resolvedConfig(cfg *config.Config, hexEdges map[string]float64) config.Config {
	resolved := *cfg

	// ConfigID is internal plumbing for cities-table keying — not a
	// user-visible setting. Strip it so the published site's Config tab
	// doesn't expose either the user's explicit ID or the auto-computed
	// host-path-derived hash.
	resolved.ConfigID = ""

	if resolved.Grid.HexEdgeM <= 0 {
		resolved.Grid.HexEdgeM = config.DefaultHexEdgeM
	}
	// Display defaults: resolve via UnitSystem() so PVMT_UNITS env wins
	// over an empty file value (same precedence config show --sources uses).
	resolved.Display.Units = cfg.UnitSystem().String()
	if resolved.Display.MinHexArea <= 0 {
		resolved.Display.MinHexArea = config.DefaultMinHexArea
	}
	// Export defaults resolve lazily through the accessors, and ExportConfig
	// emits both fields unconditionally (no omitempty), so an unset [export]
	// block published `coordinate_decimals = 0` / `boundary_simplify_m = 0.0`
	// — sentinel zeros the pipeline never uses, contradicting the documented
	// defaults of 5 and 10 while [grid]/[display]/[forecast] all materialized
	// theirs. Go through the accessors so the negative byte-exact opt-out for
	// boundary_simplify_m survives (it keys on zero-vs-nonzero, not positive).
	resolved.Export.CoordinateDecimals = cfg.CoordinateDecimals()
	resolved.Export.BoundarySimplifyM = cfg.BoundarySimplifyM()
	config.NormalizeForecast(&resolved.Forecast)
	effectiveForecast(&resolved.Forecast)

	// Per-city calibration: with [[include]] the top-level grid/forecast are
	// empty and each city carries its own flattened hex_edge/forecast/tags
	// (from the include merge). Populate every city's effective, resolved
	// values so the Config tab reflects what the pipeline actually used, not a
	// parent-only view that hides per-metro overrides. Copy the slice first so
	// we never mutate the live config.
	if len(resolved.Cities) > 0 {
		cities := make([]config.CityConfig, len(resolved.Cities))
		copy(cities, resolved.Cities)
		for i := range cities {
			rf := cfg.ResolvedForecast(&cities[i])
			cities[i].HexEdgeM = cfg.ResolvedHexEdge(&cities[i])
			if edge, ok := hexEdges[cities[i].Slug()]; ok && edge > 0 {
				cities[i].HexEdgeM = edge
			}
			// min_hex_area is flattened per-city by the include merge like
			// hex_edge_m, and CityConfig emits it unconditionally (no
			// omitempty), so without this every city would publish a
			// fabricated `min_hex_area = 0.0` next to a top level showing the
			// resolved default.
			cities[i].MinHexArea = cfg.ResolvedMinHexArea(&cities[i])
			effectiveForecast(&rf)
			cities[i].Forecast = &rf
		}
		resolved.Cities = cities
	}
	return resolved
}

// effectiveForecast fills the forecast settings config leaves for the
// forecast package to default (applyForecastDefaults) with the values the
// forecast runs on, as the forecast seed resolves them (marshalForecastSeed):
// the default decay rate, treatment cycle and cost tiers when unset. Without
// it the Config tab, which says it shows defaults filled in, published
// `decay_rate = 0.0` and `treatment_cycle_years = 0.0` and no cost tiers for
// every city that left them to the defaults. growth_rate needs nothing: an
// unset growth rate is a growth of 0, which is what it shows. fc's cost tiers
// are replaced, not appended to, so a shared slice is never written.
func effectiveForecast(fc *config.ForecastConfig) {
	fc.DecayRate = forecast.ResolveDecayRate(fc.DecayRate)
	fc.TreatmentCycleYears = forecast.ResolveCycleYears(fc.TreatmentCycleYears)
	if len(fc.CostTiers) == 0 {
		tiers := make([]config.CostTierCfg, 0, len(forecast.DefaultCostTiers))
		for _, t := range forecast.DefaultCostTiers {
			tiers = append(tiers, config.CostTierCfg{
				MinPCI:     t.MinPCI,
				MaxPCI:     t.MaxPCI,
				CostPerSqM: t.CostPerSqM,
				Label:      t.Label,
			})
		}
		fc.CostTiers = tiers
	}
}

// stripZeroCurrentBudget removes `current_budget = 0.0` lines from encoded
// TOML. current_budget uses 0 as a "not provided" sentinel, but BurntSushi's
// isEmpty (encode.go) has no float case, so the `,omitempty` tag is decorative
// — a zero value is always emitted. Removing it here keeps the published Config
// tab from showing a fabricated $0 budget for uncalibrated cities. Operating on
// the encoded text (rather than zeroing struct fields) also covers the per-city
// [[cities.forecast]] blocks without mutating the caller's shared *ForecastConfig
// pointers, which `resolved := *cfg` aliases.
func stripZeroCurrentBudget(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, ln := range lines {
		if key, val, ok := strings.Cut(ln, "="); ok && strings.TrimSpace(key) == "current_budget" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil && f == 0 {
				continue
			}
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// writeBoundary writes the city's display boundary, if it has one.
//
// A nil FC means no boundary is stored — skip the file, exactly as the inline
// GetBoundary check this replaced did. A non-nil error ALONGSIDE a usable FC
// means simplification was skipped and the raw geometry is going out (see
// BuildBoundaryGeoJSON's error contract); that is a warning, not a failure,
// because every shape it fires on exports fine today. Only a write failure
// aborts the export.
func (e *Exporter) writeBoundary(ctx context.Context, entry CityEntry, proj *geo.UTMProjector, dataDir string) error {
	fc, simplifyErr := BuildBoundaryGeoJSON(ctx, entry, proj)
	if fc == nil {
		return nil
	}
	if simplifyErr != nil {
		fmt.Fprintf(e.warnOut(),
			"warning: city %q boundary could not be simplified, writing it in full: %v\n",
			entry.City.Name, simplifyErr)
	}
	if err := writeJSON(filepath.Join(dataDir, BoundaryFileName), fc); err != nil {
		return fmt.Errorf("write boundary geojson: %w", err)
	}
	return nil
}

// writeExportMarker drops a fsutil.ExportMarkerName sentinel at the top of the
// output dir at the very start of a run. index.html is only written last, so a
// run that fails partway (e.g. a read error on city N of M) would
// otherwise leave a non-empty tree that SafeCleanDir refuses to remove; the
// early marker keeps such a partial export recoverable via --clean.
func (e *Exporter) writeExportMarker() error {
	path := filepath.Join(e.outputDir, fsutil.ExportMarkerName)
	if err := fsutil.WriteFile(path, []byte("pvmt export in progress\n"), 0o644); err != nil {
		return fmt.Errorf("write export marker: %w", err)
	}
	return nil
}

// writeNojekyll drops a zero-byte .nojekyll at the output root. GitHub Pages
// runs the published tree through Jekyll otherwise, which is slow on a large
// site and silently drops files/dirs whose names start with "_". check-site
// requires it (checkNojekyll fails on a missing or non-zero-byte file), so the
// exporter emits a publish-ready tree on its own — this was gensite's job
// before the site consolidated to a single `pvmt export`. Written next to the
// export marker (post-MkdirAll) so the output dir already exists.
func (e *Exporter) writeNojekyll() error {
	path := filepath.Join(e.outputDir, NojekyllFileName)
	if err := fsutil.WriteFile(path, nil, 0o644); err != nil {
		return fmt.Errorf("write .nojekyll: %w", err)
	}
	return nil
}

func (e *Exporter) renderHTML(ctx context.Context, meta MetaJSON, seed template.JS, cities []CityInfo) error {
	hexEdges, err := HexEdges(ctx, e.entries)
	if err != nil {
		return err
	}
	// IsLiveServer and ActiveSlug stay zero: this is the static export, so the
	// game page resolves the active city client-side from ?city=.
	td, err := NewTemplateData(e.cfg, meta, seed, cities, e.unitSystem, hexEdges)
	if err != nil {
		return err
	}

	// Render both pages before writing either, so a game-template failure
	// can't leave a partial site (index.html present, play.html missing).
	index, err := RenderPage(ParseIndexTemplate, td)
	if err != nil {
		return err
	}
	play, err := RenderPage(ParseGameTemplate, td)
	if err != nil {
		return err
	}
	if err := fsutil.WriteFile(filepath.Join(e.outputDir, IndexFileName), index, 0o644); err != nil {
		return err
	}
	return fsutil.WriteFile(filepath.Join(e.outputDir, PlayFileName), play, 0o644)
}

// writeJSON writes minified JSON (no indentation). Indentation is pure weight
// on a published site — it was the majority of boundary.geojson's bytes — and
// buys nothing a formatter can't give back on demand. It also removes a
// whitespace-only divergence from the live server, which marshals the same
// values compact in serveJSONCached.
//
// This is not the only writer of an exported JSON file: forecast_seed.json is
// written straight from BuildForecastSeed's bytes at the call site above, and
// is compact because seeds.go marshals it with json.Marshal. Both paths are
// pinned by TestExport_DataFilesAreMinified.
func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, data, 0o644)
}
