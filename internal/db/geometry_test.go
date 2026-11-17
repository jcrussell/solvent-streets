package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/peterstace/simplefeatures/geom"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// roundTrip stores one feature with geometry gj and returns the GeoJSON
// ListFeatures gives back.
func roundTrip(t *testing.T, gj string) string {
	t.Helper()
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, []Feature{feature("w1", "overpass", gj)}, nil); err != nil {
		t.Fatalf("UpsertFeatures(%s): %v", gj, err)
	}
	got, err := store.ListFeatures(ctx, resource.TypeRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("ListFeatures = %d rows, want 1", len(got))
	}
	return got[0].GeometryJSON
}

func parseGeoJSON(t *testing.T, gj string) geom.Geometry {
	t.Helper()
	g, err := geom.UnmarshalGeoJSON([]byte(gj), geom.NoValidate{})
	if err != nil {
		t.Fatalf("parse %s: %v", gj, err)
	}
	return g
}

// TestGeometry_RoundTripsEveryStoredType: every geometry type pvmt stores,
// including an invalid self-intersecting polygon OSM can hold, reads back
// with the same structure and bit-identical coordinates (equal WKB).
func TestGeometry_RoundTripsEveryStoredType(t *testing.T) {
	cases := map[string]string{
		"point":        `{"type":"Point","coordinates":[-122.2711234,37.8044321]}`,
		"line":         `{"type":"LineString","coordinates":[[-122.2711234,37.8044321],[-122.2712,37.8045],[-122.2713001,37.8046999]]}`,
		"multiline":    `{"type":"MultiLineString","coordinates":[[[-122.1,37.1],[-122.2,37.2]],[[-122.3,37.3],[-122.4,37.4],[-122.5,37.5]]]}`,
		"polygon+hole": `{"type":"Polygon","coordinates":[[[0,0],[0.001,0],[0.001,0.001],[0,0.001],[0,0]],[[0.0002,0.0002],[0.0002,0.0004],[0.0004,0.0004],[0.0002,0.0002]]]}`,
		"multipolygon": `{"type":"MultiPolygon","coordinates":[[[[0,0],[1,0],[1,1],[0,0]]],[[[2,2],[3,2],[3,3],[2,2]],[[2.1,2.1],[2.2,2.1],[2.2,2.2],[2.1,2.1]]]]}`,
		"bowtie":       `{"type":"Polygon","coordinates":[[[0,0],[1,1],[1,0],[0,1],[0,0]]]}`,
		"3d line":      `{"type":"LineString","coordinates":[[-122.1,37.1,12.5],[-122.2,37.2,13.25]]}`,
		"multipoint":   `{"type":"MultiPoint","coordinates":[[1,2],[3,4]]}`,
		"empty line":   `{"type":"LineString","coordinates":[]}`,
	}
	for name, gj := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := geom.UnmarshalGeoJSON([]byte(gj)); name == "bowtie" && err == nil {
				t.Fatal("bowtie validates; the case no longer pins invalid geometry")
			}
			got := roundTrip(t, gj)
			want, gotG := parseGeoJSON(t, gj), parseGeoJSON(t, got)
			if !bytes.Equal(want.AsBinary(), gotG.AsBinary()) {
				t.Errorf("round trip changed the geometry:\n in  %s\n out %s", gj, got)
			}
		})
	}
}

