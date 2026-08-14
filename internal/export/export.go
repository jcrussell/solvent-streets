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

	return e.renderHTML(meta, seed, rawTOML, ResolvedTOML(e.cfg), nil)
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
	// Multi-city discards the per-city meta/seed: the dashboard renders
	// config-wide aggregates (BuildMultiCityMeta / BuildMultiCityForecastSeed),
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

	// Render the dashboard index: aggregated meta and forecast seed across all
	// sub-cities. Without this aggregation the dashboard silently presents the
	// first city's totals as the headline for the whole config.
	title := e.cfg.Export.Title
	if title == "" {
		title = filepath.Base(e.outputDir)
	}
	fc := e.cfg.ResolvedForecast(nil)
	meta, err := BuildMultiCityMeta(ctx, e.entries, title)
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

	return e.renderHTML(meta, seed, rawTOML, ResolvedTOML(e.cfg), cities)
}

// exportCityData writes a city's data directory and returns the MetaJSON and
// forecast seed it built along the way, so a caller rendering index.html for
// the same city (runSingleCity) reuses them instead of recomputing — each
// rebuild is a fresh round of DB queries plus boundary re-parsing.
func (e *Exporter) exportCityData(ctx context.Context, entry CityEntry, dataDir string) (MetaJSON, template.JS, error) {
	if err := entry.RequireMatchingSnapshot(ctx); err != nil {
		return MetaJSON{}, "", err
	}
	bbox, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return MetaJSON{}, "", fmt.Errorf("city bbox: %w", err)
	}
	proj := geo.NewUTMProjector(lon, lat)
	// A single UTM zone is picked from the bbox center; a bbox much wider than
	// one 6° zone gets distorted areas near its edges. Warn so an oversized city
	// isn't silently exported with skewed metrics (bbox is [south,west,north,
	// east], so the lon span is east-west = bbox[3]-bbox[1]).
	if geo.UTMLonSpanExceeds(bbox[1], bbox[3], 6) {
		fmt.Fprintf(e.warnOut(),
			"warning: city %q bbox spans %.1f° of longitude; a single UTM zone (%d) is used, so exported areas near the edges may be distorted\n",
			entry.City.Name, bbox[3]-bbox[1], proj.Zone)
	}

	meta, err := BuildMeta(ctx, entry, 0)
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
	if err := cmdutil.WriteFile(filepath.Join(dataDir, "forecast_seed.json"), []byte(seed), 0o644); err != nil {
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

		scenariosOut, err := BuildScenariosData(ctx, entry, &fc)
		if err != nil {
			return fmt.Errorf("build scenarios: %w", err)
		}
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
			cities[i].Forecast = &rf
		}
		resolved.Cities = cities
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

// writeExportMarker drops a cmdutil.ExportMarkerName sentinel at the top of the
// output dir at the very start of a run. index.html is only written last, so a
// run that fails partway (e.g. RequireMatchingSnapshot on city N of M) would
// otherwise leave a non-empty tree that SafeCleanDir refuses to remove; the
// early marker keeps such a partial export recoverable via --clean.
func (e *Exporter) writeExportMarker() error {
	path := filepath.Join(e.outputDir, cmdutil.ExportMarkerName)
	if err := cmdutil.WriteFile(path, []byte("pvmt export in progress\n"), 0o644); err != nil {
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
	path := filepath.Join(e.outputDir, ".nojekyll")
	if err := cmdutil.WriteFile(path, nil, 0o644); err != nil {
		return fmt.Errorf("write .nojekyll: %w", err)
	}
	return nil
}

func (e *Exporter) writeWasmAssets(dir string) error {
	return WriteSharedWasmAssets(dir)
}

func (e *Exporter) renderHTML(meta MetaJSON, seed template.JS, rawTOML, resolvedTOML string, cities []CityInfo) error {
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
		UnitSystem:      e.unitSystem,
		Cities:          cities,
		CitiesByTag:     GroupCitiesByTag(cities),
		WasmPrefix:      e.wasmPrefix,
		MethodologyHTML: methodology,
		GeneratedDate:   date,
		BuildVersion:    ver,
		// IsLiveServer and ActiveSlug stay zero: this is the static export, so
		// the game page resolves the active city client-side from ?city=.
	}

	// Render both pages before writing either, so a game-template failure
	// can't leave a partial site (index.html present, play.html missing).
	index, err := renderPage(ParseIndexTemplate, td)
	if err != nil {
		return err
	}
	play, err := renderPage(ParseGameTemplate, td)
	if err != nil {
		return err
	}
	if err := cmdutil.WriteFile(filepath.Join(e.outputDir, "index.html"), index, 0o644); err != nil {
		return err
	}
	return cmdutil.WriteFile(filepath.Join(e.outputDir, "play.html"), play, 0o644)
}

// renderPage parses a page template and renders it against td into a buffer.
// Buffering (plus cmdutil.WriteFile's temp+rename at the call site) keeps an
// interrupted Execute from leaving a partial file visible.
func renderPage(parse func() (*template.Template, error), td TemplateData) ([]byte, error) {
	tmpl, err := parse()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, td); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return cmdutil.WriteFile(path, data, 0o644)
}

// writeJSONCompact writes minified JSON (no indentation). Used for the hex grid,
// which dominates site size; the other exported files stay pretty for readable
// diffs.
func writeJSONCompact(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return cmdutil.WriteFile(path, data, 0o644)
}
