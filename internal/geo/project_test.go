package geo

import (
	"math"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	// Test that ToStatePlane -> ToWGS84 round-trips correctly for Livermore
	lon, lat := -121.76, 37.68

	x, y := ToStatePlane(lon, lat)

	// Verify we get reasonable state plane coordinates (should be in millions range for x)
	if x < 5000000 || x > 8000000 {
		t.Errorf("x=%f outside expected range for CA Zone 3", x)
	}
	if y < 1000000 || y > 3000000 {
		t.Errorf("y=%f outside expected range for CA Zone 3", y)
	}

	lon2, lat2 := ToWGS84(x, y)

	if math.Abs(lon2-lon) > 0.0001 {
		t.Errorf("lon round-trip: got %f, want %f", lon2, lon)
	}
	if math.Abs(lat2-lat) > 0.0001 {
		t.Errorf("lat round-trip: got %f, want %f", lat2, lat)
	}
}

func TestDistanceInFeet(t *testing.T) {
	// Two points about 1 mile apart east-west in Livermore
	// 1 degree of longitude at 37.68N is approximately 54.8 miles
	lon1, lat := -121.76, 37.68
	lon2 := -121.74 // ~0.02 degrees east

	x1, y1 := ToStatePlane(lon1, lat)
	x2, y2 := ToStatePlane(lon2, lat)

	dx := x2 - x1
	dy := y2 - y1
	dist := math.Sqrt(dx*dx + dy*dy) // in US survey feet

	// 0.02 degrees at this latitude ~ 1.096 miles ~ 5787 feet
	if dist < 5000 || dist > 6500 {
		t.Errorf("expected ~5787 feet, got %f", dist)
	}
}