// TestGeometry_RingsRoundTrip pins what each ring shape reads back as. A
// closed ring comes back exactly as written, including a doubled closing
// vertex and a 2-point ring, which TWKB's usual omission of the closing point
// would lose. A ring whose ends differ (open, or apart only in Z) keeps every
// vertex, and the TWKB reader appends its first point: simplefeatures'
// parser closes any ring whose ends differ, so no TWKB it reads can hold one.
func TestGeometry_RingsRoundTrip(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"doubled closing vertex",
			`{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0],[0,0]]]}`,
			`{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0],[0,0]]]}`},
		{"2-point ring",
			`{"type":"Polygon","coordinates":[[[0.5,0.5],[0.5,0.5]]]}`,
			`{"type":"Polygon","coordinates":[[[0.5,0.5],[0.5,0.5]]]}`},
		{"hole with a doubled vertex",
			`{"type":"MultiPolygon","coordinates":[[[[0,0],[4,0],[4,4],[0,0]],[[1,1],[2,1],[2,2],[2,2],[1,1]]]]}`,
			`{"type":"MultiPolygon","coordinates":[[[[0,0],[4,0],[4,4],[0,0]],[[1,1],[2,1],[2,2],[2,2],[1,1]]]]}`},
		{"3D closed ring",
			`{"type":"Polygon","coordinates":[[[0,0,5],[1,0,6],[1,1,7],[0,0,5]]]}`,
			`{"type":"Polygon","coordinates":[[[0,0,5],[1,0,6],[1,1,7],[0,0,5]]]}`},
		{"3D ends apart only in Z",
			`{"type":"Polygon","coordinates":[[[0,0,5],[1,0,6],[1,1,7],[0,0,8]]]}`,
			`{"type":"Polygon","coordinates":[[[0,0,5],[1,0,6],[1,1,7],[0,0,8],[0,0,5]]]}`},
		{"open ring",
			`{"type":"MultiPolygon","coordinates":[[[[0,0],[1,0],[1,1],[0.5,1.5]]]]}`,
			`{"type":"MultiPolygon","coordinates":[[[[0,0],[1,0],[1,1],[0.5,1.5],[0,0]]]]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roundTrip(t, tc.in); got != tc.want {
				t.Errorf("round trip of %s = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestGeometry_DigestIgnoresTheEncoding: two encodings of the same
// coordinates (TWKB with and without each ring's closing point) are different
// bytes but one digest, so the digest hashes what a reader sees, not the blob.
func TestGeometry_DigestIgnoresTheEncoding(t *testing.T) {
	ctx := context.Background()
	gj := `{"type":"Polygon","coordinates":[[[0,0],[0.001,0],[0.001,0.001],[0,0]]]}`
	g := parseGeoJSON(t, gj)
	withClose, err := geom.MarshalTWKB(g, geometryPrecision, geom.TWKBCloseRings())
	if err != nil {
		t.Fatal(err)
	}
	without, err := geom.MarshalTWKB(g, geometryPrecision)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(withClose, without) {
		t.Fatal("both encodings are the same bytes; the test proves nothing")
	}
	digest := func(blob []byte) string {
		t.Helper()
		root, _, cityID := openInputsStore(t)
		if _, err := root.db.ExecContext(ctx,
			`INSERT INTO features (city_id, resource_type, id, name, tags, geometry, source_api, fetched_at)
			 VALUES (?, 'roads', 'w1', 'n', '{}', ?, 'overpass', 1)`, cityID, blob); err != nil {
			t.Fatal(err)
		}
		sum, err := featuresDigest(ctx, root.db, cityID, "roads")
		if err != nil {
			t.Fatal(err)
		}
		return sum.digest
	}
	if a, b := digest(withClose), digest(without); a == "" || a != b {
		t.Errorf("digests %q and %q differ for the same coordinates", a, b)
	}
}

// TestGeometry_SevenDecimalsReadBackBitIdentical: OSM (7 decimals) and
// TIGER (6) coordinates read back as exactly the float64 the GeoJSON parses
// to, through the same encoding/json decode the readers use. The routing
// graph joins vertices across roads and routing-roads by exact equality.
func TestGeometry_SevenDecimalsReadBackBitIdentical(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, decimals := range []int{7, 6} {
		scale := math.Pow10(decimals)
		var coords []string
		var want [][2]float64
		for range 5000 {
			lon := float64(rng.Int64N(360*int64(scale))-180*int64(scale)) / scale
			lat := float64(rng.Int64N(180*int64(scale))-90*int64(scale)) / scale
			x, y := strconv.FormatFloat(lon, 'f', decimals, 64), strconv.FormatFloat(lat, 'f', decimals, 64)
			coords = append(coords, "["+x+","+y+"]")
			px, _ := strconv.ParseFloat(x, 64)
			py, _ := strconv.ParseFloat(y, 64)
			want = append(want, [2]float64{px, py})
		}
		gj := `{"type":"LineString","coordinates":[` + strings.Join(coords, ",") + `]}`
		var got struct {
			Coordinates [][2]float64 `json:"coordinates"`
		}
		if err := json.Unmarshal([]byte(roundTrip(t, gj)), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Coordinates) != len(want) {
			t.Fatalf("%d decimals: %d coordinates back, want %d", decimals, len(got.Coordinates), len(want))
		}
		for i, c := range got.Coordinates {
			for d := range 2 {
				if math.Float64bits(c[d]) != math.Float64bits(want[i][d]) {
					t.Fatalf("%d decimals: coordinate %d = %v, want bit-identical %v", decimals, i, c, want[i])
				}
			}
		}
	}
}

// TestGeometry_ArcGISRoundsToSevenDecimals: finer coordinates (ArcGIS
// serves 12-13 decimals) are stored at 1e-7 degrees, about 1 cm.
func TestGeometry_ArcGISRoundsToSevenDecimals(t *testing.T) {
	got := roundTrip(t, `{"type":"LineString","coordinates":[[-122.2711234567891,37.8044321456789],[-122.2711239999999,37.8044325000001]]}`)
	want := `{"type":"LineString","coordinates":[[-122.2711235,37.8044321],[-122.271124,37.8044325]]}`
	if got != want {
		t.Errorf("ArcGIS line = %s, want %s", got, want)
	}
}

// TestGeometry_DigestStableAcrossRewrite: writing back what was read, the
// way a re-ingest of unchanged data does, keeps the digest, for 7-decimal
// and for rounded ArcGIS input alike.
func TestGeometry_DigestStableAcrossRewrite(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	rt := resource.TypeRoads
	in := []Feature{
		feature("w1", "overpass", `{"type":"LineString","coordinates":[[-122.2711234,37.8044321],[-122.2712,37.8045]]}`),
		feature("a1", "arcgis", `{"type":"LineString","coordinates":[[-122.2711234567891,37.8044321456789],[-122.27,37.80]]}`),
	}
	if err := store.UpsertFeatures(ctx, rt, in, nil); err != nil {
		t.Fatal(err)
	}
	first := inputState(t, store, "roads").Digest
	for i := range 2 {
		back, err := store.ListFeatures(ctx, rt)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertFeatures(ctx, rt, back, nil); err != nil {
			t.Fatal(err)
		}
		if got := inputState(t, store, "roads").Digest; got != first {
			t.Fatalf("rewrite %d: digest %s, want %s", i+1, got, first)
		}
	}
	// A real change still moves it.
	in[0].GeometryJSON = `{"type":"LineString","coordinates":[[-122.2711234,37.8044321],[-122.2712001,37.8045]]}`
	if err := store.UpsertFeatures(ctx, rt, in, nil); err != nil {
		t.Fatal(err)
	}
	if got := inputState(t, store, "roads").Digest; got == first {
		t.Error("a moved vertex kept the digest")
	}
}

// TestGeometry_BadGeoJSONFailsTheUpsert: missing or unparsable geometry
// fails the whole upsert, naming the feature, and stores nothing.
func TestGeometry_BadGeoJSONFailsTheUpsert(t *testing.T) {
	ctx := context.Background()
	for name, gj := range map[string]string{
		"empty":        "",
		"not json":     "not json",
		"no type":      "{}",
		"unknown type": `{"type":"Circle","coordinates":[0,0]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, store, _ := openInputsStore(t)
			good := feature("w0", "overpass", geomA)
			err := store.UpsertFeatures(ctx, resource.TypeRoads, []Feature{good, feature("w1", "overpass", gj)}, nil)
			if err == nil || !strings.Contains(err.Error(), "feature w1: geometry") {
				t.Fatalf("UpsertFeatures(%q) = %v, want an error naming feature w1's geometry", gj, err)
			}
			rows, err := store.ListFeatures(ctx, resource.TypeRoads)
			if err != nil || len(rows) != 0 {
				t.Errorf("after the failed upsert: %d rows (%v), want none", len(rows), err)
			}
		})
	}
}

