package geo

import (
	"math"
	"strings"
	"testing"
)

func TestSubtractGeoJSON_RemovesInnerPolygon(t *testing.T) {
	// Square from (-122.0, 37.7) to (-121.9, 37.8) (~10km on a side).
	outer := `{"type":"Polygon","coordinates":[[[-122.0,37.7],[-121.9,37.7],[-121.9,37.8],[-122.0,37.8],[-122.0,37.7]]]}`
	// Inner square covering the western half.
	inner := `{"type":"Polygon","coordinates":[[[-122.0,37.7],[-121.95,37.7],[-121.95,37.8],[-122.0,37.8],[-122.0,37.7]]]}`

	outerArea, err := BoundaryAreaSqM(outer)
	if err != nil {
		t.Fatalf("outer area: %v", err)
	}

	result, err := SubtractGeoJSON(outer, inner)
	if err != nil {
		t.Fatalf("SubtractGeoJSON: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	resultArea, err := BoundaryAreaSqM(result)
	if err != nil {
		t.Fatalf("result area: %v", err)
	}

	// Result should be roughly half the outer area.
	want := outerArea / 2
	if math.Abs(resultArea-want)/want > 0.01 {
		t.Errorf("result area = %.0f, want ~%.0f (half of %.0f)", resultArea, want, outerArea)
	}
}

func TestSubtractGeoJSON_EmptySubtrahendReturnsBoundary(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-122.0,37.7],[-121.9,37.7],[-121.9,37.8],[-122.0,37.8],[-122.0,37.7]]]}`
	result, err := SubtractGeoJSON(boundary, "")
	if err != nil {
		t.Fatalf("SubtractGeoJSON: %v", err)
	}
	if result != boundary {
		t.Errorf("expected pass-through when subtrahend is empty; got %q", result)
	}
}

func TestSubtractGeoJSON_EmptyBoundaryReturnsError(t *testing.T) {
	_, err := SubtractGeoJSON("", `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}`)
	if err == nil {
		t.Fatal("expected error for empty boundary")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSubtractGeoJSON_DisjointSubtrahendUnchanged(t *testing.T) {
	// Boundary near San Francisco.
	boundary := `{"type":"Polygon","coordinates":[[[-122.5,37.7],[-122.4,37.7],[-122.4,37.8],[-122.5,37.8],[-122.5,37.7]]]}`
	// Subtrahend nearby but not overlapping (slight offset east).
	disjoint := `{"type":"Polygon","coordinates":[[[-122.39,37.7],[-122.38,37.7],[-122.38,37.8],[-122.39,37.8],[-122.39,37.7]]]}`

	boundaryArea, err := BoundaryAreaSqM(boundary)
	if err != nil {
		t.Fatalf("boundary area: %v", err)
	}

	result, err := SubtractGeoJSON(boundary, disjoint)
	if err != nil {
		t.Fatalf("SubtractGeoJSON: %v", err)
	}

	resultArea, err := BoundaryAreaSqM(result)
	if err != nil {
		t.Fatalf("result area: %v", err)
	}

	// Disjoint subtrahend should leave area essentially unchanged (a tiny
	// reprojection-roundtrip delta is acceptable).
	if math.Abs(resultArea-boundaryArea)/boundaryArea > 0.001 {
		t.Errorf("result area = %.0f, want ~%.0f (no change for disjoint subtract)", resultArea, boundaryArea)
	}
}

// TestSubtractGeoJSON_OversizedSubtrahendClippedToBoundary pins the
// defense layer: a subtrahend extending FAR beyond the boundary on all
// sides (a malformed/over-sized water polygon) still produces the
// correct difference — area outside the boundary is irrelevant to
// what's subtracted. Without the Intersection step this still worked
// mathematically (Difference is set-theoretically correct), but the
// clip means downstream defenses operate on a sane subtrahend and any
// future per-step validation has bounded input. Part of
// solvent-streets-vtcs.
func TestSubtractGeoJSON_OversizedSubtrahendClippedToBoundary(t *testing.T) {
	// Boundary is a small SF square.
	boundary := `{"type":"Polygon","coordinates":[[[-122.5,37.7],[-122.4,37.7],[-122.4,37.8],[-122.5,37.8],[-122.5,37.7]]]}`
	boundaryArea, err := BoundaryAreaSqM(boundary)
	if err != nil {
		t.Fatalf("boundary area: %v", err)
	}

	// Subtrahend is a continent-spanning polygon overlapping the EAST
	// half of the boundary, but extending to -110° and +50°. Only the
	// in-boundary portion (half of boundary) should be subtracted.
	subtrahend := `{"type":"Polygon","coordinates":[[[-122.45,37.0],[-100.0,37.0],[-100.0,50.0],[-122.45,50.0],[-122.45,37.0]]]}`

	result, err := SubtractGeoJSON(boundary, subtrahend)
	if err != nil {
		t.Fatalf("SubtractGeoJSON: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}

	resultArea, err := BoundaryAreaSqM(result)
	if err != nil {
		t.Fatalf("result area: %v", err)
	}

	// Subtrahend overlaps the east half (longitude -122.45 to -122.4) →
	// remaining boundary should be ~half its original area. The longitude
	// bisection isn't a perfect metric-area bisection in UTM (the box has
	// slight north-south distortion), so a ~2% tolerance is appropriate.
	want := boundaryArea / 2
	if math.Abs(resultArea-want)/want > 0.02 {
		t.Errorf("result area = %.0f, want ~%.0f (half of %.0f)", resultArea, want, boundaryArea)
	}
}

// TestSubtractGeoJSON_FullyDisjointSubtrahendReturnsBoundary covers
// the Intersection-empty short-circuit path: a subtrahend with no
// overlap whatsoever should return the boundary unchanged (with a
// roundtrip reprojection delta tolerated).
func TestSubtractGeoJSON_FullyDisjointSubtrahendReturnsBoundary(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-122.5,37.7],[-122.4,37.7],[-122.4,37.8],[-122.5,37.8],[-122.5,37.7]]]}`
	// Subtrahend in the Atlantic, nowhere near SF.
	subtrahend := `{"type":"Polygon","coordinates":[[[-30.0,40.0],[-29.0,40.0],[-29.0,41.0],[-30.0,41.0],[-30.0,40.0]]]}`

	boundaryArea, err := BoundaryAreaSqM(boundary)
	if err != nil {
		t.Fatalf("boundary area: %v", err)
	}

	result, err := SubtractGeoJSON(boundary, subtrahend)
	if err != nil {
		t.Fatalf("SubtractGeoJSON: %v", err)
	}
	resultArea, err := BoundaryAreaSqM(result)
	if err != nil {
		t.Fatalf("result area: %v", err)
	}
	if math.Abs(resultArea-boundaryArea)/boundaryArea > 0.001 {
		t.Errorf("result area = %.0f, want ~%.0f (no change for fully disjoint)", resultArea, boundaryArea)
	}
}

