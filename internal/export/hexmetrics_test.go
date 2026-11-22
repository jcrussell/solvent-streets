package export

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/metric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/services"
)

// metricsEntry is a city whose resolved snapshot holds rows (the store's
// hex metrics), the facilities the fire run routed from, a roads compute and
// hexStats. The mock stands in for the snapshot: whatever it lists IS the
// snapshot's metric part.
//
// With no hexStats the city still has a grid: one roads coverage row on the
// first of metricsHexIDs, so hexgrid.geojson is written and the metrics have
// hexes to cover.
func metricsEntry(t *testing.T, rows []db.HexMetric, facilities []db.RunFacility, hexStats []results.HexCoverage) CityEntry {
	t.Helper()
	if len(hexStats) == 0 {
		hexStats = []results.HexCoverage{{HexID: metricsHexIDs(t)[0], Area: 1}}
	}
	store := &dbtest.MockStore{
		CityMetrics:     results.TotalRows(resource.TypeRoads, results.Totals{Area: 1}),
		HexMetrics:      append(slices.Clone(rows), results.HexCoverageRows(rtRoads, hexStats)...),
		Facilities:      map[string][]db.RunFacility{db.ServiceProducer("fire"): facilities},
		GetBoundaryFunc: func(context.Context) (string, error) { return boundaryGeoJSON, nil },
	}
	cfg := &config.Config{ConfigID: "t"}
	city := config.CityConfig{Name: "Test, CA", Facilities: []config.FacilityConfig{{Service: "fire", Name: "Manual", Lat: 37.7, Lon: -122.4}}}
	return CityEntry{Config: cfg, City: city, Store: store, Slug: "test-ca"}
}

// metricsHexIDs returns five hex ids metricsEntry's city draws
// (DrawnGrid), in id order: rows on them are what hex-metrics.json and
// meta.json's summaries carry.
func metricsHexIDs(t *testing.T) []string {
	t.Helper()
	probe := CityEntry{
		Config: &config.Config{ConfigID: "t"},
		City:   config.CityConfig{Name: "Test, CA"},
		Store:  &dbtest.MockStore{GetBoundaryFunc: func(context.Context) (string, error) { return boundaryGeoJSON, nil }},
	}
	return drawnHexIDs(t, probe, 5)
}

// fireFacilities is what a fire run over metricsEntry's city records: one
// OSM station and the city's hand-placed one.
func fireFacilities() []db.RunFacility {
	return []db.RunFacility{
		{ID: "osm:node:9", Name: "Station 9", Source: "osm", Lon: -122.41, Lat: 37.71},
		{ID: services.ConfigFacilityID("fire", -122.4, 37.7), Name: "Manual", Source: "config", Lon: -122.4, Lat: 37.7},
	}
}

