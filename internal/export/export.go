package export

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/forecast"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
)

//go:embed templates
var templatesFS embed.FS

type metaJSON struct {
	ProjectName  string    `json:"project_name"`
	BBox         [4]float64 `json:"bbox"`
	CenterLon    float64   `json:"center_lon"`
	CenterLat    float64   `json:"center_lat"`
	SnapshotDate string    `json:"snapshot_date"`
	Stats        []statJSON `json:"stats"`
}

type statJSON struct {
	Type           string  `json:"type"`
	TotalAreaSqFt  float64 `json:"total_area_sqft"`
	TotalAreaAcres float64 `json:"total_area_acres"`
	FeatureCount   int     `json:"feature_count"`
}

type Exporter struct {
	store     db.Store
	cfg       *config.Config
	outputDir string
}

func New(store db.Store, cfg *config.Config, outputDir string) *Exporter {
	return &Exporter{store: store, cfg: cfg, outputDir: outputDir}
}

func (e *Exporter) Run() error {
	dataDir := filepath.Join(e.outputDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	lon, lat := e.cfg.Center()
	proj := geo.NewUTMProjector(lon, lat)

	meta := metaJSON{
		ProjectName:  e.cfg.Project.Name,
		BBox:         e.cfg.Area.BBox,
		CenterLon:    lon,
		CenterLat:    lat,
		SnapshotDate: time.Now().Format("2006-01-02"),
	}

	// Export each resource type
	for _, rt := range resource.All {
		result, err := e.store.LatestComputeResult(rt.Name())
		if err != nil {
			continue
		}

		meta.Stats = append(meta.Stats, statJSON{
			Type:           result.ResourceType,
			TotalAreaSqFt:  result.TotalAreaSqFt,
			TotalAreaAcres: result.TotalAreaAcres,
			FeatureCount:   result.FeatureCount,
		})

		// Write GeoJSON file
		if result.GeometryJSON != "" {
			geojsonPath := filepath.Join(dataDir, rt.Name()+".geojson")
			fc := map[string]any{
				"type": "FeatureCollection",
				"features": []map[string]any{
					{
						"type":       "Feature",
						"geometry":   json.RawMessage(result.GeometryJSON),
						"properties": map[string]any{"type": rt.Name()},
					},
				},
			}
			if err := writeJSON(geojsonPath, fc); err != nil {
				return fmt.Errorf("write %s geojson: %w", rt.Name(), err)
			}
		}
	}

	// Export hex grid
	hexFC := e.buildHexGeoJSON(proj)
	if hexFC != nil {
		if err := writeJSON(filepath.Join(dataDir, "hexgrid.geojson"), hexFC); err != nil {
			return fmt.Errorf("write hexgrid: %w", err)
		}
	}

	// Write meta.json
	if err := writeJSON(filepath.Join(dataDir, "meta.json"), meta); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	// Export forecast and scenario data
	if err := e.exportScenarios(dataDir); err != nil {
		return fmt.Errorf("export scenarios: %w", err)
	}

	// Render HTML template
	return e.renderHTML(meta)
}

func (e *Exporter) buildHexGeoJSON(proj *geo.UTMProjector) map[string]any {
	// Collect hex stats for all resource types
	var allStats []db.HexStat
	for _, rt := range resource.All {
		stats, err := e.store.ListHexStats(rt.Name())
		if err != nil {
			continue
		}
		allStats = append(allStats, stats...)
	}

	if len(allStats) == 0 {
		return nil
	}

	// Generate hex grid to get geometries
	bbox := e.cfg.Area.BBox
	hexEdge := e.cfg.HexEdge()
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0])
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2])
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)

	hexMap := make(map[string]*geo.Hex, len(hexes))
	for i := range hexes {
		hexMap[hexes[i].ID] = &hexes[i]
	}

	var features []map[string]any
	for _, st := range allStats {
		h, ok := hexMap[st.HexID]
		if !ok {
			continue
		}
		gjson, err := geo.GeometryToGeoJSON(h.Geom, proj)
		if err != nil {
			continue
		}
		features = append(features, map[string]any{
			"type":     "Feature",
			"geometry": json.RawMessage(gjson),
			"properties": map[string]any{
				"hex_id":        st.HexID,
				"resource_type": st.ResourceType,
				"area_sqft":     st.AreaSqFt,
				"pct_covered":   st.PctCovered,
			},
		})
	}

	return map[string]any{
		"type":     "FeatureCollection",
		"features": features,
	}
}

