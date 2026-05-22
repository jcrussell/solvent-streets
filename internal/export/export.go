package export

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/units"
)

// --- Exporter (static site generation) ---

type Exporter struct {
	entries    []CityEntry
	cfg        *config.Config
	outputDir  string
	unitSystem string
	wasmPrefix string // relative path prefix for WASM assets in generated HTML
	skipWasm   bool   // skip writing WASM files (caller handles shared copy)
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

	if err := e.exportCityData(ctx, entry, dataDir); err != nil {
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

	fc := e.cfg.ResolvedForecast(&entry.City)
	seed, err := BuildForecastSeed(ctx, &fc, entry.Store)
	if err != nil {
		return err
	}
	meta, err := BuildMeta(ctx, entry)
	if err != nil {
		return err
	}
	return e.renderHTML(meta, seed, rawTOML, ResolvedTOML(e.cfg), e.unitSystem, nil)
}

func (e *Exporter) runMultiCity(ctx context.Context) error {
	if err := os.MkdirAll(e.outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// Export each city
	var cities []CityInfo
	for _, entry := range e.entries {
		cityDataDir := filepath.Join(e.outputDir, "cities", entry.Slug, "data")
		if err := os.MkdirAll(cityDataDir, 0o755); err != nil {
			return fmt.Errorf("create city dir %s: %w", entry.Slug, err)
		}
		if err := e.exportCityData(ctx, entry, cityDataDir); err != nil {
			return fmt.Errorf("export %s: %w", entry.Slug, err)
		}
		info, err := entry.Info(ctx)
		if err != nil {
			return fmt.Errorf("city %s bbox: %w", entry.Slug, err)
		}
		cities = append(cities, info)
	}

	// Write cities.json
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

func (e *Exporter) exportCityData(ctx context.Context, entry CityEntry, dataDir string) error {
	_, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return fmt.Errorf("city bbox: %w", err)
	}
	proj := geo.NewUTMProjector(lon, lat)

	meta, err := BuildMeta(ctx, entry)
	if err != nil {
		return err
	}

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
			return fmt.Errorf("write boundary geojson: %w", err)
		}
	}

	// Export hex grid — one file per scope. city is omitted when no ":city"
	// rows exist (signals "hide the scope toggle" to the client).
	cityFC, bboxFC := BuildHexGeoJSONs(ctx, entry, proj)
	if bboxFC != nil {
		if err := writeJSON(filepath.Join(dataDir, "hexgrid-bbox.geojson"), bboxFC); err != nil {
			return fmt.Errorf("write hexgrid-bbox: %w", err)
		}
	}
	if cityFC != nil {
		if err := writeJSON(filepath.Join(dataDir, "hexgrid-city.geojson"), cityFC); err != nil {
			return fmt.Errorf("write hexgrid-city: %w", err)
		}
	}

	// Write meta.json
	if err := writeJSON(filepath.Join(dataDir, "meta.json"), meta); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	// Export forecast and scenario data
	if err := exportScenariosForCity(ctx, entry, dataDir); err != nil {
		return fmt.Errorf("export scenarios: %w", err)
	}

	// Export forecast seed for interactive WASM controls (per-city)
	forecastCfg := entry.Config.ResolvedForecast(&entry.City)
	seed, err := BuildForecastSeed(ctx, &forecastCfg, entry.Store)
	if err != nil {
		return fmt.Errorf("build forecast seed: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "forecast_seed.json"), []byte(seed), 0o644); err != nil {
		return fmt.Errorf("write forecast_seed.json: %w", err)
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

	if resolved.Grid.HexEdgeM <= 0 {
		resolved.Grid.HexEdgeM = config.DefaultHexEdgeM
	}
	// Display defaults: resolve via UnitSystem() so PVMT_UNITS env wins
	// over an empty file value (same precedence config show --sources uses).
	resolved.Display.Units = cfg.UnitSystem().String()
	if resolved.Display.MinHexAreaSqM <= 0 {
		resolved.Display.MinHexAreaSqM = config.DefaultMinHexAreaSqM
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
	return buf.String()
}

func (e *Exporter) writeWasmAssets(dir string) error {
	return WriteSharedWasmAssets(dir)
}

func (e *Exporter) renderHTML(meta MetaJSON, seed template.JS, rawTOML, resolvedTOML, unitSystem string, cities []CityInfo) (err error) {
	sys := units.ParseSystem(unitSystem)
	tmpl, err := ParseIndexTemplate(sys)
	if err != nil {
		return err
	}

	outPath := filepath.Join(e.outputDir, "index.html")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create index.html: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close index.html: %w", cerr)
		}
	}()

	methodology, err := MethodologyHTML()
	if err != nil {
		return fmt.Errorf("render methodology: %w", err)
	}
	td := TemplateData{
		MetaJSON:        meta,
		ForecastSeed:    seed,
		LayerColors:     ResourceColorsJS(),
		RawTOML:         rawTOML,
		ResolvedTOML:    resolvedTOML,
		UnitSystem:      unitSystem,
		Cities:          cities,
		WasmPrefix:      e.wasmPrefix,
		MethodologyHTML: methodology,
	}
	return tmpl.Execute(f, td)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
