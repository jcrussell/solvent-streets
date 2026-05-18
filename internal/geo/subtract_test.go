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
