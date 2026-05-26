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
