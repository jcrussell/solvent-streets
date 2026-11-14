package geo

import (
	"math"
	"strings"
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

func TestBBoxFromGeoJSON_AntimeridianCrossing(t *testing.T) {
	// A polygon with points near both ±180° yields a lon span > 180°, which
	// signals an antimeridian-crossing boundary and must be rejected.
	gj := `{"type":"Polygon","coordinates":[[[-179.0,-16.5],[178.0,-16.5],[178.0,-16.0],[-179.0,-16.0],[-179.0,-16.5]]]}`
	_, err := BBoxFromGeoJSON(gj)
	if err == nil {
		t.Fatal("expected error for antimeridian-crossing boundary")
	}
	if !strings.Contains(err.Error(), "antimeridian-crossing boundaries unsupported") {
		t.Errorf("got %q, want error mentioning antimeridian-crossing boundaries unsupported", err.Error())
	}
}

// PadBBox grows every side by at least the distance asked, measured in
// projected metres, and clamps at the poles.
func TestPadBBox(t *testing.T) {
	bbox := [4]float64{37.62, -121.85, 37.72, -121.69} // Livermore-ish
	p := PadBBox(bbox, 5000)
	proj := NewUTMProjector(-121.77, 37.67)
	// dist is the projected metres between two lon/lat points.
	dist := func(lon0, lat0, lon1, lat1 float64) float64 {
		x0, y0 := proj.ToProjected(lon0, lat0)
		x1, y1 := proj.ToProjected(lon1, lat1)
		return math.Hypot(x1-x0, y1-y0)
	}
	for name, d := range map[string]float64{
		"south": dist(bbox[1], bbox[0], bbox[1], p[0]),
		"north": dist(bbox[1], bbox[2], bbox[1], p[2]),
		"west":  dist(bbox[1], bbox[2], p[1], bbox[2]),
		"east":  dist(bbox[3], bbox[2], p[3], bbox[2]),
	} {
		if d < 4995 || d > 5100 { // UTM's 0.9996 scale on the central meridian
			t.Errorf("%s pad = %.0f m, want at least 5000 and not much more", name, d)
		}
	}
	if polar := PadBBox([4]float64{89.99, -10, 90, 10}, 5000); polar[2] != 90 || polar[1] != -180 || polar[3] != 180 {
		t.Errorf("polar pad = %v, want clamped to the pole and the antimeridian", polar)
	}
}
