package export

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/peterstace/simplefeatures/geom"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

var (
	rtRoads       = resource.TypeRoads
	rtRoadsCity   = resource.TypeRoads.With(resource.ScopeCity)
	rtParkingAll  = resource.TypeParking
	rtSidewalkAll = resource.TypeSidewalks
)

// squareHex builds a geo.Hex whose Geom is an axis-aligned square of the
// given side length anchored at the origin. Area == side*side, so tests can
// hand-pick areas relative to the sliver threshold.
func squareHex(t *testing.T, id string, side float64) geo.Hex {
	t.Helper()
	return offsetSquareHex(t, id, 0, 0, side)
}

// offsetSquareHex is squareHex anchored at (ox, oy) instead of the origin —
// useful for tests that need projected coords outside the lon/lat range so
// the reprojection path actually runs.
func offsetSquareHex(t *testing.T, id string, ox, oy, side float64) geo.Hex {
	t.Helper()
	ring := geom.NewLineString(geom.NewSequence([]float64{
		ox, oy,
		ox + side, oy,
		ox + side, oy + side,
		ox, oy + side,
		ox, oy,
	}, geom.DimXY))
	poly := geom.NewPolygon([]geom.LineString{ring})
	return geo.Hex{ID: id, Geom: poly.AsGeometry()}
}

// TestRoundSig covers the significant-figure rounder hexgrid's k goes through.
// The load-bearing property is the last group: a nonzero magnitude stays nonzero
// no matter how small, because the game engine rejects a board with k <= 0 and a
// fixed decimal count would flatten a legal 1e-7 decay_rate to zero.
func TestRoundSig(t *testing.T) {
	cases := []struct {
		name   string
		in     float64
		digits int
		want   float64
	}{
		{"typical blend", 0.03712345, 4, 0.03712},
		{"rounds up", 0.0399999, 4, 0.04},
		{"exact power of ten", 0.1, 4, 0.1},
		{"above one", 704.5835576057434, 4, 704.6},
		{"negative", -0.03712345, 4, -0.03712},
		{"zero stays zero", 0, 4, 0},
		// A fixed 2dp (or even 6dp) round maps all three of these to 0.
		{"tiny override", 1e-7, 4, 1e-7},
		{"tiny with mantissa", 1.234567e-7, 4, 1.235e-7},
		{"absurdly tiny", 1e-300, 4, 1e-300},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := roundSig(c.in, c.digits)
			if math.Abs(got-c.want) > math.Abs(c.want)*1e-12 {
				t.Errorf("roundSig(%v, %d) = %v; want %v", c.in, c.digits, got, c.want)
			}
			if c.in != 0 && got == 0 {
				t.Errorf("roundSig(%v, %d) flattened a nonzero value to 0", c.in, c.digits)
			}
		})
	}

	// digits below 1 is a caller error, treated as one significant figure. The
	// wrong answer here is 0.037 back unrounded — the scaling collapses and the
	// overflow fallback hands the input straight through, doing nothing.
	for _, digits := range []int{0, -3} {
		if got := roundSig(0.037, digits); got != 0.04 {
			t.Errorf("roundSig(0.037, %d) = %v; want 0.04 (digits clamped to 1)", digits, got)
		}
	}

	// Denormals scale to +Inf and would otherwise yield NaN; they pass through.
	if got := roundSig(5e-324, 4); got != 5e-324 {
		t.Errorf("roundSig(denormal) = %v; want the input unchanged", got)
	}
	if got := roundSig(math.NaN(), 4); !math.IsNaN(got) {
		t.Errorf("roundSig(NaN) = %v; want NaN", got)
	}
	if got := roundSig(math.Inf(1), 4); !math.IsInf(got, 1) {
		t.Errorf("roundSig(+Inf) = %v; want +Inf", got)
	}
}

// TestFilterHexSlivers_DropsBelowThreshold pins the heatmap contract: hexes
// whose clipped area falls under config.DefaultMinHexArea (100) are omitted from
// hex.geojson; hexes ≥ the threshold are kept. The check is strict-less-than,
// so a hex at exactly the threshold survives.
//
// Regression caught: flipping the comparator (< → <=), moving the threshold,
// or skipping the filter entirely produces tile-edge slivers that render as
// 100% coverage in the UI.
func TestFilterHexSlivers_DropsBelowThreshold(t *testing.T) {
	cases := []struct {
		name string
		side float64
		keep bool
	}{
		{"sliver_below_threshold", 9, false}, // 81 sqm
		{"at_threshold", 10, true},           // 100 sqm — boundary inclusive (< is strict)
		{"above_threshold", 11, true},        // 121 sqm
	}

	var input []geo.Hex
	wantIDs := map[string]bool{}
	for _, tc := range cases {
		input = append(input, squareHex(t, tc.name, tc.side))
		if tc.keep {
			wantIDs[tc.name] = true
		}
	}

	got := filterHexSlivers(input, config.DefaultMinHexArea)

	gotIDs := map[string]bool{}
	for _, h := range got {
		gotIDs[h.ID] = true
	}
	for id := range wantIDs {
		if !gotIDs[id] {
			t.Errorf("hex %q was dropped; want kept", id)
		}
	}
	for id := range gotIDs {
		if !wantIDs[id] {
			t.Errorf("hex %q was kept; want dropped", id)
		}
	}
}

