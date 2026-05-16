package export

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
)

// hexEntry builds a CityEntry whose ListHexStats returns rows from the given
// map (keyed by full resource label, e.g. "roads" or "roads:city").
// LatestComputeResult is similarly keyed.
func hexEntry(t *testing.T, hexStats map[string][]db.HexStat, results map[string]db.ComputeResult) CityEntry {
	t.Helper()
	store := &dbtest.MockStore{
		ListHexStatsFunc: func(_ context.Context, rt string) ([]db.HexStat, error) {
			return hexStats[rt], nil
		},
		LatestComputeResultFunc: func(_ context.Context, rt string) (*db.ComputeResult, error) {
			r, ok := results[rt]
			if !ok {
				return nil, sql.ErrNoRows
			}
			return &r, nil
		},
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			return boundaryGeoJSON, nil
		},
	}
	return CityEntry{
		Config: &config.Config{Grid: config.GridConfig{HexEdgeM: 200}},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
}

// TestBuildHexGeoJSONs_BothScopesEmitted: when ListHexStats returns rows for
// both ":city" and bare labels, both FCs are non-nil and the bare resource
// label is preserved in feature properties (the ":city" suffix is stripped so
// the client can bucket-by-type without splitting).
func TestBuildHexGeoJSONs_BothScopesEmitted(t *testing.T) {
	// Pick a hex in the middle of boundaryGeoJSON's square — projected coords
	// land inside the generated hex grid. Hex IDs are deterministic ("q,r"),
	// but the exact id depends on UTM zone + edge; use a row that the grid
	// will accept by virtue of *any* of the per-suffix rows matching at least
	// one generated hex. We rely on the city-area projection producing many
	// hexes; pass empty hex_id and check the count behavior at the FC level.
	now := time.Now()
	cityRows := []db.HexStat{{HexID: "0,0", ResourceType: "roads:city", AreaSqM: 100, PctCovered: 50, ComputedAt: now}}
	bboxRows := []db.HexStat{{HexID: "0,0", ResourceType: "roads", AreaSqM: 200, PctCovered: 75, ComputedAt: now}}
	entry := hexEntry(t, map[string][]db.HexStat{
		"roads":      bboxRows,
		"roads:city": cityRows,
		"parking":    nil,
		"sidewalks":  nil,
	}, nil)

	_, lon, lat, err := entry.BBoxAndCenter(t.Context())
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon, lat)

	city, bbox := BuildHexGeoJSONs(t.Context(), entry, proj)
	if city == nil {
		t.Fatal("BuildHexGeoJSONs returned nil city FC when :city rows exist")
	}
	if bbox == nil {
		t.Fatal("BuildHexGeoJSONs returned nil bbox FC when bbox rows exist")
	}

	// resource_type in feature properties is the bare name, not "roads:city".
	for _, fc := range []map[string]any{city, bbox} {
		feats, _ := fc["features"].([]map[string]any)
		for _, f := range feats {
			props := f["properties"].(map[string]any)
			rt := props["resource_type"].(string)
			if strings.Contains(rt, ":") {
				t.Errorf("feature resource_type %q must not carry the :city suffix", rt)
			}
		}
	}
}

// TestBuildHexGeoJSONs_NoCityRowsReturnsNilCity: legacy cities without
// ":city"-suffixed hex_stats rows must yield a nil city FC. The client uses
// the absence of hexgrid-city.geojson as the "hide the scope toggle" signal.
func TestBuildHexGeoJSONs_NoCityRowsReturnsNilCity(t *testing.T) {
	bboxRows := []db.HexStat{{HexID: "0,0", ResourceType: "roads", AreaSqM: 100, PctCovered: 50}}
	entry := hexEntry(t, map[string][]db.HexStat{
		"roads":     bboxRows,
		"parking":   nil,
		"sidewalks": nil,
	}, nil)

	_, lon, lat, _ := entry.BBoxAndCenter(t.Context())
	city, bbox := BuildHexGeoJSONs(t.Context(), entry, geo.NewUTMProjector(lon, lat))
	if city != nil {
		t.Errorf("city FC = %v; want nil when no :city rows exist", city)
	}
	if bbox == nil {
		t.Error("bbox FC must be non-nil when bbox rows exist")
	}
}

