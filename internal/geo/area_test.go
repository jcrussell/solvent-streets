package geo

import (
	"math"
	"testing"
)

func TestBoundaryAreaSqFt(t *testing.T) {
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

	sqft, err := BoundaryAreaSqFt(gjson)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1 sq km ≈ 10,763,910 sq ft. Allow 15% tolerance for the approximate degree sizes.
	expectedSqFt := 10763910.0
	ratio := sqft / expectedSqFt
	if math.Abs(ratio-1.0) > 0.15 {
		t.Errorf("area = %.0f sq ft, expected ~%.0f sq ft (ratio %.3f)", sqft, expectedSqFt, ratio)
	}
}

func TestBoundaryAreaSqFt_InvalidGeoJSON(t *testing.T) {
	_, err := BoundaryAreaSqFt("not json")
	if err == nil {
		t.Error("expected error for invalid GeoJSON")
	}
}

func TestBoundaryAreaSqFt_EmptyPolygon(t *testing.T) {
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

	sqft, err := BoundaryAreaSqFt(gjson)
	if err != nil {
		// Some implementations may error on degenerate polygons, that's fine
		return
	}
	if sqft > 1 {
		t.Errorf("expected ~0 area for degenerate polygon, got %.0f", sqft)
	}
}