// TestGeometry_StoredAsTWKB: the column holds TWKB at precision 7, the
// header records it, and it is far smaller than the GeoJSON.
func TestGeometry_StoredAsTWKB(t *testing.T) {
	ctx := context.Background()
	root, store, _ := openInputsStore(t)
	var coords []string
	for i := range 50 {
		coords = append(coords, fmt.Sprintf("[%.7f,%.7f]", -122.27+float64(i)*0.0000731, 37.80+float64(i)*0.0000417))
	}
	gj := `{"type":"LineString","coordinates":[` + strings.Join(coords, ",") + `]}`
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, []Feature{feature("w1", "overpass", gj)}, nil); err != nil {
		t.Fatal(err)
	}
	var b []byte
	if err := root.db.QueryRowContext(ctx, `SELECT geometry FROM features`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	// TWKB's first byte: precision (zigzag) in the high nibble, type low.
	if prec, typ := int(b[0]>>4), b[0]&0x0f; prec != 2*geometryPrecision || typ != 2 {
		t.Errorf("header byte %#x: precision nibble %d type %d, want %d (zigzag 7) and 2 (LineString)", b[0], prec, typ, 2*geometryPrecision)
	}
	if len(b)*4 > len(gj) {
		t.Errorf("TWKB is %d bytes for %d bytes of GeoJSON, want under a quarter", len(b), len(gj))
	}
}
