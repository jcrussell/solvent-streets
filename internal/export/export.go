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
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// --- Exporter (static site generation) ---

type Exporter struct {
	entries    []CityEntry
	cfg        *config.Config
	outputDir  string
	unitSystem string
	wasmPrefix string    // relative path prefix for WASM assets in generated HTML
	skipWasm   bool      // skip writing WASM files (caller handles shared copy)
	errOut     io.Writer // progress/warning sink; nil means discard (byob-iostreams.3)
}

// validWasmPrefix matches safe relative path prefixes (alphanumeric, dots, slashes, hyphens, underscores).
var validWasmPrefix = regexp.MustCompile(`^[a-zA-Z0-9_./-]*$`)

// SetWasmPrefix sets the relative path prefix for WASM asset references in
// generated HTML (e.g. "../" when WASM is served from a parent directory).
// The prefix must contain only safe path characters.
func (e *Exporter) SetWasmPrefix(prefix string) error {
	if !validWasmPrefix.MatchString(prefix) {
		return fmt.Errorf("invalid WASM prefix %q: must match %s", prefix, validWasmPrefix)
	}
	e.wasmPrefix = prefix
	return nil
}

// SetSkipWasm controls whether the exporter writes WASM files. Set to true
// when the caller writes a single shared copy at a parent directory.
func (e *Exporter) SetSkipWasm(skip bool) { e.skipWasm = skip }

// SetErrOut routes the exporter's progress and warning output (chatter, not
// data — byob-iostreams.3). Unset, warnings are discarded.
func (e *Exporter) SetErrOut(w io.Writer) { e.errOut = w }

// warnOut returns the warning sink, defaulting to io.Discard when unset so
// build tools and tests stay silent without special-casing.
func (e *Exporter) warnOut() io.Writer {
	if e.errOut == nil {
		return io.Discard
	}
	return e.errOut
}

func New(entries []CityEntry, cfg *config.Config, outputDir, unitSystem string) *Exporter {
	return &Exporter{entries: entries, cfg: cfg, outputDir: outputDir, unitSystem: unitSystem}
}

func (e *Exporter) Run(ctx context.Context) error {
	if len(e.entries) == 1 {
		return e.runSingleCity(ctx)
	}
	return e.runMultiCity(ctx)
}

