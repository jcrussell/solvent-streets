package geo

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/peterstace/simplefeatures/geom"
)

const geomTypeLineString = "LineString"

func TestBufferLineString(t *testing.T) {
	// A simple horizontal line, 100 feet long, buffered by 10 feet (width=20)
	// Expected area: 100 * 20 = 2000 sq ft (flat end caps, no round ends)
	coords := [][2]float64{
		{0, 0},
		{100, 0},
	}
	g, err := BufferLineString(coords, 20)
	if err != nil {
		t.Fatal(err)
	}
	area := g.Area()
	if math.Abs(area-2000) > 10 { // tolerance of 10 sq ft
		t.Errorf("expected area ~2000, got %f", area)
	}
}

func TestBufferLineStringLShape(t *testing.T) {
	// L-shaped line: 100ft right then 100ft up, width 10ft
	// Should be slightly less than 2 * 100 * 10 = 2000 due to overlap at corner
	coords := [][2]float64{
		{0, 0},
		{100, 0},
		{100, 100},
	}
	g, err := BufferLineString(coords, 10)
	if err != nil {
		t.Fatal(err)
	}
	area := g.Area()
	if area < 1900 || area > 2100 {
		t.Errorf("expected area ~1975, got %f", area)
	}
}

func TestUnionAllRemovesOverlap(t *testing.T) {
	// Two overlapping rectangles
	r1 := makeRect(0, 0, 10, 10)
	r2 := makeRect(5, 0, 15, 10)

	union, err := UnionAll([]geom.Geometry{r1, r2})
	if err != nil {
		t.Fatal(err)
	}
	area := union.Area()
	// Each rect is 100, overlap is 50, so union should be 150
	if math.Abs(area-150) > 1 {
		t.Errorf("expected area ~150, got %f", area)
	}
}

func TestGeometryToGeoJSON_RoundTrip(t *testing.T) {
	// Create a projected geometry (in UTM meters) and convert to GeoJSON
	// Use UTM zone 10N coords (typical for western US)
	proj := NewUTMProjector(-121.76, 37.68)
	rect := makeRect(600000, 4170000, 601000, 4171000) // UTM coords
	gjson, err := GeometryToGeoJSON(rect, proj)
	if err != nil {
		t.Fatal(err)
	}
	if gjson == "" {
		t.Fatal("expected non-empty GeoJSON")
	}
	// Parse and verify coordinates are in WGS84 range
	coords, _, err := ParseGeoJSONCoords(gjson)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range coords {
		if c[0] < -180 || c[0] > 180 {
			t.Errorf("longitude out of range: %f", c[0])
		}
		if c[1] < -90 || c[1] > 90 {
			t.Errorf("latitude out of range: %f", c[1])
		}
	}
}

// TestGeometryToGeoJSONWithPrecision_RoundsCoords pins the precision
// contract: coordinates in the emitted GeoJSON are rounded to the
// requested decimal places. Asserts behavior (the decimal-string shape
// of the output), not internal call counts. Regression caught: hardcoding
// 7 inside tryReprojectCoord (the bug this knob was introduced to fix).
func TestGeometryToGeoJSONWithPrecision_RoundsCoords(t *testing.T) {
	proj := NewUTMProjector(-121.76, 37.68)
	rect := makeRect(600000, 4170000, 601000, 4171000)

	cases := []struct {
		name     string
		decimals int
		maxFrac  int // max # of digits expected after the decimal point
	}{
		{"6_decimals", 6, 6},
		{"5_decimals", 5, 5},
		{"3_decimals", 3, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gjson, err := GeometryToGeoJSONWithPrecision(rect, proj, tc.decimals)
			if err != nil {
				t.Fatal(err)
			}
			coords, _, err := ParseGeoJSONCoords(gjson)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range coords {
				for axis, v := range []float64{c[0], c[1]} {
					if frac := fractionalDigits(v); frac > tc.maxFrac {
						t.Errorf("coord[%d]=%v has %d fractional digits; want ≤ %d",
							axis, v, frac, tc.maxFrac)
					}
				}
			}
		})
	}
}

// fractionalDigits returns the count of significant digits after the
// decimal point for v formatted at minimum precision. Trailing zeros
// produced by binary float roundoff are not counted as significant.
func fractionalDigits(v float64) int {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return 0
	}
	return len(s) - dot - 1
}

