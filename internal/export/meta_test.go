package export

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/logs"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

var (
	mtRoads     = resource.TypeRoads
	mtParking   = resource.TypeParking
	mtSidewalks = resource.TypeSidewalks
	mtCombined  = resource.CombinedAll
)

// boundaryGeoJSON is a small square polygon (~111km × 111km in degrees, but
// area is computed in projected meters; the actual number isn't asserted on).
const boundaryGeoJSON = `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`

func newMockEntry(totals map[resource.Type]results.Totals) CityEntry {
	store := &dbtest.MockStore{
		CityMetrics: totalsFixture(totals),
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			return boundaryGeoJSON, nil
		},
	}
	return CityEntry{
		Config: &config.Config{},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
}

// TestBuildMeta_PrefersCombinedOverSum is the cx2 regression: when the
// "combined" totals exist, BuildMeta must use it for total_paved
// instead of the (overcounted) sum of per-resource rows.
func TestBuildMeta_PrefersCombinedOverSum(t *testing.T) {
	totals := map[resource.Type]results.Totals{
		mtRoads:     {Area: 1000},
		mtParking:   {Area: 500},
		mtSidewalks: {Area: 300},
		mtCombined:  {Area: 1500}, // less than 1800 sum because of buffer overlap
	}
	entry := newMockEntry(totals)
	meta, err := BuildMeta(context.Background(), entry, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	if meta.TotalPaved != 1500 {
		t.Errorf("total_paved = %v; want 1500 (from combined row, not 1800 sum)", meta.TotalPaved)
	}
	statByType := map[string]float64{}
	for _, s := range meta.Stats {
		statByType[s.ResourceType] = s.Area
	}
	want := map[string]float64{"roads": 1000, "parking": 500, "sidewalks": 300}
	if diff := cmp.Diff(want, statByType); diff != "" {
		t.Errorf("per-resource cards (-want +got):\n%s", diff)
	}
}

// TestBuildMultiCityMeta_AggregatesAcrossEntries is the dul regression: the
// multi-city landing must not silently render the first sub-city's totals as
// the regional headline. Per-resource Stats and TotalPaved must sum across
// entries; CityArea must be the union of sub-city boundaries.
func TestBuildMultiCityMeta_AggregatesAcrossEntries(t *testing.T) {
	// Two non-overlapping sub-cities with distinct boundaries and different
	// per-resource totals. Use boundaries far apart enough that the union
	// area is approximately the sum.
	cityA := newMockEntryWithBoundary(map[resource.Type]results.Totals{
		mtRoads:    {Area: 1000, Features: 10},
		mtCombined: {Area: 800},
	}, `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`)
	cityA.City.Name = "City A"
	cityA.Slug = "city-a"

	cityB := newMockEntryWithBoundary(map[resource.Type]results.Totals{
		mtRoads:    {Area: 2000, Features: 20},
		mtCombined: {Area: 1700},
	}, `{"type":"Polygon","coordinates":[[[-121.5,37.5],[-121.4,37.5],[-121.4,37.6],[-121.5,37.6],[-121.5,37.5]]]}`)
	cityB.City.Name = "City B"
	cityB.Slug = "city-b"

	meta, err := BuildMultiCityMeta(context.Background(), []CityEntry{cityA, cityB}, "Test Region")
	if err != nil {
		t.Fatalf("BuildMultiCityMeta: %v", err)
	}
	if meta.Name != "Test Region" {
		t.Errorf("name = %q; want %q", meta.Name, "Test Region")
	}

	// Per-resource totals sum across cities.
	if len(meta.Stats) != 1 {
		t.Fatalf("expected 1 resource stat, got %d", len(meta.Stats))
	}
	if meta.Stats[0].ResourceType != "roads" || meta.Stats[0].Area != 3000 {
		t.Errorf("aggregated roads stat = %+v; want type=roads, area=3000", meta.Stats[0])
	}

	// total_paved comes from summed combined rows (not per-resource fallback).
	if meta.TotalPaved != 2500 {
		t.Errorf("total_paved = %v; want 2500 (sum of combined rows)", meta.TotalPaved)
	}

	// Regional bbox spans both sub-cities.
	if meta.BBox[0] >= -121.4 || meta.BBox[2] <= -122.4 {
		t.Errorf("bbox %v does not span both cities", meta.BBox)
	}

	// City area is union — must be larger than either sub-city's area.
	if meta.CityArea <= 0 {
		t.Errorf("city_area = %v; want positive (union of boundaries)", meta.CityArea)
	}
}

// TestAggregatePerResourceStats_ReadErrorFails: a city whose totals cannot be
// read fails the regional cards instead of silently dropping out of them.
func TestAggregatePerResourceStats_ReadErrorFails(t *testing.T) {
	ok := newMockEntryWithBoundary(map[resource.Type]results.Totals{mtRoads: {Area: 1000, Features: 10}}, "")
	broken := newMockEntryWithBoundary(nil, "")
	broken.City.Name = "Broken City"
	locked := errors.New("database is locked")
	broken.Store.(*dbtest.MockStore).ListCityMetricsFunc = func(context.Context, []string) ([]db.CityMetric, error) {
		return nil, locked
	}
	if _, err := aggregatePerResourceStats(t.Context(), []CityEntry{ok, broken}); !errors.Is(err, locked) {
		t.Errorf("err = %v, want the read error", err)
	}
	stats, err := aggregatePerResourceStats(t.Context(), []CityEntry{ok})
	if err != nil || len(stats) != 1 || stats[0].Area != 1000 {
		t.Errorf("stats = %+v, %v; want one roads card of 1000", stats, err)
	}
}

// TestRegionBoundary_ReadErrorFails: a city whose boundary cannot be read
// fails the region's area and bbox rather than dropping out of them (which
// would leave its paved totals over a smaller area); a city with no stored
// boundary is still left out.
func TestRegionBoundary_ReadErrorFails(t *testing.T) {
	square := `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`
	ok := newMockEntryWithBoundary(nil, square)
	none := newMockEntryWithBoundary(nil, "")
	broken := newMockEntryWithBoundary(nil, square)
	broken.City.Name = "Broken City"
	locked := errors.New("database is locked")
	broken.Store.(*dbtest.MockStore).GetBoundaryFunc = func(context.Context) (string, error) { return "", locked }

	if _, err := summedBoundaryArea(t.Context(), []CityEntry{ok, broken}); !errors.Is(err, locked) {
		t.Errorf("summedBoundaryArea err = %v, want the read error", err)
	}
	if _, _, err := regionBBox(t.Context(), []CityEntry{ok, broken}); !errors.Is(err, locked) {
		t.Errorf("regionBBox err = %v, want the read error", err)
	}

	area, err := summedBoundaryArea(t.Context(), []CityEntry{ok, none})
	if err != nil || area <= 0 {
		t.Errorf("summedBoundaryArea without a boundary = (%v, %v), want the one city's area", area, err)
	}
	if _, has, err := regionBBox(t.Context(), []CityEntry{ok, none}); err != nil || !has {
		t.Errorf("regionBBox without a boundary = (%v, %v), want the one city's bbox", has, err)
	}
}

func newMockEntryWithBoundary(totals map[resource.Type]results.Totals, boundary string) CityEntry {
	store := &dbtest.MockStore{
		CityMetrics: totalsFixture(totals),
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			return boundary, nil
		},
	}
	return CityEntry{
		Config: &config.Config{},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
}

// TestBuildMeta_FallsBackToSumWhenCombinedMissing covers the transitional
// state where `pvmt all compute` has not yet been re-run after the cx2 fix
// landed: BuildMeta must still produce a non-zero total_paved by summing
// per-resource rows. The sum overcounts buffer overlap (the original bug),
// but reporting zero would be worse.
func TestBuildMeta_FallsBackToSumWhenCombinedMissing(t *testing.T) {
	totals := map[resource.Type]results.Totals{
		mtRoads:     {Area: 1000},
		mtParking:   {Area: 500},
		mtSidewalks: {Area: 300},
	}
	entry := newMockEntry(totals)
	meta, err := BuildMeta(context.Background(), entry, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	if meta.TotalPaved != 1800 {
		t.Errorf("total_paved = %v; want 1800 (sum fallback)", meta.TotalPaved)
	}
}

// TestBuildMeta_SnapshotDateUsesPinnedComputedAt is the yvlv.30 regression:
// for a pinned snapshot (snapshotID > 0) the snapshot_date must reflect that
// snapshot's computed_at, not today — otherwise the time-travel UI shows the
// render date for a months-old snapshot (and freezes it in the lifetime cache).
func TestBuildMeta_SnapshotDateUsesPinnedComputedAt(t *testing.T) {
	computed := time.Date(2025, 11, 3, 9, 30, 0, 0, time.UTC)
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return boundaryGeoJSON, nil },
		ListSnapshotsFunc: func(_ context.Context) ([]db.SnapshotCity, error) {
			return []db.SnapshotCity{
				{ID: 7, CreatedAt: computed},
				{ID: 6, CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
			}, nil
		},
	}
	entry := CityEntry{
		Config: &config.Config{},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
	meta, err := BuildMeta(context.Background(), entry, 7)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	if want := "2025-11-03"; meta.SnapshotDate != want {
		t.Errorf("snapshot_date = %q; want %q (pinned snapshot's computed_at, not today)", meta.SnapshotDate, want)
	}
}

// TestBuildMeta_SnapshotDateLatestIsToday verifies the unpinned/latest path
// (snapshotID <= 0) still reports today's date.
func TestBuildMeta_SnapshotDateLatestIsToday(t *testing.T) {
	entry := newMockEntry(nil)
	meta, err := BuildMeta(context.Background(), entry, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	if want := time.Now().Format(time.DateOnly); meta.SnapshotDate != want {
		t.Errorf("latest snapshot_date = %q; want today %q", meta.SnapshotDate, want)
	}
}

// TestBuildMultiCityMeta_MixedCombinedRollout covers partial-rollout: city A
// has been recomputed (has a combined row), city B has not. The regional
// total sums A's combined + B's per-resource fallback so a not-yet-recomputed
// entry still contributes rather than being dropped. The sum overcounts B's
// buffer overlap, but a missing city is worse than an overcounted one.
func TestBuildMultiCityMeta_MixedCombinedRollout(t *testing.T) {
	cityA := newMockEntryWithBoundary(map[resource.Type]results.Totals{
		mtRoads:    {Area: 1000},
		mtCombined: {Area: 800},
	}, `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`)
	cityA.City.Name = "City A"
	cityA.Slug = "city-a"

	// City B has no combined row — partial-rollout state.
	cityB := newMockEntryWithBoundary(map[resource.Type]results.Totals{
		mtRoads: {Area: 2000},
	}, `{"type":"Polygon","coordinates":[[[-121.5,37.5],[-121.4,37.5],[-121.4,37.6],[-121.5,37.6],[-121.5,37.5]]]}`)
	cityB.City.Name = "City B"
	cityB.Slug = "city-b"

	meta, err := BuildMultiCityMeta(context.Background(), []CityEntry{cityA, cityB}, "Test Region")
	if err != nil {
		t.Fatalf("BuildMultiCityMeta: %v", err)
	}
	// Want: A's combined (800) + B's per-resource fallback (2000) = 2800;
	// 800 would mean B was silently dropped.
	if meta.TotalPaved != 2800 {
		t.Errorf("total_paved = %v; want 2800 (combined_A + sum_B)", meta.TotalPaved)
	}
}

// TestBuildMultiCityMeta_FarApartCitiesUsePerCityUTMZones: two sub-cities in
// different UTM zones (Boston ~71°W zone 19N, San Francisco ~122°W zone 10N)
// must each be projected through their own UTM zone for accurate area.
// Projecting both through one zone picked from the regional bbox center
// inflates the area of whichever city is further from its central meridian.
func TestBuildMultiCityMeta_FarApartCitiesUsePerCityUTMZones(t *testing.T) {
	// ~1 km² square near downtown Boston (42.35°N, -71.06°W).
	const bostonBoundary = `{"type":"Polygon","coordinates":[[[-71.065,42.345],[-71.0546,42.345],[-71.0546,42.354],[-71.065,42.354],[-71.065,42.345]]]}`
	// ~1 km² square near downtown San Francisco (37.77°N, -122.42°W).
	const sfBoundary = `{"type":"Polygon","coordinates":[[[-122.425,37.765],[-122.4137,37.765],[-122.4137,37.774],[-122.425,37.774],[-122.425,37.765]]]}`

	cityA := newMockEntryWithBoundary(map[resource.Type]results.Totals{
		mtRoads: {Area: 1},
	}, bostonBoundary)
	cityA.City.Name = "Boston"
	cityA.Slug = "boston"

	cityB := newMockEntryWithBoundary(map[resource.Type]results.Totals{
		mtRoads: {Area: 1},
	}, sfBoundary)
	cityB.City.Name = "San Francisco"
	cityB.Slug = "san-francisco"

	meta, err := BuildMultiCityMeta(context.Background(), []CityEntry{cityA, cityB}, "Far-Apart Region")
	if err != nil {
		t.Fatalf("BuildMultiCityMeta: %v", err)
	}

	// Reference: each city's accurate area computed in its own UTM zone.
	areaA, err := geo.BoundaryArea(context.Background(), bostonBoundary)
	if err != nil {
		t.Fatalf("BoundaryArea(boston): %v", err)
	}
	areaB, err := geo.BoundaryArea(context.Background(), sfBoundary)
	if err != nil {
		t.Fatalf("BoundaryArea(sf): %v", err)
	}
	want := areaA + areaB
	if rel := math.Abs(meta.CityArea-want) / want; rel > 1e-9 {
		t.Errorf("city_area = %v; want %v (sum of per-city UTM areas, rel diff %.3g)",
			meta.CityArea, want, rel)
	}

	// Projecting both polygons through one UTM zone derived from the regional
	// bbox center (~96°W, zone 15N) inflates the result: Boston and SF are
	// both >20° from that meridian. Compute that single-zone value and assert
	// the per-city result is materially smaller.
	centerLon, centerLat := meta.Center[0], meta.Center[1]
	sharedProj := geo.NewUTMProjector(centerLon, centerLat)
	gA, _, err := geo.GeoJSONToProjectedGeometry(bostonBoundary, sharedProj)
	if err != nil {
		t.Fatalf("project boston through shared zone: %v", err)
	}
	gB, _, err := geo.GeoJSONToProjectedGeometry(sfBoundary, sharedProj)
	if err != nil {
		t.Fatalf("project sf through shared zone: %v", err)
	}
	buggy := gA.Area() + gB.Area()
	if meta.CityArea >= buggy*0.9 {
		t.Errorf("city_area = %v; want noticeably smaller than the shared-zone result %v (>=10%% reduction)",
			meta.CityArea, buggy)
	}
}

// TestBuildMeta_CarriesEstimatedSpaces: the parking stat in meta.json carries
// the compute pass's estimated space count; other resources omit the key.
func TestBuildMeta_CarriesEstimatedSpaces(t *testing.T) {
	entry := hexEntry(t, nil, map[resource.Type]results.Totals{
		rtRoads:      {Area: 1000, Features: 10},
		rtParkingAll: {Area: 300, Features: 3, Spaces: 42},
	})
	meta, err := BuildMeta(t.Context(), entry, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	raw, err := json.Marshal(meta.Stats)
	if err != nil {
		t.Fatal(err)
	}
	var stats []map[string]any
	if err := json.Unmarshal(raw, &stats); err != nil {
		t.Fatal(err)
	}
	for _, st := range stats {
		switch st["resource_type"] {
		case "parking":
			if st["estimated_spaces"] != float64(42) {
				t.Errorf("parking estimated_spaces = %v; want 42", st["estimated_spaces"])
			}
		default:
			if _, ok := st["estimated_spaces"]; ok {
				t.Errorf("%v must omit estimated_spaces", st["resource_type"])
			}
		}
	}
}

// TestHeadingName: before any script runs, a
// multi-city page names the city its selector opens on (the first city of
// the first tag group), not the config's first city.
func TestHeadingName(t *testing.T) {
	single := TemplateData{Name: "Livermore, CA"}
	if got := single.HeadingName(); got != "Livermore, CA" {
		t.Errorf("single-city HeadingName = %q, want the project name", got)
	}
	multi := TemplateData{
		Name:   "Livermore, CA",
		Cities: []CityInfo{{Name: "Livermore, CA"}, {Name: "Piedmont, CA"}},
		TagDimensions: []TagDimension{{Groups: []CityGroup{
			{Tag: "East Bay", Cities: []CityInfo{{Name: "Piedmont, CA"}}},
			{Tag: "Tri-Valley", Cities: []CityInfo{{Name: "Livermore, CA"}}},
		}}},
	}
	if got := multi.HeadingName(); got != "Piedmont, CA" {
		t.Errorf("multi-city HeadingName = %q, want the selector's first city", got)
	}
}

// A page rendered for one city of several (the live server's /play) is headed
// by that city, and the city a page opens on by default is the same one for
// the server and the static export: the selector's first option.
func TestHeadingName_FollowsTheSelectedCity(t *testing.T) {
	if got := citySelectFixture("denver").HeadingName(); got != "Denver, CO" {
		t.Errorf("HeadingName with denver active = %q, want Denver, CO", got)
	}
	if got := citySelectFixture("").HeadingName(); got != "Oakland, CA" {
		t.Errorf("HeadingName with no active city = %q, want the first option, Oakland, CA", got)
	}
	if got := DefaultCitySlug(citySelectFixture("").Cities); got != "oakland" {
		t.Errorf("DefaultCitySlug = %q, want oakland", got)
	}
	if got := DefaultCitySlug(nil); got != "" {
		t.Errorf("DefaultCitySlug(nil) = %q, want empty", got)
	}
}

// TestBuildMeta_MissingResources: a resource whose
// compute had no features, or that was never computed, is named in
// missing_resources so the dashboard does not present it as a real zero.
func TestBuildMeta_MissingResources(t *testing.T) {
	entry := hexEntry(t, nil, map[resource.Type]results.Totals{
		rtRoads:                {Area: 1000, Features: 10},
		resource.TypeSidewalks: {Area: 0, Features: 0},
		// parking: never computed
	})
	meta, err := BuildMeta(t.Context(), entry, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	if want := []string{"parking", "sidewalks"}; !slices.Equal(meta.MissingResources, want) {
		t.Errorf("MissingResources = %v; want %v", meta.MissingResources, want)
	}

	full := hexEntry(t, nil, map[resource.Type]results.Totals{
		rtRoads:                {Area: 1000, Features: 10},
		rtParkingAll:           {Area: 300, Features: 3},
		resource.TypeSidewalks: {Area: 200, Features: 5},
	})
	meta, err = BuildMeta(t.Context(), full, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	raw, _ := json.Marshal(meta)
	if strings.Contains(string(raw), "missing_resources") {
		t.Errorf("a city with every layer must omit missing_resources: %s", raw)
	}
}

// TestBuildMeta_IngestFailures: a configured source
// whose newest attempt failed and that holds no rows is named per resource,
// whether the resource has data from another source (partial) or none
// (failed). A source with an earlier successful fetch keeps its rows and is
// not named, nor is a source the city no longer configures.
func TestBuildMeta_IngestFailures(t *testing.T) {
	entry := hexEntry(t, nil, map[resource.Type]results.Totals{
		rtRoads:                {Area: 1000, Features: 10},
		rtParkingAll:           {Area: 300, Features: 3},
		resource.TypeSidewalks: {Area: 0, Features: 0},
	})
	entry.City.Overpass = true
	entry.City.ArcGISURL = "https://example.com/query"
	failed := db.IngestAttempt{Outcome: db.IngestFailed, Error: "502 Gateway Timeout"}
	ok := db.IngestAttempt{Outcome: db.IngestOK, FeatureCount: 903}
	entry.Store.(*dbtest.MockStore).IngestHistoryFunc = func(context.Context) ([]db.IngestHistory, error) {
		return []db.IngestHistory{
			{Input: "parking", Source: "overpass", Latest: failed, LastFailure: &failed, LastSuccess: &ok},
			{Input: "roads", Source: "arcgis", Latest: ok, LastSuccess: &ok},
			{Input: "roads", Source: "overpass", Latest: failed, LastFailure: &failed},
			{Input: "roads", Source: "tigerweb", Latest: failed, LastFailure: &failed},
			{Input: "sidewalks", Source: "overpass", Latest: failed, LastFailure: &failed},
		}, nil
	}
	meta, err := BuildMeta(t.Context(), entry, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	want := map[string][]string{"roads": {"overpass"}, "sidewalks": {"overpass"}}
	if !maps.EqualFunc(meta.FailingSources, want, slices.Equal) {
		t.Errorf("FailingSources = %v; want %v", meta.FailingSources, want)
	}

	entry.Store.(*dbtest.MockStore).IngestHistoryFunc = func(context.Context) ([]db.IngestHistory, error) {
		return nil, errors.New("database is locked")
	}
	if _, err := BuildMeta(t.Context(), entry, 0); err == nil {
		t.Error("BuildMeta swallowed an ingest-history error; serve would cache the partial meta")
	}
}

// TestBuildMeta_EmptyResources: a missing layer every
// configured source answered empty for is named with those sources, so the
// dashboard and check-site can say upstream maps none rather than asking for
// a re-ingest. A layer with a source never asked, or one that failed, or one
// with data, is not named.
func TestBuildMeta_EmptyResources(t *testing.T) {
	entry := hexEntry(t, nil, map[resource.Type]results.Totals{
		rtRoads:                {Area: 1000, Features: 10},
		rtParkingAll:           {Area: 0, Features: 0},
		resource.TypeSidewalks: {Area: 0, Features: 0},
	})
	entry.City.Overpass = true
	empty := db.IngestAttempt{Outcome: db.IngestEmpty}
	failed := db.IngestAttempt{Outcome: db.IngestFailed, Error: "502"}
	history := []db.IngestHistory{
		{Input: "parking", Source: "overpass", Latest: failed, LastFailure: &failed},
		{Input: "roads", Source: "overpass", Latest: empty, LastSuccess: &empty},
		{Input: "sidewalks", Source: "overpass", Latest: empty, LastSuccess: &empty},
	}
	entry.Store.(*dbtest.MockStore).IngestHistoryFunc = func(context.Context) ([]db.IngestHistory, error) {
		return history, nil
	}
	meta, err := BuildMeta(t.Context(), entry, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	want := map[string][]string{"sidewalks": {"overpass"}}
	if !maps.EqualFunc(meta.EmptySources, want, slices.Equal) {
		t.Errorf("EmptySources = %v; want %v", meta.EmptySources, want)
	}
	if !slices.Contains(meta.MissingResources, "sidewalks") {
		t.Errorf("an empty layer must stay in MissingResources (kept out of rankings): %v", meta.MissingResources)
	}

	// A second configured source never asked: the layer is not known empty.
	entry.City.ArcGISURL = "https://example.com/query"
	if meta, err = BuildMeta(t.Context(), entry, 0); err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	if len(meta.EmptySources) != 0 {
		t.Errorf("EmptySources = %v; want none while arcgis has not answered", meta.EmptySources)
	}
}

// TestBuildMeta_StampFromTheView: an export pins each city to its snapshot
// with snapshotID 0, so meta.json dates the data and names the snapshot from
// the view (ViewSnapshot) rather than reporting today; a latest compute is
// marked and dated by its newest run.
func TestBuildMeta_StampFromTheView(t *testing.T) {
	created := time.Date(2025, 11, 3, 9, 30, 0, 0, time.UTC)
	entry := newMockEntry(nil)
	entry.Store.(*dbtest.MockStore).ViewSnapshotFunc = func(context.Context) (*db.SnapshotCity, error) {
		return &db.SnapshotCity{ID: 9, Name: "v0.5.0", CreatedAt: created}, nil
	}
	meta, err := BuildMeta(context.Background(), entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	if meta.SnapshotDate != "2025-11-03" || meta.Snapshot != "v0.5.0" || meta.LatestCompute {
		t.Errorf("snapshot view stamp = (%q, %q, %v), want (2025-11-03, v0.5.0, false)", meta.SnapshotDate, meta.Snapshot, meta.LatestCompute)
	}

	latest := LatestComputeEntry(newMockEntry(nil), []db.LatestPart{
		{Part: "roads", RunID: 1, ComputedAt: created},
		{Part: "parking", RunID: 2, ComputedAt: created.AddDate(0, 1, 0)},
	})
	meta, err = BuildMeta(context.Background(), latest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if meta.SnapshotDate != "2025-12-03" || meta.Snapshot != "" || !meta.LatestCompute {
		t.Errorf("latest compute stamp = (%q, %q, %v), want (2025-12-03, \"\", true)", meta.SnapshotDate, meta.Snapshot, meta.LatestCompute)
	}
	b, _ := json.Marshal(MetaJSON{})
	if strings.Contains(string(b), `"snapshot"`) || strings.Contains(string(b), "latest_compute") {
		t.Errorf("empty meta marshals %s; snapshot and latest_compute must be omitempty", b)
	}
}

// degenerateBoundaryGeoJSON is a two-part boundary whose second part is a
// collapsed ring that cleaning drops (see geo's
// TestGeoJSONToProjectedGeometryDropped_MultiPolygon).
const degenerateBoundaryGeoJSON = `{"type":"MultiPolygon","coordinates":[` +
	`[[[-121.77,37.68],[-121.76,37.68],[-121.76,37.69],[-121.77,37.69],[-121.77,37.68]]],` +
	`[[[-121.50,37.50],[-121.50,37.50],[-121.50,37.50],[-121.50,37.50]]]]}`

// TestBoundaryArea_DegenerateWarningNamesCity is the v8q4 regression: the
// export-side "dropped degenerate sub-polygons" warning must say which city
// it is about, for a single-city meta and for each sub-city of a multi-city
// sum, or an operator cannot tell which source to fix.
func TestBoundaryArea_DegenerateWarningNamesCity(t *testing.T) {
	var buf bytes.Buffer
	ctx := logs.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)))

	entry := newMockEntryWithBoundary(nil, degenerateBoundaryGeoJSON)
	entry.City.Name = "Degenerate Town"
	if _, err := BuildMeta(ctx, entry, 0); err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	other := newMockEntryWithBoundary(nil, degenerateBoundaryGeoJSON)
	other.City.Name = "Other Place"
	if _, err := summedBoundaryArea(ctx, []CityEntry{other}); err != nil {
		t.Fatalf("summedBoundaryArea: %v", err)
	}

	out := buf.String()
	for _, want := range []string{`city="Degenerate Town"`, `city="Other Place"`} {
		if !strings.Contains(out, want) {
			t.Errorf("warning lacks %s; log:\n%s", want, out)
		}
	}
}