// TestCityHexGrid_HonorsPerCityMinHexArea pins l51o's runtime half: the shared
// grid resolves the sliver threshold per city (Config.ResolvedMinHexArea), not
// from the top level alone, so a city that tuned min_hex_area alongside its hex
// edge actually gets its threshold. The /play board draws the same
// hexgrid.geojson, so it moves with the heatmap.
func TestCityHexGrid_HonorsPerCityMinHexArea(t *testing.T) {
	ctx := context.Background()
	entry := hexEntry(t, nil, nil)
	_, lon0, lat0, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon0, lat0)

	base, err := cityHexGrid(ctx, entry, proj)
	if err != nil {
		t.Fatalf("cityHexGrid (default threshold): %v", err)
	}
	if len(base) == 0 {
		t.Fatal("cityHexGrid returned no hexes for the default threshold")
	}

	// A full 200 m-edge hex is 3√3/2·200² ≈ 103.9k sqm, so a 100k threshold
	// keeps only unclipped hexes and drops every boundary sliver.
	tuned := hexEntry(t, nil, nil)
	tuned.City.MinHexArea = 100_000
	got, err := cityHexGrid(ctx, tuned, proj)
	if err != nil {
		t.Fatalf("cityHexGrid (per-city threshold): %v", err)
	}
	if len(got) >= len(base) {
		t.Errorf("per-city min_hex_area had no effect: %d hexes vs %d at the default threshold",
			len(got), len(base))
	}
	if len(got) == 0 {
		t.Fatal("test setup: per-city threshold dropped every hex, so the comparison is vacuous")
	}

	// The same value set at the top level (with no per-city override) must
	// produce the identical grid — the per-city layer is an override, not a
	// separate mechanism.
	topLevel := hexEntry(t, nil, nil)
	topLevel.Config.Display.MinHexArea = 100_000
	viaTop, err := cityHexGrid(ctx, topLevel, proj)
	if err != nil {
		t.Fatalf("cityHexGrid (top-level threshold): %v", err)
	}
	if len(viaTop) != len(got) {
		t.Errorf("top-level threshold kept %d hexes; per-city kept %d — the layers must resolve to the same grid",
			len(viaTop), len(got))
	}
}