func TestGeoJSONToProjectedGeometry_LineString(t *testing.T) {
	proj := NewUTMProjector(-121.76, 37.68)
	gjson := `{"type":"LineString","coordinates":[[-121.77,37.68],[-121.76,37.69]]}`
	g, gtype, err := GeoJSONToProjectedGeometry(gjson, proj)
	if err != nil {
		t.Fatal(err)
	}
	if gtype != geomTypeLineString {
		t.Errorf("expected LineString, got %s", gtype)
	}
	if g.IsEmpty() {
		t.Error("expected non-empty geometry")
	}
}

func TestGeoJSONToProjectedGeometry_MultiLineString(t *testing.T) {
	proj := NewUTMProjector(-121.76, 37.68)
	// Two disjoint parts — must stay separate, not be concatenated.
	gjson := `{"type":"MultiLineString","coordinates":[` +
		`[[-121.77,37.68],[-121.76,37.69]],` +
		`[[-121.74,37.66],[-121.73,37.67]]]}`
	g, gtype, err := GeoJSONToProjectedGeometry(gjson, proj)
	if err != nil {
		t.Fatal(err)
	}
	if gtype != "MultiLineString" {
		t.Errorf("expected MultiLineString, got %s", gtype)
	}
	mls, ok := g.AsMultiLineString()
	if !ok {
		t.Fatalf("expected a MultiLineString geometry, got %s", g.Type())
	}
	if mls.NumLineStrings() != 2 {
		t.Errorf("expected 2 parts (no concatenation), got %d", mls.NumLineStrings())
	}
}

func TestGeoJSONToProjectedGeometry_MultiLineString_AllPartsInvalid(t *testing.T) {
	proj := NewUTMProjector(-121.76, 37.68)
	// Every part has <2 points: no valid linestrings -> error, not empty geom.
	gjson := `{"type":"MultiLineString","coordinates":[[[-121.77,37.68]],[[-121.74,37.66]]]}`
	if _, _, err := GeoJSONToProjectedGeometry(gjson, proj); err == nil {
		t.Error("expected error when all MultiLineString parts are invalid")
	}
}

func TestGeoJSONToProjectedGeometry_Polygon(t *testing.T) {
	proj := NewUTMProjector(-121.76, 37.68)
	gjson := `{"type":"Polygon","coordinates":[[[-121.77,37.68],[-121.76,37.68],[-121.76,37.69],[-121.77,37.69],[-121.77,37.68]]]}`
	g, gtype, err := GeoJSONToProjectedGeometry(gjson, proj)
	if err != nil {
		t.Fatal(err)
	}
	if gtype != geomTypePolygon {
		t.Errorf("expected Polygon, got %s", gtype)
	}
	if g.Area() <= 0 {
		t.Error("expected positive area for polygon")
	}
}

func TestUnionAll_SingleGeometry(t *testing.T) {
	rect := makeRect(0, 0, 10, 10)
	result, err := UnionAll([]geom.Geometry{rect})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.Area()-100) > 1 {
		t.Errorf("expected area ~100, got %f", result.Area())
	}
}

func makeRect(x1, y1, x2, y2 float64) geom.Geometry {
	coords := []float64{x1, y1, x2, y1, x2, y2, x1, y2, x1, y1}
	seq := geom.NewSequence(coords, geom.DimXY)
	ring := geom.NewLineString(seq)
	poly := geom.NewPolygon([]geom.LineString{ring})
	return poly.AsGeometry()
}

func mustWKT(t *testing.T, wkt string) geom.Geometry {
	t.Helper()
	g, err := geom.UnmarshalWKT(wkt)
	if err != nil {
		t.Fatalf("UnmarshalWKT(%q): %v", wkt, err)
	}
	return g
}

