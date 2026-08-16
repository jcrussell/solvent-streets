package geo

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// testProjector is a UTM projector for the Bay Area, where all the fixtures below
// sit. Zone 10N.
func testProjector(t *testing.T) *UTMProjector {
	t.Helper()
	return NewUTMProjector(-122.27, 37.80)
}

// decodePolygon pulls the ring structure back out of an emitted geometry so a
// test can assert on ring counts and point counts.
func decodePolygon(t *testing.T, gjson string) (string, [][][2]float64) {
	t.Helper()
	var raw struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gjson), &raw); err != nil {
		t.Fatalf("decode geometry: %v", err)
	}
	var rings [][][2]float64
	if err := json.Unmarshal(raw.Coordinates, &rings); err != nil {
		t.Fatalf("decode polygon rings: %v", err)
	}
	return raw.Type, rings
}

func decodeMultiPolygon(t *testing.T, gjson string) [][][][2]float64 {
	t.Helper()
	var raw struct {
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gjson), &raw); err != nil {
		t.Fatalf("decode geometry: %v", err)
	}
	var parts [][][][2]float64
	if err := json.Unmarshal(raw.Coordinates, &parts); err != nil {
		t.Fatalf("decode multipolygon parts: %v", err)
	}
	return parts
}

// squareWithNoise builds a closed ring tracing a square of side `side` degrees
// from (lon0, lat0), with `perSide` intermediate vertices jittered by `noise`
// degrees. The jitter is what RDP has to remove.
func squareWithNoise(lon0, lat0, side float64, perSide int, noise float64) [][2]float64 {
	var ring [][2]float64
	add := func(lon, lat float64) { ring = append(ring, [2]float64{lon, lat}) }
	for i := range perSide {
		f := float64(i) / float64(perSide)
		j := noise * math.Sin(float64(i)*7)
		add(lon0+side*f, lat0+j)
	}
	for i := range perSide {
		f := float64(i) / float64(perSide)
		j := noise * math.Sin(float64(i)*7)
		add(lon0+side+j, lat0+side*f)
	}
	for i := range perSide {
		f := float64(i) / float64(perSide)
		j := noise * math.Sin(float64(i)*7)
		add(lon0+side-side*f, lat0+side+j)
	}
	for i := range perSide {
		f := float64(i) / float64(perSide)
		j := noise * math.Sin(float64(i)*7)
		add(lon0+j, lat0+side-side*f)
	}
	ring = append(ring, ring[0]) // close
	return ring
}

func polygonJSON(t *testing.T, rings ...[][2]float64) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "Polygon", "coordinates": rings})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(b)
}

// TestSimplifyGeoJSONMeters_ReducesVertices is the base case: a noisy ring loses
// most of its vertices and stays a closed ring.
func TestSimplifyGeoJSONMeters_ReducesVertices(t *testing.T) {
	proj := testProjector(t)
	ring := squareWithNoise(-122.30, 37.80, 0.02, 40, 0.00005)
	in := polygonJSON(t, ring)

	out, err := SimplifyGeoJSONMeters(in, proj, 10, 6)
	if err != nil {
		t.Fatalf("SimplifyGeoJSONMeters: %v", err)
	}
	typ, rings := decodePolygon(t, out)
	if typ != "Polygon" {
		t.Errorf("type = %q; want Polygon", typ)
	}
	if len(rings) != 1 {
		t.Fatalf("ring count = %d; want 1", len(rings))
	}
	if len(rings[0]) >= len(ring) {
		t.Errorf("simplified ring has %d points; want fewer than the original %d", len(rings[0]), len(ring))
	}
	if len(rings[0]) < 4 {
		t.Errorf("simplified ring collapsed to %d points", len(rings[0]))
	}
	if first, last := rings[0][0], rings[0][len(rings[0])-1]; first != last {
		t.Errorf("ring is not closed: first=%v last=%v", first, last)
	}
}

// TestSimplifyGeoJSONMeters_PreservesRingCount is the guard that keeps holes and
// islands alive. Polygon.Simplify and MultiPolygon.Simplify drop rings that
// collapse; this implementation must not, no matter how coarse the tolerance.
func TestSimplifyGeoJSONMeters_PreservesRingCount(t *testing.T) {
	proj := testProjector(t)
	outer := squareWithNoise(-122.30, 37.80, 0.05, 30, 0.00005)
	// Two holes, one of them far below the tolerance at 10 m.
	bigHole := squareWithNoise(-122.29, 37.81, 0.01, 10, 0)
	tinyHole := squareWithNoise(-122.285, 37.815, 0.00002, 4, 0)
	in := polygonJSON(t, outer, bigHole, tinyHole)

	for _, tol := range []float64{1, 10, 100, 1000} {
		out, err := SimplifyGeoJSONMeters(in, proj, tol, 6)
		if err != nil {
			// An area tripwire at an absurd tolerance is a legitimate outcome;
			// what matters is that it returns the input, which is ring-count
			// preserving by definition.
			if out != in {
				t.Fatalf("tol=%g: error %v but output was not the input", tol, err)
			}
			continue
		}
		_, rings := decodePolygon(t, out)
		if len(rings) != 3 {
			t.Errorf("tol=%g: ring count = %d; want 3 (outer + 2 holes)", tol, len(rings))
		}
		for i, r := range rings {
			if len(r) < 4 {
				t.Errorf("tol=%g: ring %d has %d points; a ring must never drop below 4", tol, i, len(r))
			}
			if r[0] != r[len(r)-1] {
				t.Errorf("tol=%g: ring %d is not closed", tol, i)
			}
		}
	}
}

