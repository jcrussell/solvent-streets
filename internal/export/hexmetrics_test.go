package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/services"
)

// metricsEntry is a city whose resolved snapshot holds rows (the store's
// ListHexMetrics), a roads compute and hexStats. The mock stands in for the
// snapshot: whatever it lists IS the snapshot's metric part.
func metricsEntry(t *testing.T, rows []db.HexMetric, facilities []db.Feature, hexStats []db.HexStat) CityEntry {
	t.Helper()
	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, rt resource.Type) (*db.ComputeResult, error) {
			if rt == resource.TypeRoads {
				return &db.ComputeResult{ResourceType: rt, TotalArea: 1, RunID: 5}, nil
			}
			return nil, sql.ErrNoRows
		},
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
		{HexID: "h2", Metric: services.MetricFire, Value: 420, Ref: services.ConfigFacilityID("fire", -122.4, 37.7), ComputedAt: now},
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
	if m.Label != "Fire response" || m.Format != "minutes" || len(m.Scale.Bands) != 4 || m.Scale.Bands[0] != 320 || m.ThresholdS != 320 {
		t.Errorf("metric description = %+v (bands must start at the default turnout+threshold 320)", m)
	}
	// A [[services]] override moves the published bands and threshold with it.
	entry.Config.Services = []config.ServiceOverride{{Name: "fire", TurnoutS: 200}}
	hm2, err := BuildHexMetrics(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if m2 := hm2.Metrics[services.MetricFire]; m2.Scale.Bands[0] != 440 || m2.ThresholdS != 440 {
		t.Errorf("overridden bands/threshold = %v / %v, want 440", m2.Scale.Bands, m2.ThresholdS)
	}
	entry.Config.Services = nil
	if m.Values["h1"] != 300.12 || m.Values["h2"] != 420 {
		t.Errorf("values = %v", m.Values)
	}
	if len(m.Points) != 2 || m.Points[0].ID != services.ConfigFacilityID("fire", -122.4, 37.7) || m.Points[1].ID != "osm:node:9" {
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

	// A [[cities.facilities]] entry inserted before the station must not
	// change which station h2 references: ids follow position, not index.
	entry.City.Facilities = append([]config.FacilityConfig{{Service: "fire", Name: "Inserted", Lat: 37.8, Lon: -122.3}}, entry.City.Facilities...)
	hm3, err := BuildHexMetrics(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	m3 := hm3.Metrics[services.MetricFire]
	if p := m3.Points[m3.Refs["h2"]]; p.Name != "Manual" {
		t.Errorf("after inserting a facility, h2 references %q, want Manual", p.Name)
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

// TestBuildMeta_MetricSummaries: meta.json summarises each metric over every
// stored row — hexgrid v4 paints every grid hex, pavement or not — and omits
// the key when no metric has rows.
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
	if s.Hexes != 3 || s.Min != 300 || s.Max != 9000 || s.Median != 420 || s.Points != 2 || s.Format != "minutes" {
		t.Errorf("summary = %+v; the pavement-free hex (9000) is painted in v4 and must count", s)
	}
	if s.ThresholdS != 320 || s.PctWithin != 33.33 {
		t.Errorf("summary threshold/pct = %v/%v, want 320 s and 33.33%% (300 within, 420 and 9000 not)", s.ThresholdS, s.PctWithin)
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

// TestHexMetrics_IndependentOfRoadsCompute pins solvent-streets-25t6: stored
// metrics are their producers' own parts of the resolved snapshot, so they are
// published whether or not the city has a roads compute (a roads recompute
// used to hide them until their producers reran) and regardless of which run
// wrote them.
func TestHexMetrics_IndependentOfRoadsCompute(t *testing.T) {
	rows := []db.HexMetric{{HexID: "h1", Metric: services.MetricFire, Value: 300, Ref: "a", RunID: 3}}
	stats := []db.HexStat{{HexID: "h1", ResourceType: rtRoads, Area: 1}}

	noRoads := metricsEntry(t, rows, nil, stats)
	noRoads.Store.(*dbtest.MockStore).LatestComputeResultFunc = nil
	hm, err := BuildHexMetrics(context.Background(), noRoads)
	if err != nil {
		t.Fatal(err)
	}
	if hm == nil || hm.Metrics[services.MetricFire].Values["h1"] != 300 {
		t.Errorf("no roads compute: BuildHexMetrics = %+v, want the fire metric published", hm)
	}
	meta, err := BuildMeta(context.Background(), metricsEntry(t, rows, nil, stats), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := meta.Metrics[services.MetricFire]; !ok {
		t.Errorf("meta.json must summarise the snapshot's fire metric: %+v", meta.Metrics)
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

// TestRegistry_ProducersLinked: the exporter sees every metric producer's
// specs — fire, population and the derived population ratios — so none can
// silently fall out of hex-metrics.json or the page config.
func TestRegistry_ProducersLinked(t *testing.T) {
	for _, key := range []string{"fire_response_s", "population", "housing_units", "population_per_km2", "road_m2_per_resident", "year1_need_usd_per_resident"} {
		if _, ok := hexmetric.Lookup(key); !ok {
			t.Errorf("metric %s is not registered as seen from the exporter", key)
		}
	}
}

// TestSummarizeMetric_AdditiveTotal: an additive count (population) carries
// its city total; a non-additive metric (fire seconds) does not.
func TestSummarizeMetric_AdditiveTotal(t *testing.T) {
	rows := []db.HexMetric{{HexID: "a", Value: 12.5}, {HexID: "b", Value: 30}}
	pop, _ := hexmetric.Lookup("population")
	s, ok := summarizeMetric(pop, rows, 0, false)
	if !ok || s.Total == nil || *s.Total != 42.5 {
		t.Errorf("population total = %v, want 42.5", s.Total)
	}
	fire, _ := hexmetric.Lookup(services.MetricFire)
	if s, _ := summarizeMetric(fire, rows, 0, false); s.Total != nil {
		t.Errorf("fire response must not carry a total, got %v", *s.Total)
	}
}