// hexEntry builds a CityEntry whose store holds the given per-hex coverage
// and city totals, keyed by resource type (scope included).
func hexEntry(t *testing.T, hexStats map[resource.Type][]results.HexCoverage, totals map[resource.Type]results.Totals) CityEntry {
	t.Helper()
	var hexRows []db.HexMetric
	for _, rt := range slices.Sorted(maps.Keys(hexStats)) {
		hexRows = append(hexRows, results.HexCoverageRows(rt, hexStats[rt])...)
	}
	store := &dbtest.MockStore{
		CityMetrics: totalsFixture(totals),
		HexMetrics:  hexRows,
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

// TestBuildHexGeoJSON_EmittedWhenRowsExist: a single FeatureCollection is
// returned when any per-hex coverage exists (across either scope), and nil only
// when there are no rows at all. The per-feature {all, city?} shape is pinned
// deterministically in TestBuildHexFeature_NestedScopes.
func TestBuildHexGeoJSON_EmittedWhenRowsExist(t *testing.T) {
	cityRows := []results.HexCoverage{{HexID: "0,0", Area: 100, PctCovered: 50}}
	allRows := []results.HexCoverage{{HexID: "0,0", Area: 200, PctCovered: 75}}
	entry := hexEntry(t, map[resource.Type][]results.HexCoverage{
		rtRoads:       allRows,
		rtRoadsCity:   cityRows,
		rtParkingAll:  nil,
		rtSidewalkAll: nil,
	}, nil)

	_, lon, lat, err := entry.BBoxAndCenter(t.Context())
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon, lat)

	fc, err := BuildHexGeoJSON(t.Context(), entry, proj)
	if err != nil {
		t.Fatalf("BuildHexGeoJSON: %v", err)
	}
	if fc == nil {
		t.Fatal("BuildHexGeoJSON returned nil when rows exist")
	}
	if fc["type"] != "FeatureCollection" {
		t.Errorf("type = %v; want FeatureCollection", fc["type"])
	}

	// Resource keys inside the scope objects are bare names, not "roads:city".
	feats, _ := fc["features"].([]map[string]any)
	for _, f := range feats {
		props := f["properties"].(map[string]any)
		for _, scope := range []string{"all", "city"} {
			byRes, ok := props[scope].(map[string]map[string]float64)
			if !ok {
				continue
			}
			for rt := range byRes {
				if strings.Contains(rt, ":") {
					t.Errorf("scope %q resource key %q must not carry the :city suffix", scope, rt)
				}
			}
		}
	}
}

// TestBuildHexFeature_NestedScopes pins the per-feature shape: "id" plus "all"
// keyed by bare resource name -> {"area", "share"}, plus exactly one of "city"
// (city-scope coverage that DIFFERS from all), "city_same" (coverage identical
// to all, elided), or neither (no city-scope coverage at all).
//
// The differing values below are load-bearing: equal ones would take the
// city_same branch and this test would stop covering the "city" object.
func TestBuildHexFeature_NestedScopes(t *testing.T) {
	proj := geo.NewUTMProjector(-122.45, 37.55)
	h := offsetSquareHex(t, "0,0", 550000, 4156000, 50)
	hexMap := map[string]*geo.Hex{"0,0": &h}

	// Hex with both scopes.
	agg := &hexAgg{
		all:  map[string]map[string]float64{"roads": {"area": 200, "share": 0.75}},
		city: map[string]map[string]float64{"roads": {"area": 100, "share": 0.5}},
	}
	feat, ok := encodedHexFeature("0,0", agg, hexMap, proj, 6)
	if !ok {
		t.Fatal("buildHexFeature returned ok=false")
	}
	props := feat["properties"].(map[string]any)
	if props["id"] != "0,0" {
		t.Errorf("id = %v; want 0,0", props["id"])
	}
	all := props["all"].(map[string]map[string]float64)
	if all["roads"]["area"] != 200 || all["roads"]["share"] != 0.75 {
		t.Errorf("all.roads = %v; want {area:200, share:0.75}", all["roads"])
	}
	city := props["city"].(map[string]map[string]float64)
	if city["roads"]["area"] != 100 || city["roads"]["share"] != 0.5 {
		t.Errorf("city.roads = %v; want {area:100, share:0.5}", city["roads"])
	}

	if _, ok := props["city_same"]; ok {
		t.Error("city_same must be absent when the scopes differ")
	}

	// Hex with no city coverage omits BOTH keys. That pair-absence — not the
	// absence of "city" alone — is what tells the client there is no city-scope
	// data and keeps the scope toggle hidden.
	allOnly := &hexAgg{all: map[string]map[string]float64{"roads": {"area": 200, "share": 0.75}}}
	feat2, _ := encodedHexFeature("0,0", allOnly, hexMap, proj, 6)
	props2 := feat2["properties"].(map[string]any)
	if _, ok := props2["city"]; ok {
		t.Error("city key must be absent when the hex has no city coverage")
	}
	if _, ok := props2["city_same"]; ok {
		t.Error("city_same must be absent when the hex has no city coverage")
	}
}

// TestBuildHexFeature_CitySameElidesDuplicate pins B6b's dedup: city coverage
// byte-identical to all is emitted as "city_same": 1 instead of a duplicate
// object. About 70% of features site-wide take this branch, so a regression
// here is a ~28 MiB size regression — and, if the client half were ever
// reverted, a silent under-report of the entire city scope rather than an error.
func TestBuildHexFeature_CitySameElidesDuplicate(t *testing.T) {
	proj := geo.NewUTMProjector(-122.45, 37.55)
	h := offsetSquareHex(t, "0,0", 550000, 4156000, 50)
	hexMap := map[string]*geo.Hex{"0,0": &h}

	// Equal values in two independently-allocated maps: the dedup must compare
	// by value, not by identity.
	agg := &hexAgg{
		all:  map[string]map[string]float64{"roads": {"area": 200, "share": 0.75}},
		city: map[string]map[string]float64{"roads": {"area": 200, "share": 0.75}},
	}
	feat, ok := encodedHexFeature("0,0", agg, hexMap, proj, 6)
	if !ok {
		t.Fatal("buildHexFeature returned ok=false")
	}
	props := feat["properties"].(map[string]any)
	if _, ok := props["city"]; ok {
		t.Error("city object must be elided when it equals all")
	}
	if props["city_same"] != 1 {
		t.Errorf("city_same = %v; want 1", props["city_same"])
	}

	// A difference in ANY resource, or a differing resource set, must fall back
	// to emitting the full object — otherwise real city-scope data is lost.
	for name, city := range map[string]map[string]map[string]float64{
		"one value differs":  {"roads": {"area": 200, "share": 0.74}},
		"extra resource":     {"roads": {"area": 200, "share": 0.75}, "parking": {"area": 1, "share": 0.01}},
		"missing resource":   {},
		"different resource": {"parking": {"area": 200, "share": 0.75}},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := encodedHexFeature("0,0", &hexAgg{
				all:  map[string]map[string]float64{"roads": {"area": 200, "share": 0.75}},
				city: city,
			}, hexMap, proj, 6)
			p := f["properties"].(map[string]any)
			if _, ok := p["city_same"]; ok {
				t.Error("must not dedup when the scopes differ")
			}
			if _, ok := p["city"]; !ok {
				t.Error("differing city coverage must be emitted in full")
			}
		})
	}
}

// TestBuildHexGeoJSON_CarriesFormatVersion pins the version the client gates
// on. Without it an older client silently drops ~70% of city-scope features
// instead of showing the error banner.
func TestBuildHexGeoJSON_CarriesFormatVersion(t *testing.T) {
	entry := hexEntry(t, map[resource.Type][]results.HexCoverage{
		rtRoads: {{HexID: "0,0", Area: 200, PctCovered: 75}},
	}, nil)
	_, lon, lat, err := entry.BBoxAndCenter(t.Context())
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	fc, err := BuildHexGeoJSON(t.Context(), entry, geo.NewUTMProjector(lon, lat))
	if err != nil {
		t.Fatalf("BuildHexGeoJSON: %v", err)
	}
	if fc["v"] != HexGridFileVersion || HexGridFileVersion != 6 {
		t.Errorf("v = %v; want 6 — the client refuses to render an unrecognized version", fc["v"])
	}
}

