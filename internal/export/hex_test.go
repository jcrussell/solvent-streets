package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/peterstace/simplefeatures/geom"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
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

// hexEntry builds a CityEntry whose ListHexStats returns rows from the given
// map (keyed by full resource label, e.g. roads or roads:city).
// LatestComputeResult is similarly keyed.
func hexEntry(t *testing.T, hexStats map[resource.Type][]db.HexStat, results map[resource.Type]db.ComputeResult) CityEntry {
	t.Helper()
	store := &dbtest.MockStore{
		ListHexStatsFunc: func(_ context.Context, rt resource.Type) ([]db.HexStat, error) {
			return hexStats[rt], nil
		},
		LatestComputeResultFunc: func(_ context.Context, rt resource.Type) (*db.ComputeResult, error) {
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

// TestBuildHexGeoJSON_EmittedWhenRowsExist: a single FeatureCollection is
// returned when any hex_stats rows exist (across either scope), and nil only
// when there are no rows at all. The per-feature {bbox, city?} shape is pinned
// deterministically in TestBuildHexFeature_NestedScopes.
func TestBuildHexGeoJSON_EmittedWhenRowsExist(t *testing.T) {
	now := time.Now()
	cityRows := []db.HexStat{{HexID: "0,0", ResourceType: rtRoadsCity, Area: 100, PctCovered: 50, ComputedAt: now}}
	bboxRows := []db.HexStat{{HexID: "0,0", ResourceType: rtRoads, Area: 200, PctCovered: 75, ComputedAt: now}}
	entry := hexEntry(t, map[resource.Type][]db.HexStat{
		rtRoads:       bboxRows,
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
		for _, scope := range []string{"bbox", "city"} {
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

// TestBuildHexFeature_NestedScopes pins the per-feature shape: "id" plus nested
// {bbox, city?} objects keyed by bare resource name -> {"area", "pct"}. "city"
// is present only when the hex has city-scope coverage.
func TestBuildHexFeature_NestedScopes(t *testing.T) {
	proj := geo.NewUTMProjector(-122.45, 37.55)
	h := offsetSquareHex(t, "0,0", 550000, 4156000, 50)
	hexMap := map[string]*geo.Hex{"0,0": &h}

	// Hex with both scopes.
	agg := &hexAgg{
		bbox: map[string]map[string]float64{"roads": {"area": 200, "pct": 75}},
		city: map[string]map[string]float64{"roads": {"area": 100, "pct": 50}},
	}
	feat, ok := buildHexFeature("0,0", agg, hexMap, proj, 6)
	if !ok {
		t.Fatal("buildHexFeature returned ok=false")
	}
	props := feat["properties"].(map[string]any)
	if props["id"] != "0,0" {
		t.Errorf("id = %v; want 0,0", props["id"])
	}
	bbox := props["bbox"].(map[string]map[string]float64)
	if bbox["roads"]["area"] != 200 || bbox["roads"]["pct"] != 75 {
		t.Errorf("bbox.roads = %v; want {area:200, pct:75}", bbox["roads"])
	}
	city := props["city"].(map[string]map[string]float64)
	if city["roads"]["area"] != 100 || city["roads"]["pct"] != 50 {
		t.Errorf("city.roads = %v; want {area:100, pct:50}", city["roads"])
	}

	// Hex with no city coverage omits the "city" key entirely.
	bboxOnly := &hexAgg{bbox: map[string]map[string]float64{"roads": {"area": 200, "pct": 75}}}
	feat2, _ := buildHexFeature("0,0", bboxOnly, hexMap, proj, 6)
	if _, ok := feat2["properties"].(map[string]any)["city"]; ok {
		t.Error("city key must be absent when the hex has no city coverage")
	}
}

// TestBuildHexFeature_RespectsCoordinatePrecision pins the wiring from
// the decimals parameter down through buildHexFeature → the
// precision-aware geo helper. Exercises buildHexFeature directly with a
// known hex (no dependency on grid-generation ID matching) and asserts
// the lower-precision pass emits fewer significant fractional digits.
//
// Regression caught: dropping the decimals plumb-through (forgetting to
// thread the value into buildHexFeature, or calling the legacy
// GeometryToGeoJSON inadvertently).
func TestBuildHexFeature_RespectsCoordinatePrecision(t *testing.T) {
	proj := geo.NewUTMProjector(-122.45, 37.55)
	// Place the hex at realistic UTM coords (zone 10N, ~SF Bay) — values
	// well outside the [-180..180, -90..90] lon/lat window so the
	// reprojector actually runs (it's a no-op on already-lon/lat coords).
	h := offsetSquareHex(t, "0,0", 550000, 4156000, 50)
	hexMap := map[string]*geo.Hex{"0,0": &h}
	agg := &hexAgg{bbox: map[string]map[string]float64{"roads": {"area": 200, "pct": 75}}}

	maxFrac := func(decimals int) int {
		feat, ok := buildHexFeature("0,0", agg, hexMap, proj, decimals)
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

// TestBuildHexGeoJSON_NoCityRowsOmitsCity: legacy cities without ":city"-suffixed
// hex_stats rows still yield a FeatureCollection, but no feature carries a "city"
// object. The client uses that absence as the "hide the scope toggle" signal.
func TestBuildHexGeoJSON_NoCityRowsOmitsCity(t *testing.T) {
	bboxRows := []db.HexStat{{HexID: "0,0", ResourceType: rtRoads, Area: 100, PctCovered: 50}}
	entry := hexEntry(t, map[resource.Type][]db.HexStat{
		rtRoads:       bboxRows,
		rtParkingAll:  nil,
		rtSidewalkAll: nil,
	}, nil)

	_, lon, lat, _ := entry.BBoxAndCenter(t.Context())
	fc, err := BuildHexGeoJSON(t.Context(), entry, geo.NewUTMProjector(lon, lat))
	if err != nil {
		t.Fatalf("BuildHexGeoJSON: %v", err)
	}
	if fc == nil {
		t.Fatal("FC must be non-nil when bbox rows exist")
	}
	feats, _ := fc["features"].([]map[string]any)
	for _, f := range feats {
		if _, ok := f["properties"].(map[string]any)["city"]; ok {
			t.Error("no feature may carry a city object when no :city rows exist")
		}
	}
}

// TestBuildHexCostSummary_NestedByScope: the legacy flat shape
// (map[resource]{...}) is replaced by map[scope]map[resource]{...}. Both
// "city" and "bbox" keys appear when BboxBaseline is set on the forecast
// export (the marker that compute produced both scopes).
func TestBuildHexCostSummary_NestedByScope(t *testing.T) {
	now := time.Now()
	results := map[resource.Type]db.ComputeResult{
		rtRoads:     {ResourceType: rtRoads, TotalArea: 2000, ComputedAt: now},
		rtRoadsCity: {ResourceType: rtRoadsCity, TotalArea: 1000, ComputedAt: now},
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
	if city["roads"]["total_area"] != 1000 {
		t.Errorf("city roads total_area = %v; want 1000 (city-scope row)", city["roads"]["total_area"])
	}
	if bbox["roads"]["year1_cost"] != 50000 {
		t.Errorf("bbox roads year1_cost = %v; want 50000 (from BboxBaseline)", bbox["roads"]["year1_cost"])
	}
	if bbox["roads"]["total_area"] != 2000 {
		t.Errorf("bbox roads total_area = %v; want 2000 (bbox-scope row)", bbox["roads"]["total_area"])
	}
}

// TestBuildHexCostSummary_BboxOnlyWhenNoCity: when BboxBaseline is nil
// (legacy single-scope compute), the output has only the "bbox" key and
// Baseline carries the bbox numbers.
func TestBuildHexCostSummary_BboxOnlyWhenNoCity(t *testing.T) {
	results := map[resource.Type]db.ComputeResult{
		rtRoads: {ResourceType: rtRoads, TotalArea: 2000},
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
	results := map[resource.Type]db.ComputeResult{
		rtRoads:     {ResourceType: rtRoads, TotalArea: 2000, FeatureCount: 100},
		rtRoadsCity: {ResourceType: rtRoadsCity, TotalArea: 1000, FeatureCount: 60},
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
	results := map[resource.Type]db.ComputeResult{
		rtRoads: {ResourceType: rtRoads, TotalArea: 2000, FeatureCount: 100},
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

// projectedLonLatSquare builds a hex whose Geom is the lon/lat square
// [(lon0,lat0),(lon1,lat1)] projected into proj's coordinate system — i.e. in
// the same projected space clipHexGridToBoundary operates in. Used to place
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

// TestClipHexGridToBoundary_MatchesUnconditionalOverlay pins the prepared-
// geometry fast path against the original behavior: preparing the boundary once
// and using Intersects/Covers to skip the overlay for outside/interior hexes
// must produce exactly the same kept set and the same clipped areas as calling
// geom.Intersection unconditionally on every hex.
//
// The boundary is a square with a hole, and the hexes deliberately hit every
// branch: fully interior (Covers→keep as-is), perimeter-straddling (overlay),
// fully outside (Intersects=false→drop), straddling the hole (overlay clips the
// hole out), and fully inside the hole (disjoint→drop).
//
// Regression caught: a wrong predicate (e.g. Contains instead of Covers
// dropping edge-touching hexes), or the keep-shortcut emitting an unclipped
// hex where the hole/boundary should have trimmed it.
func TestClipHexGridToBoundary_MatchesUnconditionalOverlay(t *testing.T) {
	proj := geo.NewUTMProjector(-122.45, 37.55)
	// Square boundary (-122.5,37.5)-(-122.4,37.6) with a hole
	// (-122.47,37.53)-(-122.43,37.57).
	const boundaryWithHole = `{"type":"Polygon","coordinates":[` +
		`[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]],` +
		`[[-122.47,37.53],[-122.43,37.53],[-122.43,37.57],[-122.47,37.57],[-122.47,37.53]]]}`
	entry := CityEntry{
		Config: &config.Config{Grid: config.GridConfig{HexEdgeM: 200}},
		City:   config.CityConfig{Name: "Test City"},
		Slug:   "test-city",
		Store: &dbtest.MockStore{
			GetBoundaryFunc: func(_ context.Context) (string, error) { return boundaryWithHole, nil },
		},
	}

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

	got := clipHexGridToBoundary(t.Context(), hexes, entry, proj)
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
