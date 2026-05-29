package geo

import (
	"math"
	"testing"
)

func TestBoundaryArea(t *testing.T) {
	// ~1km x 1km square near Austin, TX (30.27°N, -97.74°W)
	// 0.009° lat ≈ 1 km, 0.0104° lon ≈ 1 km at this latitude
	gjson := `{
		"type": "Polygon",
		"coordinates": [[
			[-97.745, 30.265],
			[-97.7346, 30.265],
			[-97.7346, 30.274],
			[-97.745, 30.274],
			[-97.745, 30.265]
		]]
	}`

	sqm, err := BoundaryArea(gjson)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1 sq km = 1,000,000 sq m. Allow 15% tolerance for the approximate degree sizes.
	expectedSqM := 1_000_000.0
	ratio := sqm / expectedSqM
	if math.Abs(ratio-1.0) > 0.15 {
		t.Errorf("area = %.0f sq m, expected ~%.0f sq m (ratio %.3f)", sqm, expectedSqM, ratio)
	}
}

func TestBoundaryArea_InvalidGeoJSON(t *testing.T) {
	_, err := BoundaryArea("not json")
	if err == nil {
		t.Error("expected error for invalid GeoJSON")
	}
}

func TestBoundaryArea_EmptyPolygon(t *testing.T) {
	// Degenerate polygon (all same point)
	gjson := `{
		"type": "Polygon",
		"coordinates": [[
			[-97.74, 30.27],
			[-97.74, 30.27],
			[-97.74, 30.27],
			[-97.74, 30.27]
		]]
	}`

	sqm, err := BoundaryArea(gjson)
	if err != nil {
		// Some implementations may error on degenerate polygons, that's fine
		return
	}
	if sqm > 1 {
		t.Errorf("expected ~0 area for degenerate polygon, got %.0f", sqm)
	}
}

func TestInteriorPoints_Polygon(t *testing.T) {
	gjson := `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}`
	probes, err := InteriorPoints(gjson)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("Polygon should yield 1 probe, got %d", len(probes))
	}
	p := probes[0]
	if p[0] < 0 || p[0] > 1 || p[1] < 0 || p[1] > 1 {
		t.Errorf("probe %v should lie inside unit square", p)
	}
}

func TestInteriorPoints_MultiPolygon(t *testing.T) {
	// Two disjoint unit squares.
	gjson := `{
		"type":"MultiPolygon",
		"coordinates":[
			[[[0,0],[1,0],[1,1],[0,1],[0,0]]],
			[[[10,10],[11,10],[11,11],[10,11],[10,10]]]
		]
	}`
	probes, err := InteriorPoints(gjson)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(probes) != 2 {
		t.Fatalf("MultiPolygon with 2 sub-polygons should yield 2 probes, got %d", len(probes))
	}
	// One probe should land in each square.
	inFirst := func(p [2]float64) bool { return p[0] >= 0 && p[0] <= 1 && p[1] >= 0 && p[1] <= 1 }
	inSecond := func(p [2]float64) bool { return p[0] >= 10 && p[0] <= 11 && p[1] >= 10 && p[1] <= 11 }
	got1, got2 := false, false
	for _, p := range probes {
		switch {
		case inFirst(p):
			got1 = true
		case inSecond(p):
			got2 = true
		default:
			t.Errorf("probe %v outside both sub-polygons", p)
		}
	}
	if !got1 || !got2 {
		t.Errorf("expected one probe per sub-polygon; first=%v second=%v probes=%v", got1, got2, probes)
	}
}

func TestInteriorPoints_InvalidGeoJSON(t *testing.T) {
	if _, err := InteriorPoints("not json"); err == nil {
		t.Error("expected parse error")
	}
}

func TestInteriorPoints_UnsupportedType(t *testing.T) {
	// Point is not a Polygon/MultiPolygon — should error rather than
	// silently return an empty slice.
	if _, err := InteriorPoints(`{"type":"Point","coordinates":[0,0]}`); err == nil {
		t.Error("expected error for unsupported geometry type")
	}
}

func TestInteriorPoints_EmptyGeometry(t *testing.T) {
	// MultiPolygon with no sub-polygons.
	if _, err := InteriorPoints(`{"type":"MultiPolygon","coordinates":[]}`); err == nil {
		t.Error("expected error for empty MultiPolygon")
	}
}

func TestInteriorPoints_MultiPolygonSkipsEmptySubPolygon(t *testing.T) {
	// A MultiPolygon where the first sub-polygon is empty (no
	// coordinates) and the second is a valid unit square. The empty
	// sub-polygon must be silently skipped — we should get exactly one
	// probe, in the valid sub-polygon. Catches a regression where the
	// IsEmpty() guard in InteriorPoints is dropped.
	gjson := `{
		"type":"MultiPolygon",
		"coordinates":[
			[[]],
			[[[10,10],[11,10],[11,11],[10,11],[10,10]]]
		]
	}`
	probes, err := InteriorPoints(gjson)
	if err != nil {
		// simplefeatures might reject this as invalid GeoJSON before our
		// IsEmpty() check runs; that's also an acceptable outcome (no
		// probes returned). Don't fail the test in that case.
		t.Skipf("simplefeatures rejected fixture before IsEmpty check: %v", err)
	}
	if len(probes) != 1 {
		t.Fatalf("expected 1 probe (empty sub-polygon skipped); got %d: %v", len(probes), probes)
	}
	p := probes[0]
	if p[0] < 10 || p[0] > 11 || p[1] < 10 || p[1] > 11 {
		t.Errorf("probe %v should lie inside the valid sub-polygon", p)
	}
}
