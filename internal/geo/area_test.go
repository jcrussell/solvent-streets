package geo

import (
	"math"
	"testing"
)

func TestBoundaryAreaSqM(t *testing.T) {
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

	sqm, err := BoundaryAreaSqM(gjson)
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

func TestBoundaryAreaSqM_InvalidGeoJSON(t *testing.T) {
	_, err := BoundaryAreaSqM("not json")
	if err == nil {
		t.Error("expected error for invalid GeoJSON")
	}
}

func TestBoundaryAreaSqM_EmptyPolygon(t *testing.T) {
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

	sqm, err := BoundaryAreaSqM(gjson)
	if err != nil {
		// Some implementations may error on degenerate polygons, that's fine
		return
	}
	if sqm > 1 {
		t.Errorf("expected ~0 area for degenerate polygon, got %.0f", sqm)
	}
}
