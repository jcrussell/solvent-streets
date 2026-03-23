package geo

import (
	"math"
	"testing"
)

func TestBBoxFromGeoJSON_Polygon(t *testing.T) {
	gj := `{"type":"Polygon","coordinates":[[[-121.9,37.6],[-121.8,37.6],[-121.8,37.7],[-121.9,37.7],[-121.9,37.6]]]}`
	bbox, err := BBoxFromGeoJSON(gj)
	if err != nil {
		t.Fatal(err)
	}
	// [south, west, north, east]
	if bbox[0] != 37.6 || bbox[1] != -121.9 || bbox[2] != 37.7 || bbox[3] != -121.8 {
		t.Errorf("unexpected bbox: %v", bbox)
	}
}

func TestBBoxFromGeoJSON_MultiPolygon(t *testing.T) {
	gj := `{"type":"MultiPolygon","coordinates":[[[[-122.0,37.5],[-121.7,37.5],[-121.7,37.8],[-122.0,37.8],[-122.0,37.5]]],[[[-121.5,37.4],[-121.3,37.4],[-121.3,37.6],[-121.5,37.6],[-121.5,37.4]]]]}`
	bbox, err := BBoxFromGeoJSON(gj)
	if err != nil {
		t.Fatal(err)
	}
	if bbox[0] != 37.4 || bbox[1] != -122.0 || bbox[2] != 37.8 || bbox[3] != -121.3 {
		t.Errorf("unexpected bbox: %v", bbox)
	}
}

func TestBBoxFromGeoJSON_UnsupportedType(t *testing.T) {
	gj := `{"type":"Point","coordinates":[-121.9,37.6]}`
	_, err := BBoxFromGeoJSON(gj)
	if err == nil {
		t.Fatal("expected error for Point geometry")
	}
}

func TestBBoxFromGeoJSON_InvalidLatitude(t *testing.T) {
	gj := `{"type":"Polygon","coordinates":[[[-121.9,97.0],[-121.8,97.0],[-121.8,98.0],[-121.9,98.0],[-121.9,97.0]]]}`
	_, err := BBoxFromGeoJSON(gj)
	if err == nil {
		t.Fatal("expected error for out-of-range latitude")
	}
}

func TestBBoxFromGeoJSON_InvalidLongitude(t *testing.T) {
	gj := `{"type":"Polygon","coordinates":[[[-200.0,37.6],[-199.0,37.6],[-199.0,37.7],[-200.0,37.7],[-200.0,37.6]]]}`
	_, err := BBoxFromGeoJSON(gj)
	if err == nil {
		t.Fatal("expected error for out-of-range longitude")
	}
}

func TestCenterFromBBox(t *testing.T) {
	bbox := [4]float64{37.6, -121.9, 37.7, -121.8}
	lon, lat := CenterFromBBox(bbox)
	if math.Abs(lat-37.65) > 0.001 || math.Abs(lon-(-121.85)) > 0.001 {
		t.Errorf("unexpected center: lon=%f lat=%f", lon, lat)
	}
}
