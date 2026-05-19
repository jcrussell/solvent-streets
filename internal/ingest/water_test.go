package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestFetchOSMWater_Success(t *testing.T) {
	body := `{"elements":[
		{"type":"way","id":1,"tags":{"natural":"water"},"geometry":[
			{"lat":42.36,"lon":-71.06},
			{"lat":42.36,"lon":-71.05},
			{"lat":42.37,"lon":-71.05},
			{"lat":42.37,"lon":-71.06},
			{"lat":42.36,"lon":-71.06}
		]}
	]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "pvmt") {
			t.Errorf("expected User-Agent containing 'pvmt', got %q", ua)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		q := r.PostForm.Get("data")
		if !strings.Contains(q, `way["natural"="water"]`) {
			t.Errorf("expected query to fetch natural=water ways; got %q", q)
		}
		if !strings.Contains(q, `relation["natural"="water"]`) {
			t.Errorf("expected query to fetch natural=water relations; got %q", q)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	result, err := fetchOSMWater(context.Background(), srv.Client(), srv.URL, [4]float64{42.0, -72.0, 43.0, -71.0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result, `{"type":"MultiPolygon"`) {
		t.Errorf("expected MultiPolygon GeoJSON; got %q", result)
	}
	if !strings.Contains(result, "-71.06") || !strings.Contains(result, "42.36") {
		t.Errorf("missing expected coordinates in result %q", result)
	}
}

func TestFetchOSMWater_NoWaterReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"elements":[]}`))
	}))
	t.Cleanup(srv.Close)

	result, err := fetchOSMWater(context.Background(), srv.Client(), srv.URL, [4]float64{0, 0, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result for no water; got %q", result)
	}
}