// TestRetainPolygonal exercises the helper directly: drop lower-dim
// children of a GeometryCollection, return polygonal-only input
// unchanged, and yield an empty Geometry for purely lower-dim input.
func TestRetainPolygonal(t *testing.T) {
	t.Run("mixed_dim_GC_keeps_polygon", func(t *testing.T) {
		gc := mustWKT(t, "GEOMETRYCOLLECTION(POLYGON((0 0,4 0,4 4,0 4,0 0)),LINESTRING(5 5,6 6),POINT(7 7))")
		got, err := RetainPolygonal(gc)
		if err != nil {
			t.Fatalf("RetainPolygonal: %v", err)
		}
		if !got.IsPolygon() && !got.IsMultiPolygon() {
			t.Fatalf("expected polygonal result, got %s", got.Type())
		}
		if math.Abs(got.Area()-16) > 1e-9 {
			t.Errorf("expected area 16, got %f", got.Area())
		}
	})

	t.Run("nested_GC_flattens", func(t *testing.T) {
		gc := mustWKT(t, "GEOMETRYCOLLECTION(GEOMETRYCOLLECTION(POLYGON((0 0,2 0,2 2,0 2,0 0))),LINESTRING(3 3,4 4))")
		got, err := RetainPolygonal(gc)
		if err != nil {
			t.Fatalf("RetainPolygonal: %v", err)
		}
		if !got.IsPolygon() && !got.IsMultiPolygon() {
			t.Fatalf("expected polygonal result, got %s", got.Type())
		}
		if math.Abs(got.Area()-4) > 1e-9 {
			t.Errorf("expected area 4, got %f", got.Area())
		}
	})

	t.Run("pure_linestring_empty", func(t *testing.T) {
		ls := mustWKT(t, "LINESTRING(0 0,1 1,2 2)")
		got, err := RetainPolygonal(ls)
		if err != nil {
			t.Fatalf("RetainPolygonal: %v", err)
		}
		if !got.IsEmpty() {
			t.Errorf("expected empty, got %s with area %f", got.Type(), got.Area())
		}
	})

	t.Run("pure_polygon_unchanged", func(t *testing.T) {
		p := mustWKT(t, "POLYGON((0 0,3 0,3 3,0 3,0 0))")
		got, err := RetainPolygonal(p)
		if err != nil {
			t.Fatalf("RetainPolygonal: %v", err)
		}
		if !got.IsPolygon() {
			t.Errorf("expected Polygon, got %s", got.Type())
		}
		if math.Abs(got.Area()-9) > 1e-9 {
			t.Errorf("expected area 9, got %f", got.Area())
		}
	})
}

// TestRetainPolygonal_NormalizesMixedDimIntersectionResult pins the
// transform RetainPolygonal must do for the solvent-streets-i3ih fix:
// Intersection of two polygons that share a boundary segment outside
// their 2-D overlap can return a mixed-dimension GeometryCollection
// (a polygon for the overlap plus a LineString for the shared edge —
// documented behavior of JTS OverlayNG default mode). The helper
// must extract the polygonal part and discard the 1-D shared-edge
// artifact, so the result is safe to hand back to the binary
// overlay operations.
//
// This uses the polygon pair verified to produce mixed-dim
// Intersection by simplefeatures' own test suite (alg_overlay_test.go
// near line 1216). We do NOT assert that pre-fix
// `geom.Difference(polyA, inter)` panics — for simple synthetic
// inputs the first FLOAT-noder attempt succeeds and the snap-retry
// path (where the `addGeometryCollection` mixed-dim check actually
// lives) is not exercised. The production failure for Austin TX
// only fires through snap retry on complex high-precision OSM
// coordinates; the synthetic case can't reproduce that triggering
// condition, but the FIX is the same either way: strip the 1-D
// parts before any subsequent overlay.
func TestRetainPolygonal_NormalizesMixedDimIntersectionResult(t *testing.T) {
	polyA := mustWKT(t, "POLYGON((0 0,0 2,2 2,1 1,1 0,0 0))")
	polyB := mustWKT(t, "POLYGON((1 0,1 1,0 2,2 2,2 0,1 0))")

	inter, err := geom.Intersection(polyA, polyB)
	if err != nil {
		t.Fatalf("Intersection: %v", err)
	}
	if !inter.IsGeometryCollection() {
		t.Fatalf("expected GeometryCollection from Intersection (upstream-verified shape); got %s — simplefeatures upstream behavior changed and this test no longer covers the bug class", inter.Type())
	}
	gc, _ := inter.AsGeometryCollection()
	sawLine := false
	for i := range gc.NumGeometries() {
		if gc.GeometryN(i).Dimension() == 1 {
			sawLine = true
		}
	}
	if !sawLine {
		t.Fatalf("expected mixed-dim GC (a 1-D LineString from the shared boundary edge); got %s", inter.AsText())
	}

	cleaned, err := RetainPolygonal(inter)
	if err != nil {
		t.Fatalf("RetainPolygonal: %v", err)
	}
	if !cleaned.IsPolygon() && !cleaned.IsMultiPolygon() {
		t.Fatalf("expected polygonal result, got %s with WKT %s", cleaned.Type(), cleaned.AsText())
	}
	if _, err := geom.Difference(polyA, cleaned); err != nil {
		t.Fatalf("Difference(polyA, cleaned): %v — Difference should accept polygonal input", err)
	}
}
