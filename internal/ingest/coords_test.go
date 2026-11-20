package ingest

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

// mustGeoJSON unwraps a GeoJSON writer's result, failing tb on its error:
// mustGeoJSON(t)(coordToPointGeoJSON(c)).
func mustGeoJSON(tb testing.TB) func(string, error) string {
	tb.Helper()
	return func(s string, err error) string {
		tb.Helper()
		if err != nil {
			tb.Fatalf("geojson: %v", err)
		}
		return s
	}
}

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
		"way line":         mustGeoJSON(t)(coordsToLineStringGeoJSON(ring)),
		"way polygon":      mustGeoJSON(t)(coordsToPolygonGeoJSON(ring)),
		"relation polygon": mustGeoJSON(t)(polysToMultiPolygonGeoJSON([]waterPolygon{{outer: ring, holes: [][][2]float64{ring}}})),
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
	if got, w := mustGeoJSON(t)(coordToPointGeoJSON(ring[0])), `{"type":"Point","coordinates":[-121.7680123,37.6819123]}`; got != w {
		t.Errorf("point: got %s, want %s", got, w)
	}
}

// The hand-built writers the typed encoder replaced, kept verbatim as the
// reference: a stored boundary's digest hashes this text, so the encoder
// must reproduce it byte for byte.
func legacyCoordJSON(c [2]float64) string {
	return "[" + strconv.FormatFloat(c[0], 'f', coordDecimals, 64) + "," +
		strconv.FormatFloat(c[1], 'f', coordDecimals, 64) + "]"
}

func legacyCoordListJSON(coords [][2]float64) string {
	parts := make([]string, len(coords))
	for i, c := range coords {
		parts[i] = legacyCoordJSON(c)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func legacyMultiPolygonJSON(polys []waterPolygon) string {
	parts := make([]string, len(polys))
	for i, p := range polys {
		rings := make([]string, 0, 1+len(p.holes))
		rings = append(rings, legacyCoordListJSON(p.outer))
		for _, h := range p.holes {
			rings = append(rings, legacyCoordListJSON(h))
		}
		parts[i] = "[" + strings.Join(rings, ",") + "]"
	}
	return fmt.Sprintf(`{"type":"MultiPolygon","coordinates":[%s]}`, strings.Join(parts, ","))
}

func TestGeoJSONWriters_ByteIdenticalToHandBuilt(t *testing.T) {
	negZero := math.Copysign(0, -1)
	rings := map[string][][2]float64{
		"empty":         {},
		"nil":           nil,
		"single":        {{-121.77, 37.68}},
		"negative":      {{-179.9999999, -89.9999999}, {-0.5, -45.25}, {-121.76801234567, -37.68191234567}},
		"negative zero": {{negZero, negZero}, {-0.00000004, -0.00000005}, {0.00000005, negZero}},
		"rounding":      {{1.00000005, 2.00000015}, {-1.00000005, -2.00000015}, {180, 90}, {-180, -90}},
		"tiny":          {{1e-9, -1e-12}, {5e-324, -5e-324}, {1e-7, -1e-7}},
		"closed ring":   {{-121.77, 37.68}, {-121.76, 37.68}, {-121.76, 37.69}, {-121.77, 37.68}},
	}
	for name, ring := range rings {
		if got, want := mustGeoJSON(t)(coordsToLineStringGeoJSON(ring)), `{"type":"LineString","coordinates":`+legacyCoordListJSON(ring)+`}`; got != want {
			t.Errorf("%s line:\n got %s\nwant %s", name, got, want)
		}
		if got, want := mustGeoJSON(t)(coordsToPolygonGeoJSON(ring)), `{"type":"Polygon","coordinates":[`+legacyCoordListJSON(ring)+`]}`; got != want {
			t.Errorf("%s polygon:\n got %s\nwant %s", name, got, want)
		}
		for _, c := range ring {
			if got, want := mustGeoJSON(t)(coordToPointGeoJSON(c)), `{"type":"Point","coordinates":`+legacyCoordJSON(c)+`}`; got != want {
				t.Errorf("%s point:\n got %s\nwant %s", name, got, want)
			}
		}
	}

	multis := map[string][]waterPolygon{
		"none":       nil,
		"empty":      {},
		"one":        {{outer: rings["closed ring"]}},
		"with holes": {{outer: rings["closed ring"], holes: [][][2]float64{rings["negative"], rings["negative zero"]}}},
		"several":    {{outer: rings["negative"]}, {outer: rings["rounding"], holes: [][][2]float64{rings["tiny"]}}, {outer: rings["empty"]}},
	}
	for name, polys := range multis {
		if got, want := mustGeoJSON(t)(polysToMultiPolygonGeoJSON(polys)), legacyMultiPolygonJSON(polys); got != want {
			t.Errorf("%s multipolygon:\n got %s\nwant %s", name, got, want)
		}
	}
}

func TestGeoJSONWriters_RejectNonFinite(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := coordToPointGeoJSON([2]float64{v, 37.5}); !errors.Is(err, errNonFiniteCoord) {
			t.Errorf("point lon %v: err = %v, want errNonFiniteCoord", v, err)
		}
		if _, err := coordsToLineStringGeoJSON([][2]float64{{-121.5, 37.5}, {-121.5, v}}); !errors.Is(err, errNonFiniteCoord) {
			t.Errorf("line lat %v: err = %v, want errNonFiniteCoord", v, err)
		}
		if _, err := polysToMultiPolygonGeoJSON([]waterPolygon{{outer: [][2]float64{{v, v}}}}); !errors.Is(err, errNonFiniteCoord) {
			t.Errorf("multipolygon %v: err = %v, want errNonFiniteCoord", v, err)
		}
	}
}