func TestFetchOSMWater_SkipsOpenWays(t *testing.T) {
	// First way is open (a coastline-style polyline); second is a closed
	// water polygon. Only the closed one should make it into the output.
	body := `{"elements":[
		{"type":"way","id":1,"tags":{"natural":"coastline"},"geometry":[
			{"lat":42.36,"lon":-71.06},
			{"lat":42.36,"lon":-71.05},
			{"lat":42.37,"lon":-71.05}
		]},
		{"type":"way","id":2,"tags":{"natural":"water"},"geometry":[
			{"lat":42.36,"lon":-71.06},
			{"lat":42.36,"lon":-71.05},
			{"lat":42.37,"lon":-71.05},
			{"lat":42.37,"lon":-71.06},
			{"lat":42.36,"lon":-71.06}
		]}
	]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	result, err := fetchOSMWater(context.Background(), srv.Client(), srv.URL, [4]float64{0, 0, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one polygon should land in the MultiPolygon (the closed
	// natural=water way; the open coastline way is dropped).
	var parsed struct {
		Type        string           `json:"type"`
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result json: %v: %s", err, result)
	}
	if parsed.Type != "MultiPolygon" {
		t.Errorf("expected MultiPolygon, got %s", parsed.Type)
	}
	if got := len(parsed.Coordinates); got != 1 {
		t.Errorf("expected 1 polygon, got %d: %s", got, result)
	}
}

func TestFetchOSMWater_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	_, err := fetchOSMWater(context.Background(), srv.Client(), srv.URL, [4]float64{0, 0, 1, 1})
	if err == nil {
		t.Fatal("expected error on non-200")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("expected 502 in error, got: %v", err)
	}
}

// TestParseWaterResponse_RelationStitchedOuter exercises the canonical
// Boston-harbor case: a multipolygon relation whose outer boundary is
// split across multiple unclosed member ways that only form a ring when
// chained. Without ring stitching this relation would be dropped.
func TestParseWaterResponse_RelationStitchedOuter(t *testing.T) {
	// Square 0,0 → 1,0 → 1,1 → 0,1 → 0,0 split into three open ways:
	//   w1: 0,0 → 1,0 → 1,1
	//   w2: 1,1 → 0,1
	//   w3: 0,1 → 0,0
	body := `{"elements":[{
		"type":"relation","id":42,"tags":{"natural":"water","type":"multipolygon"},
		"members":[
			{"type":"way","ref":1,"role":"outer","geometry":[{"lat":0,"lon":0},{"lat":0,"lon":1},{"lat":1,"lon":1}]},
			{"type":"way","ref":2,"role":"outer","geometry":[{"lat":1,"lon":1},{"lat":1,"lon":0}]},
			{"type":"way","ref":3,"role":"outer","geometry":[{"lat":1,"lon":0},{"lat":0,"lon":0}]}
		]
	}]}`

	result, err := parseWaterResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Type        string           `json:"type"`
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result json: %v: %s", err, result)
	}
	if parsed.Type != "MultiPolygon" || len(parsed.Coordinates) != 1 {
		t.Fatalf("expected one polygon; got %s with %d polygons: %s",
			parsed.Type, len(parsed.Coordinates), result)
	}
	outer := parsed.Coordinates[0][0]
	if !ringsEquivalent(outer, [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}) {
		t.Errorf("stitched outer ring not equivalent to expected square: got %v", outer)
	}
}

// TestParseWaterResponse_RelationWithInnerHole verifies that a relation
// with both outer and inner members produces a polygon with a hole.
func TestParseWaterResponse_RelationWithInnerHole(t *testing.T) {
	body := `{"elements":[{
		"type":"relation","id":7,"tags":{"natural":"water","type":"multipolygon"},
		"members":[
			{"type":"way","ref":10,"role":"outer","geometry":[
				{"lat":0,"lon":0},{"lat":0,"lon":10},{"lat":10,"lon":10},{"lat":10,"lon":0},{"lat":0,"lon":0}
			]},
			{"type":"way","ref":11,"role":"inner","geometry":[
				{"lat":4,"lon":4},{"lat":4,"lon":6},{"lat":6,"lon":6},{"lat":6,"lon":4},{"lat":4,"lon":4}
			]}
		]
	}]}`

	result, err := parseWaterResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Type        string           `json:"type"`
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result json: %v: %s", err, result)
	}
	if len(parsed.Coordinates) != 1 {
		t.Fatalf("expected 1 polygon, got %d: %s", len(parsed.Coordinates), result)
	}
	if got := len(parsed.Coordinates[0]); got != 2 {
		t.Fatalf("expected polygon with 1 outer + 1 hole = 2 rings; got %d", got)
	}
}

// TestParseWaterResponse_RelationDropsOpenChain verifies that an
// unstitchable outer (gap in the chain) is dropped rather than emitted
// as a broken polygon — bad polygons crash the JTS overlay downstream.
func TestParseWaterResponse_RelationDropsOpenChain(t *testing.T) {
	body := `{"elements":[{
		"type":"relation","id":42,"tags":{"natural":"water"},
		"members":[
			{"type":"way","ref":1,"role":"outer","geometry":[{"lat":0,"lon":0},{"lat":0,"lon":1}]},
			{"type":"way","ref":2,"role":"outer","geometry":[{"lat":5,"lon":5},{"lat":6,"lon":6}]}
		]
	}]}`

	result, err := parseWaterResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result when relation cannot be stitched; got %q", result)
	}
}

func TestStitchRings_AlreadyClosed(t *testing.T) {
	ways := [][][2]float64{
		{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}},
	}
	rings := stitchRings(ways)
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	if !ringsEquivalent(rings[0], ways[0]) {
		t.Errorf("ring changed unexpectedly: got %v", rings[0])
	}
}

func TestStitchRings_TwoWaysChainForward(t *testing.T) {
	ways := [][][2]float64{
		{{0, 0}, {1, 0}, {1, 1}},
		{{1, 1}, {0, 1}, {0, 0}},
	}
	rings := stitchRings(ways)
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	want := [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}
	if !ringsEquivalent(rings[0], want) {
		t.Errorf("got %v, want %v", rings[0], want)
	}
}

func TestStitchRings_ReversedWayChained(t *testing.T) {
	// Same square but the second way is written in reverse — the stitcher
	// must walk it tail-to-head to extend the ring.
	ways := [][][2]float64{
		{{0, 0}, {1, 0}, {1, 1}},
		{{0, 0}, {0, 1}, {1, 1}}, // reversed: ends where ring1 ends
	}
	rings := stitchRings(ways)
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	want := [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}
	if !ringsEquivalent(rings[0], want) {
		t.Errorf("got %v, want %v", rings[0], want)
	}
}

func TestStitchRings_OpenChainDropped(t *testing.T) {
	// Two ways that form a chain but never close back to the start.
	ways := [][][2]float64{
		{{0, 0}, {1, 0}},
		{{1, 0}, {2, 0}},
	}
	rings := stitchRings(ways)
	if len(rings) != 0 {
		t.Errorf("expected open chain to be dropped, got %d rings: %v", len(rings), rings)
	}
}

func TestPointInRing(t *testing.T) {
	ring := [][2]float64{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}
	cases := []struct {
		p    [2]float64
		want bool
	}{
		{[2]float64{5, 5}, true},
		{[2]float64{-1, 5}, false},
		{[2]float64{11, 5}, false},
		{[2]float64{5, -1}, false},
		{[2]float64{5, 11}, false},
	}
	for _, c := range cases {
		if got := pointInRing(c.p, ring); got != c.want {
			t.Errorf("pointInRing(%v) = %v, want %v", c.p, got, c.want)
		}
	}
}

// ringsEquivalent compares two closed rings ignoring start-point offset
// and direction (CW vs CCW). Stitching may emit a ring starting at any
// member-way endpoint depending on traversal order; the geometry is the
// same either way.
func ringsEquivalent(a, b [][2]float64) bool {
	if len(a) != len(b) || !isClosedRing(a) || !isClosedRing(b) {
		return false
	}
	// Drop the closing vertex; cycle a until it matches b in either
	// direction.
	ao := a[:len(a)-1]
	bo := b[:len(b)-1]
	n := len(ao)
	for offset := range n {
		fwd := true
		rev := true
		for i := range n {
			if ao[(i+offset)%n] != bo[i] {
				fwd = false
			}
			if ao[(offset-i+n*n)%n] != bo[i] {
				rev = false
			}
		}
		if fwd || rev {
			return true
		}
	}
	// Final sanity check against deep equality on the closed forms.
	return reflect.DeepEqual(a, b)
}