func (e *Exporter) renderHTML(meta metaJSON) (err error) {
	tmplData, err := templatesFS.ReadFile("templates/index.html.tmpl")
	if err != nil {
		return fmt.Errorf("read template: %w", err)
	}

	funcMap := template.FuncMap{
		"divf": func(a, b float64) float64 { return a / b },
	}
	tmpl, err := template.New("index").Funcs(funcMap).Parse(string(tmplData))
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
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

	return tmpl.Execute(f, meta)
}

func (e *Exporter) exportScenarios(dataDir string) error {
	// Build forecasting params from config
	var costTiers []forecast.CostTier
	for _, t := range e.cfg.Forecast.CostTiers {
		costTiers = append(costTiers, forecast.CostTier{
			MinPCI:      t.MinPCI,
			MaxPCI:      t.MaxPCI,
			CostPerSqFt: t.CostPerSqFt,
			Label:       t.Label,
		})
	}
	params := forecast.NewParams(e.cfg.Forecast.DecayRate, e.cfg.Forecast.GrowthRate, costTiers)
	years := e.cfg.ForecastYears()

	type forecastExport struct {
		ResourceType string                    `json:"resource_type"`
		Baseline     forecast.ScenarioResult   `json:"baseline"`
		Comparisons  []forecast.Comparison     `json:"comparisons"`
	}

	var allForecasts []forecastExport

	// Aggregate total area across all resource types for combined scenarios
	var totalAreaSqFt float64
	for _, rt := range resource.All {
		result, err := e.store.LatestComputeResult(rt.Name())
		if err != nil || result == nil {
			continue
		}
		totalAreaSqFt += result.TotalAreaSqFt
	}

	// Per-resource-type forecasts for forecast.json
	for _, rt := range resource.All {
		result, err := e.store.LatestComputeResult(rt.Name())
		if err != nil || result == nil {
			continue
		}

		areaSqFt := result.TotalAreaSqFt
		currentPCI := 85.0

		baseline := forecast.Simulate(
			forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing},
			areaSqFt, currentPCI, years, params.PCI, params.Cost, params.Growth,
		)

		year1Need := baseline.Years[0].AnnualNeed
		comparisons := forecast.GroupedComparisons(year1Need, areaSqFt, currentPCI, years,
			params.PCI, params.Cost, params.Growth)

		allForecasts = append(allForecasts, forecastExport{
			ResourceType: rt.Name(),
			Baseline:     baseline,
			Comparisons:  comparisons,
		})
	}

	if len(allForecasts) > 0 {
		if err := writeJSON(filepath.Join(dataDir, "forecast.json"), allForecasts); err != nil {
			return fmt.Errorf("write forecast.json: %w", err)
		}

		// Build combined scenarios.json using aggregated area
		currentPCI := 85.0
		baseline := forecast.Simulate(
			forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing},
			totalAreaSqFt, currentPCI, years, params.PCI, params.Cost, params.Growth,
		)

		year1Need := baseline.Years[0].AnnualNeed
		comparisons := forecast.GroupedComparisons(year1Need, totalAreaSqFt, currentPCI, years,
			params.PCI, params.Cost, params.Growth)

		allScenarios := []forecast.ScenarioResult{baseline}
		for _, comp := range comparisons {
			allScenarios = append(allScenarios, comp.Scenarios...)
		}
		if err := writeJSON(filepath.Join(dataDir, "scenarios.json"), allScenarios); err != nil {
			return fmt.Errorf("write scenarios.json: %w", err)
		}
	}

	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