// TestBuildHexFeature_RespectsCoordinatePrecision pins the wiring from
// the decimals parameter down through buildHexFeature → the
// precision-aware geo helper. Exercises buildHexFeature directly with a
// known hex (no dependency on grid-generation ID matching) and asserts
// the lower-precision pass emits fewer significant fractional digits.
//
// Regression caught: dropping the decimals plumb-through (forgetting to
// thread the value into buildHexFeature, or calling the default-precision
// GeometryToGeoJSON inadvertently).
func TestBuildHexFeature_RespectsCoordinatePrecision(t *testing.T) {
	proj := geo.NewUTMProjector(-122.45, 37.55)
	// Place the hex at realistic UTM coords (zone 10N, ~SF Bay) — values
	// well outside the [-180..180, -90..90] lon/lat window so the
	// reprojector actually runs (it's a no-op on already-lon/lat coords).
	h := offsetSquareHex(t, "0,0", 550000, 4156000, 50)
	hexMap := map[string]*geo.Hex{"0,0": &h}
	agg := &hexAgg{all: map[string]map[string]float64{"roads": {"area": 200, "share": 0.75}}}

	maxFrac := func(decimals int) int {
		feat, ok := encodedHexFeature("0,0", agg, hexMap, proj, decimals)
		if !ok {
			t.Fatalf("buildHexFeature returned ok=false at decimals=%d", decimals)
		}
		raw, _ := feat["geometry"].(json.RawMessage)
		maxDigits := 0
		for tok := range strings.SplitSeq(string(raw), ",") {
			dot := strings.IndexByte(tok, '.')
			if dot < 0 {
				continue
			}
			end := len(tok)
			for end > dot+1 && (tok[end-1] < '0' || tok[end-1] > '9') {
				end--
			}
			if frac := end - dot - 1; frac > maxDigits {
				maxDigits = frac
			}
		}
		return maxDigits
	}

	hi := maxFrac(7)
	lo := maxFrac(4)
	if hi <= lo {
		t.Errorf("precision=7 max fractional digits (%d) must exceed precision=4 (%d)", hi, lo)
	}
	if lo > 4 {
		t.Errorf("precision=4 produced coords with %d fractional digits; want ≤ 4", lo)
	}
}

// TestBuildHexGeoJSON_NoCityRowsOmitsCity: cities without ":city"-suffixed
// coverage rows still yield a FeatureCollection, but no feature carries a "city"
// object. The client uses that absence as the "hide the scope toggle" signal.
func TestBuildHexGeoJSON_NoCityRowsOmitsCity(t *testing.T) {
	allRows := []results.HexCoverage{{HexID: "0,0", Area: 100, PctCovered: 50}}
	entry := hexEntry(t, map[resource.Type][]results.HexCoverage{
		rtRoads:       allRows,
		rtParkingAll:  nil,
		rtSidewalkAll: nil,
	}, nil)

	_, lon, lat, _ := entry.BBoxAndCenter(t.Context())
	fc, err := BuildHexGeoJSON(t.Context(), entry, geo.NewUTMProjector(lon, lat))
	if err != nil {
		t.Fatalf("BuildHexGeoJSON: %v", err)
	}
	if fc == nil {
		t.Fatal("FC must be non-nil when all rows exist")
	}
	feats, _ := fc["features"].([]map[string]any)
	for _, f := range feats {
		if _, ok := f["properties"].(map[string]any)["city"]; ok {
			t.Error("no feature may carry a city object when no :city rows exist")
		}
	}
}

// TestBuildHexCostSummary_NestedByScope: the summary is
// map[scope]map[resource]{...}. Both "city" and "all" keys appear when
// AllBaseline is set on the forecast export (the marker that compute produced
// both scopes).
func TestBuildHexCostSummary_NestedByScope(t *testing.T) {
	allBaseline := forecast.ScenarioResult{
		Years: []forecast.ScenarioYear{{AnnualNeed: 50000}},
	}
	forecasts := []ForecastExport{{
		ResourceType:  "roads",
		Baseline:      forecast.ScenarioResult{Years: []forecast.ScenarioYear{{AnnualNeed: 25000}}},
		AllBaseline:   &allBaseline,
		totalArea:     2000,
		cityTotalArea: 1000,
		hasCityTotal:  true,
	}}

	out := BuildHexCostSummary(forecasts)

	city, ok := out["city"]
	if !ok {
		t.Fatalf("missing 'city' key in %v", out)
	}
	all, ok := out["all"]
	if !ok {
		t.Fatalf("missing 'all' key in %v", out)
	}
	if city["roads"]["year1_need"] != 25000 {
		t.Errorf("city roads year1_need = %v; want 25000 (from Baseline)", city["roads"]["year1_need"])
	}
	if city["roads"]["total_area"] != 1000 {
		t.Errorf("city roads total_area = %v; want 1000 (city-scope row)", city["roads"]["total_area"])
	}
	if all["roads"]["year1_need"] != 50000 {
		t.Errorf("all roads year1_need = %v; want 50000 (from AllBaseline)", all["roads"]["year1_need"])
	}
	if all["roads"]["total_area"] != 2000 {
		t.Errorf("all roads total_area = %v; want 2000 (all-scope row)", all["roads"]["total_area"])
	}
}

// TestBuildHexCostSummary_AllOnlyWhenNoCity: when AllBaseline is nil
// (single-scope compute), the output has only the "all" key and
// Baseline carries the all-scope numbers.
func TestBuildHexCostSummary_AllOnlyWhenNoCity(t *testing.T) {
	forecasts := []ForecastExport{{
		ResourceType: "roads",
		Baseline:     forecast.ScenarioResult{Years: []forecast.ScenarioYear{{AnnualNeed: 50000}}},
		// AllBaseline left nil → no city scope.
		totalArea: 2000,
	}}

	out := BuildHexCostSummary(forecasts)

	if _, ok := out["city"]; ok {
		t.Errorf("'city' key must be absent when AllBaseline is nil; got %v", out)
	}
	if out["all"]["roads"]["year1_need"] != 50000 {
		t.Errorf("all roads year1_need = %v; want 50000", out["all"]["roads"]["year1_need"])
	}
}