// TestBuildHexCostSummary_NestedByScope: the legacy flat shape
// (map[resource]{...}) is replaced by map[scope]map[resource]{...}. Both
// "city" and "bbox" keys appear when BboxBaseline is set on the forecast
// export (the marker that compute produced both scopes).
func TestBuildHexCostSummary_NestedByScope(t *testing.T) {
	now := time.Now()
	results := map[string]db.ComputeResult{
		"roads":      {ResourceType: "roads", TotalAreaSqM: 2000, ComputedAt: now},
		"roads:city": {ResourceType: "roads:city", TotalAreaSqM: 1000, ComputedAt: now},
	}
	entry := hexEntry(t, nil, results)

	bboxBaseline := forecast.ScenarioResult{
		Years: []forecast.ScenarioYear{{AnnualNeed: 50000}},
	}
	forecasts := []ForecastExport{{
		ResourceType: "roads",
		Baseline:     forecast.ScenarioResult{Years: []forecast.ScenarioYear{{AnnualNeed: 25000}}},
		BboxBaseline: &bboxBaseline,
	}}

	out := BuildHexCostSummary(t.Context(), entry, forecasts)

	city, ok := out["city"]
	if !ok {
		t.Fatalf("missing 'city' key in %v", out)
	}
	bbox, ok := out["bbox"]
	if !ok {
		t.Fatalf("missing 'bbox' key in %v", out)
	}
	if city["roads"]["year1_cost"] != 25000 {
		t.Errorf("city roads year1_cost = %v; want 25000 (from Baseline)", city["roads"]["year1_cost"])
	}
	if city["roads"]["total_area_sqm"] != 1000 {
		t.Errorf("city roads total_area_sqm = %v; want 1000 (city-scope row)", city["roads"]["total_area_sqm"])
	}
	if bbox["roads"]["year1_cost"] != 50000 {
		t.Errorf("bbox roads year1_cost = %v; want 50000 (from BboxBaseline)", bbox["roads"]["year1_cost"])
	}
	if bbox["roads"]["total_area_sqm"] != 2000 {
		t.Errorf("bbox roads total_area_sqm = %v; want 2000 (bbox-scope row)", bbox["roads"]["total_area_sqm"])
	}
}

// TestBuildHexCostSummary_BboxOnlyWhenNoCity: when BboxBaseline is nil
// (legacy single-scope compute), the output has only the "bbox" key and
// Baseline carries the bbox numbers.
func TestBuildHexCostSummary_BboxOnlyWhenNoCity(t *testing.T) {
	results := map[string]db.ComputeResult{
		"roads": {ResourceType: "roads", TotalAreaSqM: 2000},
	}
	entry := hexEntry(t, nil, results)

	forecasts := []ForecastExport{{
		ResourceType: "roads",
		Baseline:     forecast.ScenarioResult{Years: []forecast.ScenarioYear{{AnnualNeed: 50000}}},
		// BboxBaseline left nil → no city scope.
	}}

	out := BuildHexCostSummary(t.Context(), entry, forecasts)

	if _, ok := out["city"]; ok {
		t.Errorf("'city' key must be absent when BboxBaseline is nil; got %v", out)
	}
	if out["bbox"]["roads"]["year1_cost"] != 50000 {
		t.Errorf("bbox roads year1_cost = %v; want 50000", out["bbox"]["roads"]["year1_cost"])
	}
}

// TestBuildScenariosData_RenamedKeys: the legacy "all" key (which actually
// held the city-scope primary) is renamed to "city". When no :city compute
// rows exist, the only scope key is "bbox".
func TestBuildScenariosData_RenamedKeys(t *testing.T) {
	results := map[string]db.ComputeResult{
		"roads":      {ResourceType: "roads", TotalAreaSqM: 2000, FeatureCount: 100},
		"roads:city": {ResourceType: "roads:city", TotalAreaSqM: 1000, FeatureCount: 60},
	}
	entry := hexEntry(t, nil, results)
	fc := &config.ForecastConfig{Years: 5, InitialPCI: 85, DecayRate: 1.5}

	out := BuildScenariosData(t.Context(), entry, fc)
	if _, ok := out["all"]; ok {
		t.Errorf("legacy 'all' key still present; got %v", out)
	}
	if _, ok := out["city"]; !ok {
		t.Errorf("'city' key missing; got %v", out)
	}
	if _, ok := out["bbox"]; !ok {
		t.Errorf("'bbox' key missing when city data exists; got %v", out)
	}
}

// TestBuildScenariosData_BboxOnlyKey: when only bbox compute rows exist,
// the output has just the "bbox" key — no "city", no legacy "all".
func TestBuildScenariosData_BboxOnlyKey(t *testing.T) {
	results := map[string]db.ComputeResult{
		"roads": {ResourceType: "roads", TotalAreaSqM: 2000, FeatureCount: 100},
	}
	entry := hexEntry(t, nil, results)
	fc := &config.ForecastConfig{Years: 5, InitialPCI: 85, DecayRate: 1.5}

	out := BuildScenariosData(t.Context(), entry, fc)
	if _, ok := out["all"]; ok {
		t.Errorf("legacy 'all' key still present; got %v", out)
	}
	if _, ok := out["city"]; ok {
		t.Errorf("'city' key must be absent when no :city rows; got %v", out)
	}
	if _, ok := out["bbox"]; !ok {
		t.Errorf("'bbox' key missing; got %v", out)
	}
}