func TestSubtractGeoJSON_FullCoverReturnsError(t *testing.T) {
	inner := `{"type":"Polygon","coordinates":[[[-122.0,37.7],[-121.9,37.7],[-121.9,37.8],[-122.0,37.8],[-122.0,37.7]]]}`
	cover := `{"type":"Polygon","coordinates":[[[-123.0,37.0],[-121.0,37.0],[-121.0,38.0],[-123.0,38.0],[-123.0,37.0]]]}`

	_, err := SubtractGeoJSON(inner, cover)
	if err == nil {
		t.Fatal("expected error when subtrahend fully covers boundary")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestSubtractGeoJSON_OverlappingPolygonsWithSharedEdge is the end-to-end
// sanity check for solvent-streets-i3ih: the same polygon pair as the
// bedrock TestRetainPolygonal_NormalizesMixedDimIntersectionResult test,
// but fed through SubtractGeoJSON (GeoJSON parsing + UTM projection +
// the Intersection/Difference chain). Without the fix the inner
// difference: panic would surface as an error; with the fix the
// subtraction succeeds.
//
// The polygons are placed near the equator at small lon/lat so UTM
// projection scales them to about 200 km on a side — large enough that
// the shared-edge topology survives float roundoff, small enough that
// the operation finishes instantly.
func TestSubtractGeoJSON_OverlappingPolygonsWithSharedEdge(t *testing.T) {
	// Same topology as the upstream-verified mixed-dim Intersection
	// fixture, translated to (lon, lat) and scaled by 0.01° so the
	// shared edge from (1,0) to (1,1) becomes a meaningful UTM segment.
	boundary := `{"type":"Polygon","coordinates":[[[0,0],[0,0.02],[0.02,0.02],[0.01,0.01],[0.01,0],[0,0]]]}`
	water := `{"type":"Polygon","coordinates":[[[0.01,0],[0.01,0.01],[0,0.02],[0.02,0.02],[0.02,0],[0.01,0]]]}`

	result, err := SubtractGeoJSON(boundary, water)
	if err != nil {
		t.Fatalf("SubtractGeoJSON: %v (expected success post-i3ih fix)", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if _, err := BoundaryAreaSqM(result); err != nil {
		t.Fatalf("BoundaryAreaSqM on result: %v", err)
	}
}