// TestBuildHexMetrics pins the hex-metrics.json contract: one entry per metric
// with rows carrying the registry description, values rounded, refs resolved
// to a sorted points list spanning the run's OSM and config facilities, and
// (nil, nil) for a city with no metric rows.
func TestBuildHexMetrics(t *testing.T) {
	h := metricsHexIDs(t)
	now := time.Now()
	rows := []db.HexMetric{
		{HexID: h[0], Metric: metric.FireResponseS, Value: 300.123, Ref: "osm:node:9", ComputedAt: now},
		{HexID: h[1], Metric: metric.FireResponseS, Value: 420, Ref: services.ConfigFacilityID("fire", -122.4, 37.7), ComputedAt: now},
		{HexID: h[2], Metric: metric.FireResponseS, Value: 500, Ref: "osm:node:missing", ComputedAt: now},
	}
	entry := metricsEntry(t, rows, fireFacilities(), nil)
	hm, err := BuildHexMetrics(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if hm == nil || hm.V != HexMetricsFileVersion {
		t.Fatalf("hm = %+v", hm)
	}
	m, ok := hm.Metrics[metric.FireResponseS]
	if !ok {
		t.Fatalf("metrics = %v, want %s", hm.Metrics, metric.FireResponseS)
	}
	if m.Label != "Fire response time" || m.Format != "minutes" || len(m.Scale.Bands) != 4 || m.Scale.Bands[0] != 320 || m.ThresholdS != 320 {
		t.Errorf("metric description = %+v (bands must start at the default turnout+threshold 320)", m)
	}
	// A run that recorded no turnout follows the config's [[services]]
	// override.
	entry.Config.Services = []config.ServiceOverride{{Name: "fire", TurnoutS: new(200.0)}}
	hm2, err := BuildHexMetrics(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if m2 := hm2.Metrics[metric.FireResponseS]; m2.Scale.Bands[0] != 440 || m2.ThresholdS != 440 {
		t.Errorf("overridden bands/threshold = %v / %v, want 440", m2.Scale.Bands, m2.ThresholdS)
	}
	entry.Config.Services = nil
	if m.Values[h[0]] != 300.12 || m.Values[h[1]] != 420 {
		t.Errorf("values = %v", m.Values)
	}
	if len(m.Points) != 2 || m.Points[0].ID != services.ConfigFacilityID("fire", -122.4, 37.7) || m.Points[1].ID != "osm:node:9" {
		t.Fatalf("points = %+v, want config then osm (sorted by id)", m.Points)
	}
	if m.Points[1].Position[0] != -122.41 || m.Points[0].Source != "config" || m.Points[0].Name != "Manual" {
		t.Errorf("points = %+v", m.Points)
	}
	if m.Refs[h[0]] != 1 || m.Refs[h[1]] != 0 {
		t.Errorf("refs = %v", m.Refs)
	}
	if _, ok := m.Refs[h[2]]; ok {
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

	boom := metricsEntry(t, rows, fireFacilities(), nil)
	boom.Store.(*dbtest.MockStore).ListHexMetricsFunc = func(context.Context, string, string) ([]db.HexMetric, error) { return nil, errors.New("db down") }
	if _, err := BuildHexMetrics(context.Background(), boom); err == nil {
		t.Error("a store error must propagate, not become an empty file")
	}
}

// TestBuildMeta_PointCountsAddUp: a service summary's point count is the
// facilities its run recorded that the rows reference, the markers
// hex-metrics.json carries, so it is inside + outside. A reference to no
// recorded facility has no position: it is not a point, so it is in neither
// the count nor the split.
func TestBuildMeta_PointCountsAddUp(t *testing.T) {
	h := metricsHexIDs(t)
	rows := []db.HexMetric{
		{HexID: h[0], Metric: metric.FireResponseS, Value: 300, Ref: "osm:node:9"},
		{HexID: h[1], Metric: metric.FireResponseS, Value: 420, Ref: "osm:node:missing"},
	}
	entry := metricsEntry(t, rows, fireFacilities(), nil)
	meta, err := BuildMeta(context.Background(), entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := meta.Metrics[metric.FireResponseS]
	if s.InsidePointCount == nil || s.OutsidePointCount == nil {
		t.Fatalf("summary has no inside/outside split: %+v", s)
	}
	if s.PointCount != 1 || *s.InsidePointCount+*s.OutsidePointCount != s.PointCount {
		t.Errorf("point_count %d, inside %d + outside %d; want 1 recorded station, and the split to add up to it",
			s.PointCount, *s.InsidePointCount, *s.OutsidePointCount)
	}
	hm, err := BuildHexMetrics(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(hm.Metrics[metric.FireResponseS].Points); got != s.PointCount {
		t.Errorf("hex-metrics.json marks %d points, meta.json counts %d", got, s.PointCount)
	}
}

// TestBuildMeta_MetricSummaries: meta.json summarises each metric over every
// stored row — hexgrid paints every grid hex, pavement or not — and omits
// the key when no metric has rows.
func TestBuildMeta_MetricSummaries(t *testing.T) {
	h := metricsHexIDs(t)
	now := time.Now()
	rows := []db.HexMetric{
		{HexID: h[0], Metric: metric.FireResponseS, Value: 300, Ref: "a", ComputedAt: now},
		{HexID: h[1], Metric: metric.FireResponseS, Value: 420, Ref: "b", ComputedAt: now},
		{HexID: h[4], Metric: metric.FireResponseS, Value: 9000, Ref: "b", ComputedAt: now},
	}
	stats := []results.HexCoverage{{HexID: h[0], Area: 1}, {HexID: h[1], Area: 1}}
	recorded := []db.RunFacility{{ID: "a", Lon: -122.41, Lat: 37.71}, {ID: "b", Lon: -122.4, Lat: 37.7}}
	entry := metricsEntry(t, rows, recorded, stats)
	meta, err := BuildMeta(context.Background(), entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := meta.Metrics[metric.FireResponseS]
	if !ok {
		t.Fatalf("meta.metrics = %v", meta.Metrics)
	}
	if s.HexCount != 3 || s.Min != 300 || s.Max != 9000 || s.Median != 420 || s.PointCount != 2 || s.Format != "minutes" {
		t.Errorf("summary = %+v; the pavement-free hex (9000) is painted and must count", s)
	}
	if s.ThresholdS != 320 || s.WithinThresholdShare == nil || *s.WithinThresholdShare != 0.3333 {
		t.Errorf("summary threshold/share = %v/%v, want 320 s and 0.3333 (300 within, 420 and 9000 not)", s.ThresholdS, s.WithinThresholdShare)
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

// TestBuildMeta_UnreachableHexesLowerTheShare: a
// hex the fire run recorded as unreachable counts against the
// within-threshold share and is reported, but has no value in
// hex-metrics.json and stays out of the count, median and range.
func TestBuildMeta_UnreachableHexesLowerTheShare(t *testing.T) {
	h := metricsHexIDs(t)
	now := time.Now()
	rows := []db.HexMetric{
		{HexID: h[0], Metric: metric.FireResponseS, Value: 300, Ref: "a", ComputedAt: now},
		{HexID: h[1], Metric: metric.FireResponseS, Value: 420, Ref: "a", ComputedAt: now},
		{HexID: h[2], Metric: metric.FireResponseS, Subject: services.SubjectUnreachable, ComputedAt: now},
		{HexID: h[3], Metric: metric.FireResponseS, Subject: services.SubjectUnreachable, ComputedAt: now},
	}
	stats := []results.HexCoverage{{HexID: h[0], Area: 1}}
	entry := metricsEntry(t, rows, nil, stats)
	meta, err := BuildMeta(context.Background(), entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := meta.Metrics[metric.FireResponseS]
	if s.HexCount != 2 || s.UnreachableCount != 2 || s.Min != 300 || s.Max != 420 {
		t.Errorf("summary = %+v, want 2 timed hexes (300..420) and 2 unreachable", s)
	}
	if s.Median != 360 {
		t.Errorf("median = %v, want 360 (the mean of the two timed hexes)", s.Median)
	}
	if s.WithinThresholdShare == nil || *s.WithinThresholdShare != 0.25 {
		t.Errorf("share = %v, want 0.25 (1 within of 4 on the network)", s.WithinThresholdShare)
	}
	hm, err := BuildHexMetrics(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if v := hm.Metrics[metric.FireResponseS].Values; len(v) != 2 {
		t.Errorf("values = %v, want only the timed hexes (an unreachable hex has no value)", v)
	}
}

// TestHexMetrics_IndependentOfRoadsCompute: stored metrics are their
// producers' own parts of the resolved snapshot, so they are published
// whether or not the city has a roads compute, a roads recompute does not hide
// them, and it does not matter which run wrote them.
func TestHexMetrics_IndependentOfRoadsCompute(t *testing.T) {
	h := metricsHexIDs(t)
	rows := []db.HexMetric{{HexID: h[0], Metric: metric.FireResponseS, Value: 300, Ref: "a", RunID: 3}}
	stats := []results.HexCoverage{{HexID: h[0], Area: 1}}

	noRoads := metricsEntry(t, rows, nil, stats)
	noRoads.Store.(*dbtest.MockStore).CityMetrics = nil
	hm, err := BuildHexMetrics(context.Background(), noRoads)
	if err != nil {
		t.Fatal(err)
	}
	if hm == nil || hm.Metrics[metric.FireResponseS].Values[h[0]] != 300 {
		t.Errorf("no roads compute: BuildHexMetrics = %+v, want the fire metric published", hm)
	}
	meta, err := BuildMeta(context.Background(), metricsEntry(t, rows, nil, stats), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := meta.Metrics[metric.FireResponseS]; !ok {
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
	for _, key := range []string{"fire_response_s", "population", "housing_units", "population_density", "maintained_area_per_resident", "year1_need_per_resident"} {
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
	s, ok := summarizeMetric(pop, rows, 0, 0, false)
	if !ok || s.Total == nil || *s.Total != 42.5 {
		t.Errorf("population total = %v, want 42.5", s.Total)
	}
	fire, _ := hexmetric.Lookup(metric.FireResponseS)
	if s, _ := summarizeMetric(fire, rows, 0, 0, false); s.Total != nil {
		t.Errorf("fire response must not carry a total, got %v", *s.Total)
	}
}

// TestDensityKeepsItsDigits: population_density is stored per m² (about
// 0.0003 to 0.02), so hex-metrics.json and the meta.json summary emit it by
// significant figures; two decimals would flatten every value to 0 or 0.01.
func TestDensityKeepsItsDigits(t *testing.T) {
	spec, ok := hexmetric.Lookup(metric.PopulationDensity)
	if !ok {
		t.Fatal("population_density is not registered")
	}
	rows := []db.HexMetric{{HexID: "a", Value: 0.000312345}, {HexID: "b", Value: 0.00491234}, {HexID: "c", Value: 0.0187654}}
	m, err := buildMetricJSON(context.Background(), CityEntry{}, spec, rows)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]float64{"a": 0.0003123, "b": 0.004912, "c": 0.01877} {
		if got := m.Values[id]; got != want {
			t.Errorf("value %s = %v, want %v", id, got, want)
		}
	}
	s, ok := summarizeMetric(spec, rows, 0, 0, false)
	if !ok || s.Min != 0.0003123 || s.Median != 0.004912 || s.Max != 0.01877 {
		t.Errorf("summary min/median/max = %v/%v/%v, want 0.0003123/0.004912/0.01877", s.Min, s.Median, s.Max)
	}
	// A value of 1 or more keeps two decimals, as fire seconds always have.
	if got := roundMetric(312.3456); got != 312.35 {
		t.Errorf("roundMetric(312.3456) = %v, want 312.35", got)
	}
}
