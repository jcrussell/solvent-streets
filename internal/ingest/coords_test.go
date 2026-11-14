package ingest

import "testing"

// Ways, relations, water and boundaries all store coordinates the same way:
// 7 fixed decimals, never exponent form.
func TestGeoJSONWriters_ShareOneCoordinateFormat(t *testing.T) {
	ring := [][2]float64{
		{-121.76801234567, 37.68191234567},
		{0.00000012, 37.5},
		{-121.5, 1e-9},
		{-121.76801234567, 37.68191234567},
	}
	const want = `[-121.7680123,37.6819123],[0.0000001,37.5000000],[-121.5000000,0.0000000],[-121.7680123,37.6819123]`

	cases := map[string]string{
		"way line":         coordsToLineStringGeoJSON(ring),
		"way polygon":      coordsToPolygonGeoJSON(ring),
		"relation polygon": polysToMultiPolygonGeoJSON([]waterPolygon{{outer: ring, holes: [][][2]float64{ring}}}),
	}
	wants := map[string]string{
		"way line":         `{"type":"LineString","coordinates":[` + want + `]}`,
		"way polygon":      `{"type":"Polygon","coordinates":[[` + want + `]]}`,
		"relation polygon": `{"type":"MultiPolygon","coordinates":[[[` + want + `],[` + want + `]]]}`,
	}
	for name, got := range cases {
		if got != wants[name] {
			t.Errorf("%s:\n got %s\nwant %s", name, got, wants[name])
		}
	}
	if got, w := coordToPointGeoJSON(ring[0]), `{"type":"Point","coordinates":[-121.7680123,37.6819123]}`; got != w {
		t.Errorf("point: got %s, want %s", got, w)
	}
}