// TestSimplifyGeoJSONMeters_SmallIslandSurvives is the MultiPolygon form of the
// same guard: a part far smaller than the tolerance keeps its ring rather than
// being discarded, which is how a 302-part boundary loses landmasses.
func TestSimplifyGeoJSONMeters_SmallIslandSurvives(t *testing.T) {
	proj := testProjector(t)
	mainland := squareWithNoise(-122.30, 37.80, 0.05, 30, 0.00005)
	island := squareWithNoise(-122.20, 37.90, 0.00003, 4, 0) // ~3 m across, under a 10 m tolerance

	b, err := json.Marshal(map[string]any{
		"type":        "MultiPolygon",
		"coordinates": [][][][2]float64{{mainland}, {island}},
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	out, sErr := SimplifyGeoJSONMeters(string(b), proj, 10, 6)
	if sErr != nil {
		t.Fatalf("SimplifyGeoJSONMeters: %v", sErr)
	}
	parts := decodeMultiPolygon(t, out)
	if len(parts) != 2 {
		t.Fatalf("part count = %d; want 2 — the island was dropped", len(parts))
	}
	if len(parts[1]) != 1 {
		t.Fatalf("island ring count = %d; want 1", len(parts[1]))
	}
	if len(parts[1][0]) < 4 {
		t.Errorf("island ring has %d points; want at least 4", len(parts[1][0]))
	}
}

// TestSimplifyGeoJSONMeters_ZeroToleranceIsByteExact pins the opt-out. It is
// also the guard that keeps a negative tolerance away from LineString.Simplify,
// which does not terminate on one — so this test standing in for termination is
// deliberate, not incidental.
func TestSimplifyGeoJSONMeters_ZeroToleranceIsByteExact(t *testing.T) {
	proj := testProjector(t)
	in := polygonJSON(t, squareWithNoise(-122.30, 37.80, 0.02, 20, 0.00005))

	for _, tol := range []float64{0, -1, -1000} {
		out, err := SimplifyGeoJSONMeters(in, proj, tol, 6)
		if err != nil {
			t.Errorf("tol=%g: unexpected error %v", tol, err)
		}
		if out != in {
			t.Errorf("tol=%g: output differs from input; the opt-out must be byte-exact", tol)
		}
	}
}

// TestSimplifyGeoJSONMeters_MalformedPassthrough covers the failure contract:
// every one of these renders today, so none may become a hard failure. The
// caller emits the returned string regardless and only warns on the error.
func TestSimplifyGeoJSONMeters_MalformedPassthrough(t *testing.T) {
	proj := testProjector(t)
	cases := map[string]string{
		"not json":            `{"type": "Polygon", `,
		"feature wrapper":     `{"type":"Feature","geometry":{"type":"Polygon","coordinates":[]},"properties":{}}`,
		"geometry collection": `{"type":"GeometryCollection","geometries":[]}`,
		"point":               `{"type":"Point","coordinates":[-122.27,37.80]}`,
		"linestring":          `{"type":"LineString","coordinates":[[-122.27,37.80],[-122.26,37.81]]}`,
		"coords wrong shape":  `{"type":"Polygon","coordinates":{"nope":true}}`,
		"empty string":        ``,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := SimplifyGeoJSONMeters(in, proj, 10, 6)
			if err == nil {
				t.Errorf("want an advisory error for %q", name)
			}
			if out != in {
				t.Errorf("output must be the unchanged input on failure\n got: %q\nwant: %q", out, in)
			}
		})
	}
}

// TestSimplifyGeoJSONMeters_NilProjector is the guard-4 case that matters most
// on the server path: deriving a projector can fail, and when it does the raw
// geometry still has to go out.
func TestSimplifyGeoJSONMeters_NilProjector(t *testing.T) {
	in := polygonJSON(t, squareWithNoise(-122.30, 37.80, 0.02, 8, 0))
	out, err := SimplifyGeoJSONMeters(in, nil, 10, 6)
	if err == nil {
		t.Error("want an error for a nil projector")
	}
	if out != in {
		t.Error("output must be the unchanged input when no projector is available")
	}
}