func (e *Exporter) runSingleCity(ctx context.Context) error {
	entry := e.entries[0]
	dataDir := filepath.Join(e.outputDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// exportCityData already builds the MetaJSON and forecast seed it writes to
	// data/meta.json + data/forecast_seed.json; reuse them for index.html rather
	// than recomputing both (a second round of DB queries and boundary parses).
	meta, seed, err := e.exportCityData(ctx, entry, dataDir)
	if err != nil {
		return err
	}

	// Write WASM assets (skip when caller provides a shared copy)
	if !e.skipWasm {
		if err := e.writeWasmAssets(e.outputDir); err != nil {
			return err
		}
	}

	// Read raw TOML and build resolved version for Config tab
	var rawTOML string
	if e.cfg.SourcePath != "" {
		if data, err := os.ReadFile(e.cfg.SourcePath); err == nil {
			rawTOML = string(data)
		}
	}

	return e.renderHTML(meta, seed, rawTOML, ResolvedTOML(e.cfg), e.unitSystem, nil)
}

// exportOneCity writes one sub-city's data directory and returns its
// CityInfo. Skips (kept=false, no error) when the city has no boundary
// stored — typically because ingest tripped a hard error like NYC's
// water-strip backstop or Nominatim returning a Point. The regional
// aggregation helpers (regionBBox, summedBoundaryArea, etc.) already
// tolerate missing boundaries via continue-on-error.
func (e *Exporter) exportOneCity(ctx context.Context, entry CityEntry) (CityInfo, bool, error) {
	cityDataDir := filepath.Join(e.outputDir, "cities", entry.Slug, "data")
	if err := os.MkdirAll(cityDataDir, 0o755); err != nil {
		return CityInfo{}, false, fmt.Errorf("create city dir %s: %w", entry.Slug, err)
	}
	// Multi-city discards the per-city meta/seed: the landing page renders
	// region-wide aggregates (BuildMultiCityMeta / BuildMultiCityForecastSeed),
	// and exportOneCity only needs the per-city bbox/center via entry.Info.
	if _, _, err := e.exportCityData(ctx, entry, cityDataDir); err != nil {
		if errors.Is(err, ErrNoBoundary) {
			fmt.Fprintf(e.warnOut(), "  skipping %s: no boundary stored (ingest failed earlier)\n", entry.Slug)
			return CityInfo{}, false, nil
		}
		return CityInfo{}, false, fmt.Errorf("export %s: %w", entry.Slug, err)
	}
	info, err := entry.Info(ctx)
	if err != nil {
		if errors.Is(err, ErrNoBoundary) {
			return CityInfo{}, false, nil
		}
		return CityInfo{}, false, fmt.Errorf("city %s bbox: %w", entry.Slug, err)
	}
	return info, true, nil
}

func (e *Exporter) runMultiCity(ctx context.Context) error {
	if err := os.MkdirAll(e.outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
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

	// Write cities.json. Emit [] rather than null when every city skipped,
	// matching the /api/cities nil-guard so server/static parity holds
	// (TestHandleCitiesList_SchemaParity) and a consumer iterating the list
	// never hits a null.
	if cities == nil {
		cities = []CityInfo{}
	}
	// Alphabetise the city selector case-insensitively by Name. Stable so
	// ties (e.g. two same-named cities in different states) keep config order.
	slices.SortStableFunc(cities, func(a, b CityInfo) int {
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	if err := writeJSON(filepath.Join(e.outputDir, "cities.json"), cities); err != nil {
		return fmt.Errorf("write cities.json: %w", err)
	}

	// Write WASM assets (skip when caller provides a shared copy)
	if !e.skipWasm {
		if err := e.writeWasmAssets(e.outputDir); err != nil {
			return err
		}
	}

	// Render the regional landing page: aggregated meta and forecast seed
	// across all sub-cities. Without this aggregation the landing silently
	// presents the first city's totals as the regional headline.
	regionName := e.cfg.Export.Title
	if regionName == "" {
		regionName = filepath.Base(e.outputDir)
	}
	fc := e.cfg.ResolvedForecast(nil)
	meta, err := BuildMultiCityMeta(ctx, e.entries, regionName)
	if err != nil {
		return err
	}
	seed, err := BuildMultiCityForecastSeed(ctx, &fc, e.entries)
	if err != nil {
		return err
	}

	var rawTOML string
	if e.cfg.SourcePath != "" {
		if data, err := os.ReadFile(e.cfg.SourcePath); err == nil {
			rawTOML = string(data)
		}
	}

	return e.renderHTML(meta, seed, rawTOML, ResolvedTOML(e.cfg), e.unitSystem, cities)
}

// exportCityData writes a city's data directory and returns the MetaJSON and
// forecast seed it built along the way, so a caller rendering index.html for
// the same city (runSingleCity) reuses them instead of recomputing — each
// rebuild is a fresh round of DB queries plus boundary re-parsing.
func (e *Exporter) exportCityData(ctx context.Context, entry CityEntry, dataDir string) (MetaJSON, template.JS, error) {
	if err := entry.RequireMatchingSnapshot(ctx); err != nil {
		return MetaJSON{}, "", err
	}
	_, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return MetaJSON{}, "", fmt.Errorf("city bbox: %w", err)
	}
	proj := geo.NewUTMProjector(lon, lat)

	meta, err := BuildMeta(ctx, entry)
	if err != nil {
		return MetaJSON{}, "", err
	}

	// The per-city data files written below (and in exportScenariosForCity)
	// are enumerated for the publish-readiness checker by DataFileNames in
	// checkassets.go — keep the two in sync when adding or removing a file.
	//
	// Write boundary.geojson if boundary exists
	if boundaryGJSON, err := entry.Store.GetBoundary(ctx); err == nil && boundaryGJSON != "" {
		fc := map[string]any{
			"type": "FeatureCollection",
			"features": []map[string]any{
				{
					"type":       "Feature",
					"geometry":   json.RawMessage(boundaryGJSON),
					"properties": map[string]any{"type": "boundary"},
				},
			},
		}
		if err := writeJSON(filepath.Join(dataDir, "boundary.geojson"), fc); err != nil {
			return MetaJSON{}, "", fmt.Errorf("write boundary geojson: %w", err)
		}
	}

	// Export hex grid — a single multi-scope file, one feature per hex with
	// nested {bbox, city?} coverage. Written minified (it dominates site size);
	// a feature without "city" signals "hide the scope toggle" to the client.
	hexFC, err := BuildHexGeoJSON(ctx, entry, proj)
	if err != nil {
		return MetaJSON{}, "", fmt.Errorf("build hexgrid: %w", err)
	}
	if hexFC != nil {
		if err := writeJSONCompact(filepath.Join(dataDir, "hexgrid.geojson"), hexFC); err != nil {
			return MetaJSON{}, "", fmt.Errorf("write hexgrid: %w", err)
		}
	}

	// Export the /play board's per-hex blended decay rates from real road
	// geometry — the same data the live server computes on demand. BuildPlayHexes
	// shares cityHexGrid with BuildHexGeoJSON, so the emitted ids are a subset of
	// hexgrid.geojson; it returns nil when the city has no road features, so skip
	// the write then (exactly like hexFC above). Enumerated in DataFileNames
	// (checkassets.go) so check-site requires it for a publish-ready city.
	playHexes, err := BuildPlayHexes(ctx, entry, proj)
	if err != nil {
		return MetaJSON{}, "", fmt.Errorf("build play hexes: %w", err)
	}
	if playHexes != nil {
		if err := writeJSON(filepath.Join(dataDir, "play-hexes.json"), playHexes); err != nil {
			return MetaJSON{}, "", fmt.Errorf("write play-hexes: %w", err)
		}
	}

	// Write meta.json
	if err := writeJSON(filepath.Join(dataDir, "meta.json"), meta); err != nil {
		return MetaJSON{}, "", fmt.Errorf("write meta: %w", err)
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
	if err := os.WriteFile(filepath.Join(dataDir, "forecast_seed.json"), []byte(seed), 0o644); err != nil {
		return MetaJSON{}, "", fmt.Errorf("write forecast_seed.json: %w", err)
	}

	return meta, seed, nil
}

func exportScenariosForCity(ctx context.Context, entry CityEntry, dataDir string) error {
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := ConvertCostTiers(&fc)

	forecasts, err := BuildForecastsForCity(ctx, entry, &fc, costTiers)
	if err != nil {
		return fmt.Errorf("build forecasts: %w", err)
	}

	if len(forecasts) > 0 {
		if err := writeJSON(filepath.Join(dataDir, "forecast.json"), forecasts); err != nil {
			return fmt.Errorf("write forecast.json: %w", err)
		}

		hexCostSummary := BuildHexCostSummary(ctx, entry, forecasts)
		if err := writeJSON(filepath.Join(dataDir, "hex-cost-summary.json"), hexCostSummary); err != nil {
			return fmt.Errorf("write hex-cost-summary.json: %w", err)
		}

		scenariosOut := BuildScenariosData(ctx, entry, &fc)
		if err := writeJSON(filepath.Join(dataDir, "scenarios.json"), scenariosOut); err != nil {
			return fmt.Errorf("write scenarios.json: %w", err)
		}
	}

	return nil
}

// ResolvedTOML returns the config serialized as TOML with all defaults filled in.
func ResolvedTOML(cfg *config.Config) string {
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
	config.NormalizeForecast(&resolved.Forecast)
	if resolved.Forecast.DecayRate <= 0 {
		resolved.Forecast.DecayRate = forecast.DefaultDecayRates["default"]
	}
	if len(resolved.Forecast.CostTiers) == 0 {
		for _, t := range forecast.DefaultCostTiers {
			resolved.Forecast.CostTiers = append(resolved.Forecast.CostTiers, config.CostTierCfg{
				MinPCI:     t.MinPCI,
				MaxPCI:     t.MaxPCI,
				CostPerSqM: t.CostPerSqM,
				Label:      t.Label,
			})
		}
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(resolved); err != nil {
		return "# error encoding config"
	}
	return stripZeroCurrentBudget(buf.String())
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

func (e *Exporter) writeWasmAssets(dir string) error {
	return WriteSharedWasmAssets(dir)
}

func (e *Exporter) renderHTML(meta MetaJSON, seed template.JS, rawTOML, resolvedTOML, unitSystem string, cities []CityInfo) error {
	sys := units.ParseSystem(unitSystem)

	methodology, err := MethodologyHTML()
	if err != nil {
		return fmt.Errorf("render methodology: %w", err)
	}
	date, ver := FooterInfo()
	td := TemplateData{
		MetaJSON:        meta,
		ForecastSeed:    seed,
		LayerColors:     ResourceColorsJS(),
		RawTOML:         rawTOML,
		ResolvedTOML:    resolvedTOML,
		UnitSystem:      unitSystem,
		Cities:          cities,
		CitiesByRegion:  GroupCitiesByRegion(cities),
		WasmPrefix:      e.wasmPrefix,
		MethodologyHTML: methodology,
		GeneratedDate:   date,
		BuildVersion:    ver,
		// IsLiveServer and ActiveSlug stay zero: this is the static export, so
		// the game page resolves the active city client-side from ?city=.
	}

	if err := e.renderPage(ParseIndexTemplate, sys, td, "index.html"); err != nil {
		return err
	}
	return e.renderPage(ParseGameTemplate, sys, td, "play.html")
}

// renderPage parses a page template for the unit system and renders it against
// td into outputDir/name. Rendering to a buffer first (then cmdutil.WriteFile's
// temp+rename) keeps an interrupted Execute from leaving a partial file visible.
func (e *Exporter) renderPage(parse func(units.System) (*template.Template, error), sys units.System, td TemplateData, name string) error {
	tmpl, err := parse(sys)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, td); err != nil {
		return err
	}
	return cmdutil.WriteFile(filepath.Join(e.outputDir, name), buf.Bytes(), 0o644)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// writeJSONCompact writes minified JSON (no indentation). Used for the hex grid,
// which dominates site size; the other exported files stay pretty for readable
// diffs.
func writeJSONCompact(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