// TestBuildHexCostSummary_TotalsFromForecastRead: the summary's areas are the
// totals the forecasts were built from, and it makes no read of its own, so a
// store that fails after the forecasts cannot leave a partial summary. A
// resource without a city-scope total stays out of the "city" sub-map.
func TestBuildHexCostSummary_TotalsFromForecastRead(t *testing.T) {
	entry := hexEntry(t, nil, map[resource.Type]results.Totals{
		rtRoads:      {Area: 2000, Features: 10},
		rtRoadsCity:  {Area: 1000, Features: 5},
		rtParkingAll: {Area: 300, Features: 3},
	})
	store := entry.Store.(*dbtest.MockStore)
	// A city-scope cohort split is what gives the city its city-maintained view.
	store.CityMetrics = append(store.CityMetrics,
		results.CohortRows(rtRoadsCity, []results.Cohort{{Class: "residential", Area: 1000, Features: 5}})...)
	fc := config.ForecastConfig{HorizonYears: 5, InitialPCI: 70}
	forecasts, err := BuildForecastsForCity(t.Context(), entry, &fc, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.ListCityMetricsFunc = func(context.Context, []string) ([]db.CityMetric, error) {
		t.Error("BuildHexCostSummary read the store")
		return nil, errors.New("read after forecasts")
	}

	out := BuildHexCostSummary(forecasts)

	if got := out["all"]["roads"]["total_area"]; got != 2000 {
		t.Errorf("all roads total_area = %v, want 2000", got)
	}
	if got := out["all"]["parking"]["total_area"]; got != 300 {
		t.Errorf("all parking total_area = %v, want 300", got)
	}
	if got := out["city"]["roads"]["total_area"]; got != 1000 {
		t.Errorf("city roads total_area = %v, want 1000", got)
	}
	if _, ok := out["city"]["parking"]; ok {
		t.Errorf("city parking present without a city-scope total: %v", out["city"])
	}
}

// TestBuildScenariosData_ScopeKeys: with :city compute rows, scenarios.json
// carries exactly the "city" and "all" scope keys — no summary or feature
// counts (the site publishes none).
func TestBuildScenariosData_ScopeKeys(t *testing.T) {
	totals := map[resource.Type]results.Totals{
		rtRoads:     {Area: 2000, Features: 100},
		rtRoadsCity: {Area: 1000, Features: 60},
	}
	entry := hexEntry(t, nil, totals)
	fc := &config.ForecastConfig{HorizonYears: 5, InitialPCI: 85, DecayRate: 1.5}

	out, err := BuildScenariosData(t.Context(), entry, fc)
	if err != nil {
		t.Fatalf("BuildScenariosData: %v", err)
	}
	if _, ok := out["city"]; !ok {
		t.Errorf("'city' key missing; got %v", out)
	}
	if _, ok := out["all"]; !ok {
		t.Errorf("'all' key missing when city data exists; got %v", out)
	}
	if len(out) != 2 {
		t.Errorf("scenarios.json keys = %v; want exactly city and all", scenarioKeys(out))
	}
}

func scenarioKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestBuildScenariosData_AllOnlyKey: when only all-scope compute rows exist,
// the output has just the "all" key — no "city".
func TestBuildScenariosData_AllOnlyKey(t *testing.T) {
	totals := map[resource.Type]results.Totals{
		rtRoads: {Area: 2000, Features: 100},
	}
	entry := hexEntry(t, nil, totals)
	fc := &config.ForecastConfig{HorizonYears: 5, InitialPCI: 85, DecayRate: 1.5}

	out, err := BuildScenariosData(t.Context(), entry, fc)
	if err != nil {
		t.Fatalf("BuildScenariosData: %v", err)
	}
	if _, ok := out["city"]; ok {
		t.Errorf("'city' key must be absent when no :city rows; got %v", out)
	}
	if _, ok := out["all"]; !ok {
		t.Errorf("'all' key missing; got %v", out)
	}
	if len(out) != 1 {
		t.Errorf("scenarios.json keys = %v; want just all", scenarioKeys(out))
	}
}

// projectedLonLatSquare builds a hex whose Geom is the lon/lat square
// [(lon0,lat0),(lon1,lat1)] projected into proj's coordinate system — i.e. in
// the same projected space the boundary clip operates in. Used to place
// test hexes at known positions relative to a lon/lat boundary fixture.
func projectedLonLatSquare(t *testing.T, proj *geo.UTMProjector, id string, lon0, lat0, lon1, lat1 float64) geo.Hex {
	t.Helper()
	corners := [][2]float64{{lon0, lat0}, {lon1, lat0}, {lon1, lat1}, {lon0, lat1}, {lon0, lat0}}
	flat := make([]float64, 0, len(corners)*2)
	for _, c := range corners {
		x, y := proj.ToProjected(c[0], c[1])
		flat = append(flat, x, y)
	}
	ring := geom.NewLineString(geom.NewSequence(flat, geom.DimXY))
	return geo.Hex{ID: id, Geom: geom.NewPolygon([]geom.LineString{ring}).AsGeometry()}
}

// TestSharedClip_MatchesUnconditionalOverlay pins the clip compute and export
// share (geo.ClipHexesToBoundaryGeoJSON) against the plain definition of a
// clipped grid: it must keep exactly the hexes, with the same clipped areas,
// that calling geom.Intersection on every hex against the boundary keeps.
//
// The boundary is a square with a hole, and the hexes deliberately hit every
// branch: fully interior (kept as-is), perimeter-straddling (clipped), fully
// outside (dropped), straddling the hole (the hole is clipped out), and fully
// inside the hole (dropped).
//
// Regression caught: a fast path that drops an edge-touching hex, or keeps a
// hex unclipped where the hole or the boundary should have trimmed it.
func TestSharedClip_MatchesUnconditionalOverlay(t *testing.T) {
	proj := geo.NewUTMProjector(-122.45, 37.55)
	// Square boundary (-122.5,37.5)-(-122.4,37.6) with a hole
	// (-122.47,37.53)-(-122.43,37.57).
	const boundaryWithHole = `{"type":"Polygon","coordinates":[` +
		`[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]],` +
		`[[-122.47,37.53],[-122.43,37.53],[-122.43,37.57],[-122.47,37.57],[-122.47,37.53]]]}`
	hexes := []geo.Hex{
		projectedLonLatSquare(t, proj, "interior", -122.495, 37.505, -122.49, 37.51),      // Covers → keep
		projectedLonLatSquare(t, proj, "straddle-edge", -122.405, 37.55, -122.395, 37.56), // overlay
		projectedLonLatSquare(t, proj, "outside", -122.30, 37.55, -122.29, 37.56),         // drop
		projectedLonLatSquare(t, proj, "over-hole", -122.46, 37.52, -122.44, 37.54),       // overlay (hole)
		projectedLonLatSquare(t, proj, "inside-hole", -122.45, 37.55, -122.448, 37.552),   // drop (disjoint)
	}

	// Reference: the pre-optimization behavior — unconditional overlay per hex.
	boundaryGeom, _, err := geo.GeoJSONToProjectedGeometry(boundaryWithHole, proj)
	if err != nil {
		t.Fatalf("project boundary: %v", err)
	}
	wantAreas := map[string]float64{}
	for _, h := range hexes {
		inter, iErr := geom.Intersection(h.Geom, boundaryGeom)
		if iErr == nil && !inter.IsEmpty() {
			wantAreas[h.ID] = inter.Area()
		}
	}

	got, err := geo.ClipHexesToBoundaryGeoJSON(t.Context(), hexes, boundaryWithHole, proj, nil)
	if err != nil {
		t.Fatalf("clip: %v", err)
	}
	gotAreas := map[string]float64{}
	for _, h := range got {
		gotAreas[h.ID] = h.Geom.Area()
	}

	if len(gotAreas) != len(wantAreas) {
		t.Fatalf("kept %d hexes %v; reference kept %d %v", len(gotAreas), keysOf(gotAreas), len(wantAreas), keysOf(wantAreas))
	}
	for id, want := range wantAreas {
		got, ok := gotAreas[id]
		if !ok {
			t.Errorf("hex %q dropped by prepared path; reference kept it (area %.2f)", id, want)
			continue
		}
		// Interior hexes keep their original (un-snapped) geometry, so allow a
		// sub-cm² tolerance against the overlay's precision-modeled area.
		if diff := got - want; diff > 0.01 || diff < -0.01 {
			t.Errorf("hex %q area = %.4f; reference overlay = %.4f (diff %.4f)", id, got, want, diff)
		}
	}

	// Branch coverage sanity: the fixture must actually exercise keep + drop.
	for _, id := range []string{"interior", "straddle-edge", "over-hole"} {
		if _, ok := gotAreas[id]; !ok {
			t.Errorf("expected hex %q to be kept", id)
		}
	}
	for _, id := range []string{"outside", "inside-hole"} {
		if _, ok := gotAreas[id]; ok {
			t.Errorf("expected hex %q to be dropped", id)
		}
	}
}

func keysOf(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestBuildHexGeoJSON_DefaultCoordinatePrecision pins the precision an export
// actually EMITS when coordinate_decimals is unset.
//
// Everything else in this area pins the plumbing rather than the outcome:
// validate_test.go asserts against the constant (so it tracks any change), and
// TestBuildHexFeature_RespectsCoordinatePrecision passes 7 and 4 explicitly.
// Nothing observed the default, which means a revert of it — the single
// highest-leverage byte knob in the export, spanning the two largest files —
// would not fail a single test. The size budgets in check-site are a proxy, not
// an assertion.
func TestBuildHexGeoJSON_DefaultCoordinatePrecision(t *testing.T) {
	if config.DefaultCoordinateDecimals != 5 {
		t.Fatalf("DefaultCoordinateDecimals = %d; want 5. If this was changed deliberately, "+
			"update this test AND re-seed the per-city size budgets in check_sizes.go",
			config.DefaultCoordinateDecimals)
	}

	// Resolve through an unset config, which is the step under test: an export
	// that sets nothing must land on the default.
	cfg := &config.Config{}
	if cfg.Export.CoordinateDecimals != 0 {
		t.Fatal("fixture must leave coordinate_decimals unset")
	}
	decimals := cfg.CoordinateDecimals()
	if decimals != config.DefaultCoordinateDecimals {
		t.Fatalf("CoordinateDecimals() = %d; want the default %d", decimals, config.DefaultCoordinateDecimals)
	}

	// A hex at realistic UTM coords so the reprojection (and its rounding)
	// actually runs — it is a no-op on values already in the lon/lat window.
	proj := geo.NewUTMProjector(-122.45, 37.55)
	h := offsetSquareHex(t, "0,0", 550000, 4156000, 50)
	hexMap := map[string]*geo.Hex{"0,0": &h}
	agg := &hexAgg{all: map[string]map[string]float64{"roads": {"area": 200, "share": 0.75}}}

	feat, ok := encodedHexFeature("0,0", agg, hexMap, proj, decimals)
	if !ok {
		t.Fatal("buildHexFeature returned ok=false")
	}
	raw, _ := feat["geometry"].(json.RawMessage)
	if len(raw) == 0 {
		t.Fatal("no geometry emitted")
	}
	sawFraction := false
	for tok := range strings.SplitSeq(string(raw), ",") {
		dot := strings.IndexByte(tok, '.')
		if dot < 0 {
			continue
		}
		end := len(tok)
		for end > dot+1 && (tok[end-1] < '0' || tok[end-1] > '9') {
			end--
		}
		frac := end - dot - 1
		if frac > 0 {
			sawFraction = true
		}
		if frac > config.DefaultCoordinateDecimals {
			t.Errorf("coordinate %q carries %d fractional digits; want <= %d",
				tok, frac, config.DefaultCoordinateDecimals)
		}
	}
	if !sawFraction {
		t.Error("no fractional coordinates found; the test parsed nothing and would pass vacuously")
	}
}

// TestBuildHexGeoJSON_ParkingSpaces pins the "spaces" key: a parking hex
// carries its apportioned spaces (stored in hex_metrics under each scope)
// rounded to a whole count, a sub-space sliver and every non-parking resource
// carry no key, and — because both scopes round through the same function — an
// identical city scope still dedups to city_same rather than silently emitting
// a duplicate object.
func TestBuildHexGeoJSON_ParkingSpaces(t *testing.T) {
	const run = int64(7)
	parkingCity := rtParkingAll.With(resource.ScopeCity)
	entry := hexEntry(t, map[resource.Type][]results.HexCoverage{
		rtRoads: {{HexID: "0,0", Area: 200, PctCovered: 75}},
		rtParkingAll: {{HexID: "0,0", Area: 90, PctCovered: 30},
			{HexID: "0,1", Area: 9, PctCovered: 3}},
		parkingCity: {{HexID: "0,0", Area: 90, PctCovered: 30},
			{HexID: "0,1", Area: 9, PctCovered: 3}},
	}, nil)
	store := entry.Store.(*dbtest.MockStore)
	for _, subject := range []string{"parking", "parking:city"} {
		store.HexMetrics = append(store.HexMetrics,
			db.HexMetric{HexID: "0,0", Metric: resource.MetricParkingSpaces, Subject: subject, Value: 12.4, RunID: run},
			db.HexMetric{HexID: "0,1", Metric: resource.MetricParkingSpaces, Subject: subject, Value: 0.3, RunID: run})
	}
	aggs, err := aggregateHexStats(t.Context(), entry)
	if err != nil {
		t.Fatalf("aggregateHexStats: %v", err)
	}
	a := aggs["0,0"]
	if got := a.all["parking"]["spaces"]; got != 12 {
		t.Errorf("parking spaces = %v; want 12 (rounded)", got)
	}
	if _, ok := a.all["roads"]["spaces"]; ok {
		t.Error("roads must carry no spaces key")
	}
	if _, ok := aggs["0,1"].all["parking"]["spaces"]; ok {
		t.Error("a sub-space sliver (0.3) must carry no spaces key")
	}
	if !sameCoverage(a.city["parking"], a.all["parking"]) {
		t.Errorf("identical parking scopes must still compare equal with spaces present: city=%v all=%v", a.city["parking"], a.all["parking"])
	}
}

// TestPavementKeysNotStoredSpecs guards the pavement metric keys from being
// registered as stored hex metrics: a stored spec would read the
// resource-subject rows as if they were one city-wide metric, duplicating the
// derived Color-by of the same name and ignoring the resource and scope
// toggles. Registering a key as a derived spec is fine.
func TestPavementKeysNotStoredSpecs(t *testing.T) {
	for _, key := range []string{resource.MetricPavedArea, resource.MetricCoveragePct, resource.MetricFeatureCount, resource.MetricParkingSpaces} {
		if spec, ok := hexmetric.Lookup(key); ok && !spec.Derived() {
			t.Errorf("%s is a stored hexmetric spec; pavement keys must stay derived or unregistered", key)
		}
	}
}

// TestBuildHexGeoJSON_EmitsEveryGridHex pins that every hex of the
// clipped grid is a feature, and a hex without pavement rows carries only its
// id (geometry for stored metrics such as population, nothing for the
// pavement colorings).
func TestBuildHexGeoJSON_EmitsEveryGridHex(t *testing.T) {
	probe := hexEntry(t, nil, nil)
	_, lon, lat, err := probe.BBoxAndCenter(t.Context())
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon, lat)
	grid, err := cityHexGrid(t.Context(), probe, proj)
	if err != nil || len(grid) < 2 {
		t.Fatalf("cityHexGrid: %d hexes, err %v; need at least 2", len(grid), err)
	}
	paved := grid[0].ID
	entry := hexEntry(t, map[resource.Type][]results.HexCoverage{
		rtRoads: {{HexID: paved, Area: 100, PctCovered: 50}},
	}, nil)

	fc, err := BuildHexGeoJSON(t.Context(), entry, proj)
	if err != nil {
		t.Fatalf("BuildHexGeoJSON: %v", err)
	}
	feats, _ := fc["features"].([]map[string]any)
	if len(feats) != len(grid) {
		t.Fatalf("features = %d, want one per grid hex (%d)", len(feats), len(grid))
	}
	for _, f := range feats {
		props := f["properties"].(map[string]any)
		_, hasAll := props["all"]
		if id := props["id"]; id == paved {
			if !hasAll {
				t.Errorf("paved hex %s lost its coverage", id)
			}
			continue
		}
		if hasAll || len(props) != 1 {
			t.Errorf("pavement-free hex must carry only its id, got %v", props)
		}
	}
}

// TestBuildHexGeoJSON_RoadDecayRate pins hexgrid's "k", the /play board's
// per-hex decay rate: read from the stored default-rate blend
// (resource.MetricDefaultDecayRate, never from road features — roadEntry fails
// the test on ListFeatures), present only on city-jurisdiction road hexes, and
// scaled by the decay_rate override exactly as each road class is
// (forecast.ClassDecayRate), so a blend of one class lands on that class's
// overridden rate.
func TestBuildHexGeoJSON_RoadDecayRate(t *testing.T) {
	for _, override := range []float64{0, 0.07, 1e-7} {
		cfg := &config.Config{Grid: config.GridConfig{HexEdgeM: 200}}
		cfg.Forecast.DecayRate = override
		entry := roadEntry(t, cfg, "Board City", "board-city", exportBoundaryA)
		fc, err := BuildHexGeoJSON(t.Context(), entry, projectorFor(t, entry))
		if err != nil || fc == nil {
			t.Fatalf("override %g: BuildHexGeoJSON = (%v, %v)", override, fc, err)
		}
		// fixtureArterialK0 is primary's default rate and fixtureLocalK0
		// residential's, so each fixture hex is a one-class blend.
		want := map[float64]float64{
			fixtureArterialK0: roundSig(forecast.ClassDecayRate("primary", override), 4),
			fixtureLocalK0:    roundSig(forecast.ClassDecayRate(forecast.ClassResidential, override), 4),
		}
		if forecast.DecayRateForClass("primary") != fixtureArterialK0 || forecast.DecayRateForClass(forecast.ClassResidential) != fixtureLocalK0 {
			t.Fatal("fixture k0 values no longer match the class defaults they stand for")
		}
		stored := map[string]float64{}
		for _, r := range entry.Store.(*dbtest.MockStore).HexMetrics {
			if r.Metric == resource.MetricDefaultDecayRate {
				stored[r.HexID] = r.Value
			}
		}
		var withK, withoutK int
		for _, f := range fc["features"].([]map[string]any) {
			props := f["properties"].(map[string]any)
			id := props["id"].(string)
			k, hasK := props["k"]
			k0, isRoad := stored[id]
			switch {
			case isRoad && !hasK:
				t.Errorf("override %g: road hex %s has no k", override, id)
			case !isRoad && hasK:
				t.Errorf("override %g: hex %s with no city roads carries k = %v", override, id, k)
			case isRoad:
				withK++
				if k != want[k0] {
					t.Errorf("override %g: hex %s k = %v, want %v (k0 %v)", override, id, k, want[k0], k0)
				}
			default:
				withoutK++
			}
		}
		if withK == 0 || withoutK == 0 {
			t.Fatalf("override %g: %d hexes with k, %d without; the fixture must hold both", override, withK, withoutK)
		}
	}
}

// projectorFor builds the UTM projector for a fixture entry the same way the
// production export path does, so grid ids line up with the stored rows.
func projectorFor(t *testing.T, entry CityEntry) *geo.UTMProjector {
	t.Helper()
	_, lon0, lat0, err := entry.BBoxAndCenter(context.Background())
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	return geo.NewUTMProjector(lon0, lat0)
}

// TestBuildHexGeoJSON_NoStatsBuildsNoGrid: a city with no hex stats returns
// before it needs a grid, so it pays for no lattice+clip and a grid error
// cannot abort its export. The grid thunk is poisoned: forcing it fails.
func TestBuildHexGeoJSON_NoStatsBuildsNoGrid(t *testing.T) {
	entry := hexEntry(t, nil, nil)
	poison := drawnHexesFunc(func() (*drawnHexes, error) {
		t.Error("hex grid was built for a city with no hex stats")
		return nil, errors.New("grid must not be built")
	})
	got, err := buildHexGeoJSONFromGrid(t.Context(), entry, poison)
	if err != nil || got != nil {
		t.Errorf("buildHexGeoJSONFromGrid = (%v, %v), want (nil, nil)", got, err)
	}
}

// encodedHexFeature is buildHexFeature for hexMap's hex hexID, encoded as
// DrawnGrid encodes it; ok is false when the hex is missing or cannot be
// encoded, so DrawnGrid would not draw it.
//
//nolint:unparam // hexID keeps buildHexFeature's shape, though every fixture names one hex
func encodedHexFeature(hexID string, agg *hexAgg, hexMap map[string]*geo.Hex, proj *geo.UTMProjector, decimals int) (map[string]any, bool) {
	h, ok := hexMap[hexID]
	if !ok {
		return nil, false
	}
	gjson, ok := hexGeometryJSON(h, proj, decimals)
	if !ok {
		return nil, false
	}
	return buildHexFeature(hexID, agg, gjson), true
}