// TestSimplifyGeoJSONMeters_ElevationCoordinate pins the parse shape that makes
// geom.NewSequence's panic unreachable. A [lon, lat, elev] coordinate is legal
// GeoJSON and BBoxFromGeoJSON accepts it; here the third element is dropped
// rather than reaching NewSequence with an odd length and panicking.
func TestSimplifyGeoJSONMeters_ElevationCoordinate(t *testing.T) {
	proj := testProjector(t)
	in := `{"type":"Polygon","coordinates":[[` +
		`[-122.300000,37.800000,12.5],` +
		`[-122.280000,37.800000,13.5],` +
		`[-122.280000,37.820000,14.5],` +
		`[-122.300000,37.820000,15.5],` +
		`[-122.300000,37.800000,12.5]]]}`

	out, err := SimplifyGeoJSONMeters(in, proj, 10, 6) // must not panic
	if err != nil {
		t.Fatalf("SimplifyGeoJSONMeters: %v", err)
	}
	_, rings := decodePolygon(t, out)
	if len(rings) != 1 {
		t.Fatalf("ring count = %d; want 1", len(rings))
	}
	for i, c := range rings[0] {
		if len(c) != 2 {
			t.Errorf("coordinate %d has %d components; output must be 2D", i, len(c))
		}
	}
}

// TestSimplifyGeoJSONMeters_RoundingDoesNotDegenerate covers the failure mode
// that every other guard misses: the checks run before rounding, so a ring that
// legitimately survives simplification can still collapse when its coordinates
// are snapped to a coarse grid. Here a ~2 m ring is rounded at 3 decimals
// (~110 m), which would flatten it to a single repeated point.
func TestSimplifyGeoJSONMeters_RoundingDoesNotDegenerate(t *testing.T) {
	proj := testProjector(t)
	tiny := squareWithNoise(-122.3000, 37.8000, 0.00002, 4, 0)
	in := polygonJSON(t, tiny)

	out, err := SimplifyGeoJSONMeters(in, proj, 1, 3)
	if err != nil {
		t.Fatalf("SimplifyGeoJSONMeters: %v", err)
	}
	_, rings := decodePolygon(t, out)
	if len(rings) != 1 {
		t.Fatalf("ring count = %d; want 1", len(rings))
	}
	if got := distinctPoints(rings[0]); got < 3 {
		t.Errorf("emitted ring has %d distinct points after rounding; want >= 3 "+
			"(it should have fallen back to the raw ring)", got)
	}
}

// TestSimplifyGeoJSONMeters_AreaTripwire checks that a wildly coarse tolerance
// is caught and falls back rather than emitting a mangled outline.
func TestSimplifyGeoJSONMeters_AreaTripwire(t *testing.T) {
	proj := testProjector(t)
	// A circle is the worst case for RDP: every vertex carries real shape, so a
	// coarse tolerance turns it into a polygon of visibly smaller area.
	var ring [][2]float64
	const n = 200
	for i := range n {
		th := 2 * math.Pi * float64(i) / n
		ring = append(ring, [2]float64{-122.27 + 0.01*math.Cos(th), 37.80 + 0.01*math.Sin(th)})
	}
	ring = append(ring, ring[0])
	in := polygonJSON(t, ring)

	// Fine tolerance: well inside the tripwire.
	if _, err := SimplifyGeoJSONMeters(in, proj, 5, 6); err != nil {
		t.Errorf("5 m tolerance should not trip the area guard: %v", err)
	}
	// Coarse tolerance: must trip and fall back byte-exactly.
	out, err := SimplifyGeoJSONMeters(in, proj, 400, 6)
	if err == nil {
		t.Fatal("400 m tolerance on a 1 km circle should trip the area tripwire")
	}
	if !strings.Contains(err.Error(), "area changed") {
		t.Errorf("error %q should name the area tripwire", err)
	}
	if out != in {
		t.Error("a tripped tripwire must return the input unchanged")
	}
}

// TestSimplifyGeoJSONMeters_RoundsCoordinates pins that the emitted precision
// actually follows the decimals argument — including on the fallback paths, so
// one file never mixes 7-decimal Nominatim coordinates with 6-decimal ones.
func TestSimplifyGeoJSONMeters_RoundsCoordinates(t *testing.T) {
	proj := testProjector(t)
	in := `{"type":"Polygon","coordinates":[[` +
		`[-122.3000001,37.8000001],` +
		`[-122.2800001,37.8000001],` +
		`[-122.2800001,37.8200001],` +
		`[-122.3000001,37.8200001],` +
		`[-122.3000001,37.8000001]]]}`

	out, err := SimplifyGeoJSONMeters(in, proj, 10, 5)
	if err != nil {
		t.Fatalf("SimplifyGeoJSONMeters: %v", err)
	}
	_, rings := decodePolygon(t, out)
	for _, c := range rings[0] {
		for _, v := range c {
			if math.Abs(v-roundTo(v, 5)) > 1e-12 {
				t.Errorf("coordinate %v carries more than 5 decimals", v)
			}
		}
	}
}
