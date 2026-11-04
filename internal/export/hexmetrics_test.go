package export

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/services"
)

func metricsEntry(t *testing.T, rows []db.HexMetric, facilities []db.Feature, hexStats []db.HexStat) CityEntry {
	t.Helper()
	store := &dbtest.MockStore{
		ListHexMetricsFunc: func(_ context.Context, key string) ([]db.HexMetric, error) {
			var out []db.HexMetric
			for _, r := range rows {
				if r.Metric == key {
					out = append(out, r)
				}
			}
			return out, nil
		},
		ListFeaturesFunc: func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
			if rt == resource.FacilityType("fire") {
				return facilities, nil
			}
			return nil, nil
		},
		ListHexStatsFunc: func(_ context.Context, rt resource.Type) ([]db.HexStat, error) {
			if rt == rtRoads {
				return hexStats, nil
			}
			return nil, nil
		},
		GetBoundaryFunc: func(context.Context) (string, error) { return boundaryGeoJSON, nil },
	}
	cfg := &config.Config{ConfigID: "t"}
	city := config.CityConfig{Name: "Test, CA", Facilities: []config.FacilityConfig{{Service: "fire", Name: "Manual", Lat: 37.7, Lon: -122.4}}}
	return CityEntry{Config: cfg, City: city, Store: store, Slug: "test-ca"}
}

// TestBuildHexMetrics pins the hex-metrics.json contract: one entry per metric
// with rows carrying the registry description, values rounded, refs resolved
// to a sorted points list spanning OSM facilities and config facilities, and
// (nil, nil) for a city with no metric rows.
func TestBuildHexMetrics(t *testing.T) {
	now := time.Now()
	rows := []db.HexMetric{
		{HexID: "h1", Metric: services.MetricFire, Value: 300.123, Ref: "osm:node:9", ComputedAt: now},
		{HexID: "h2", Metric: services.MetricFire, Value: 420, Ref: "config:fire:0", ComputedAt: now},
		{HexID: "h3", Metric: services.MetricFire, Value: 500, Ref: "osm:node:missing", ComputedAt: now},
	}
	facilities := []db.Feature{{ID: "osm:node:9", Name: "Station 9", GeometryJSON: `{"type":"Point","coordinates":[-122.41,37.71]}`}}
	entry := metricsEntry(t, rows, facilities, nil)
	hm, err := BuildHexMetrics(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if hm == nil || hm.V != HexMetricsFileVersion {
		t.Fatalf("hm = %+v", hm)
	}
	m, ok := hm.Metrics[services.MetricFire]
	if !ok {
		t.Fatalf("metrics = %v, want %s", hm.Metrics, services.MetricFire)
	}
	if m.Label != "Fire response" || m.Format != "minutes" || len(m.Scale.Bands) != 4 {
		t.Errorf("metric description = %+v", m)
	}
	if m.Values["h1"] != 300.12 || m.Values["h2"] != 420 {
		t.Errorf("values = %v", m.Values)
	}
	if len(m.Points) != 2 || m.Points[0].ID != "config:fire:0" || m.Points[1].ID != "osm:node:9" {
		t.Fatalf("points = %+v, want config then osm (sorted by id)", m.Points)
	}
	if m.Points[1].Lon != -122.41 || m.Points[0].Source != "config" || m.Points[0].Name != "Manual" {
		t.Errorf("points = %+v", m.Points)
	}
	if m.Refs["h1"] != 1 || m.Refs["h2"] != 0 {
		t.Errorf("refs = %v", m.Refs)
	}
	if _, ok := m.Refs["h3"]; ok {
		t.Error("a ref to an unknown point must be dropped, not indexed")
	}
	// Round-trips as the client reads it.
	raw, _ := json.Marshal(hm)
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}

	empty := metricsEntry(t, nil, nil, nil)
	if got, err := BuildHexMetrics(context.Background(), empty); err != nil || got != nil {
		t.Errorf("no rows: got (%v, %v), want (nil, nil)", got, err)
	}

	boom := metricsEntry(t, rows, facilities, nil)
	boom.Store.(*dbtest.MockStore).ListHexMetricsFunc = func(context.Context, string) ([]db.HexMetric, error) { return nil, errors.New("db down") }
	if _, err := BuildHexMetrics(context.Background(), boom); err == nil {
		t.Error("a store error must propagate, not become an empty file")
	}
}

// TestBuildMeta_MetricSummaries: meta.json summarises each metric over the
// hexes that have pavement rows (what the map paints), and omits the key when
// no metric has rows.
func TestBuildMeta_MetricSummaries(t *testing.T) {
	now := time.Now()
	rows := []db.HexMetric{
		{HexID: "h1", Metric: services.MetricFire, Value: 300, Ref: "a", ComputedAt: now},
		{HexID: "h2", Metric: services.MetricFire, Value: 420, Ref: "b", ComputedAt: now},
		{HexID: "h-nopave", Metric: services.MetricFire, Value: 9000, Ref: "b", ComputedAt: now},
	}
	stats := []db.HexStat{{HexID: "h1", ResourceType: rtRoads, Area: 1}, {HexID: "h2", ResourceType: rtRoads, Area: 1}}
	entry := metricsEntry(t, rows, nil, stats)
	meta, err := BuildMeta(context.Background(), entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := meta.Metrics[services.MetricFire]
	if !ok {
		t.Fatalf("meta.metrics = %v", meta.Metrics)
	}
	if s.Hexes != 2 || s.Min != 300 || s.Max != 420 || s.Median != 420 || s.Points != 2 || s.Format != "minutes" {
		t.Errorf("summary = %+v; the unpainted hex (9000) must be excluded", s)
	}
	empty := metricsEntry(t, nil, nil, stats)
	emeta, err := BuildMeta(context.Background(), empty, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(emeta)
	if json.Valid(raw) && len(emeta.Metrics) != 0 {
		t.Errorf("metrics must be omitted when none has rows: %s", raw)
	}
}

func TestDataFileNames_OptionalAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range AllDataFileNames() {
		if seen[n] {
			t.Errorf("duplicate data file name %q", n)
		}
		seen[n] = true
	}
	if !seen["hex-metrics.json"] {
		t.Error("hex-metrics.json must be an (optional) data file name")
	}
}
