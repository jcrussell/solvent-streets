package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/logs"
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
	// First way is a coastline polyline lying entirely outside the query
	// bbox (clipping discards it); second is a closed water polygon. Only
	// the closed polygon should make it into the output.
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

	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{0, 0, 1, 1})
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

	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{0, 0, 10, 10})
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

	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{0, 0, 10, 10})
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result when relation cannot be stitched; got %q", result)
	}
}

// TestParseWaterResponse_LogsDroppedMemberWays verifies that when a
// water relation has unstitchable member ways, parseWaterResponse logs
// a warning naming the relation id and the dropped member-way ids.
// This is the observability hook for real-world OSM data losing chunks
// of water area to fragmented relations.
func TestParseWaterResponse_LogsDroppedMemberWays(t *testing.T) {
	body := `{"elements":[{
		"type":"relation","id":4242,"tags":{"natural":"water"},
		"members":[
			{"type":"way","ref":101,"role":"outer","geometry":[{"lat":0,"lon":0},{"lat":0,"lon":1}]},
			{"type":"way","ref":202,"role":"outer","geometry":[{"lat":5,"lon":5},{"lat":6,"lon":6}]}
		]
	}]}`

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := logs.WithLogger(context.Background(), logger)

	if _, err := parseWaterResponse(ctx, []byte(body), [4]float64{0, 0, 10, 10}); err != nil {
		t.Fatal(err)
	}

	var record struct {
		Msg          string  `json:"msg"`
		Level        string  `json:"level"`
		Relation     int64   `json:"relation"`
		DroppedOuter []int64 `json:"dropped_outer"`
		DroppedInner []int64 `json:"dropped_inner"`
	}
	line := bytes.TrimSpace(buf.Bytes())
	if len(line) == 0 {
		t.Fatal("expected warn log line; got nothing")
	}
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatalf("parse log line: %v: %s", err, line)
	}
	if record.Level != "WARN" {
		t.Errorf("level = %q, want WARN", record.Level)
	}
	if !strings.Contains(record.Msg, "dropped") {
		t.Errorf("msg = %q, want it to mention dropping", record.Msg)
	}
	if record.Relation != 4242 {
		t.Errorf("relation = %d, want 4242", record.Relation)
	}
	wantDropped := map[int64]bool{101: true, 202: true}
	if len(record.DroppedOuter) != len(wantDropped) {
		t.Fatalf("dropped_outer = %v, want ids %v", record.DroppedOuter, wantDropped)
	}
	for _, id := range record.DroppedOuter {
		if !wantDropped[id] {
			t.Errorf("unexpected dropped outer id %d (want %v)", id, wantDropped)
		}
	}
	if len(record.DroppedInner) != 0 {
		t.Errorf("dropped_inner = %v, want empty", record.DroppedInner)
	}
}

func TestStitchRings_AlreadyClosed(t *testing.T) {
	ways := []stitchInput{
		{id: 1, coords: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}},
	}
	rings, dropped := stitchRings(ways)
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	if len(dropped) != 0 {
		t.Errorf("expected no dropped ids, got %v", dropped)
	}
	if !ringsEquivalent(rings[0], ways[0].coords) {
		t.Errorf("ring changed unexpectedly: got %v", rings[0])
	}
}

func TestStitchRings_TwoWaysChainForward(t *testing.T) {
	ways := []stitchInput{
		{id: 1, coords: [][2]float64{{0, 0}, {1, 0}, {1, 1}}},
		{id: 2, coords: [][2]float64{{1, 1}, {0, 1}, {0, 0}}},
	}
	rings, dropped := stitchRings(ways)
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	if len(dropped) != 0 {
		t.Errorf("expected no dropped ids, got %v", dropped)
	}
	want := [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}
	if !ringsEquivalent(rings[0], want) {
		t.Errorf("got %v, want %v", rings[0], want)
	}
}

func TestStitchRings_ReversedWayChained(t *testing.T) {
	// Same square but the second way is written in reverse — the stitcher
	// must walk it tail-to-head to extend the ring.
	ways := []stitchInput{
		{id: 1, coords: [][2]float64{{0, 0}, {1, 0}, {1, 1}}},
		{id: 2, coords: [][2]float64{{0, 0}, {0, 1}, {1, 1}}}, // reversed: ends where ring1 ends
	}
	rings, _ := stitchRings(ways)
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	want := [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}
	if !ringsEquivalent(rings[0], want) {
		t.Errorf("got %v, want %v", rings[0], want)
	}
}

func TestStitchRings_OpenChainDropped(t *testing.T) {
	// Two ways that form a chain but never close back to the start. The
	// ids of the partial chain must surface in `dropped` so callers can
	// log which OSM ways were lost.
	ways := []stitchInput{
		{id: 11, coords: [][2]float64{{0, 0}, {1, 0}}},
		{id: 22, coords: [][2]float64{{1, 0}, {2, 0}}},
	}
	rings, dropped := stitchRings(ways)
	if len(rings) != 0 {
		t.Errorf("expected open chain to be dropped, got %d rings: %v", len(rings), rings)
	}
	want := map[int64]bool{11: true, 22: true}
	if len(dropped) != len(want) {
		t.Fatalf("expected dropped ids %v, got %v", want, dropped)
	}
	for _, id := range dropped {
		if !want[id] {
			t.Errorf("unexpected dropped id %d (want %v)", id, want)
		}
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

func TestStitchCoastlineChains_PreservesDirection(t *testing.T) {
	// Three ways meeting head-to-tail. Reversal is forbidden so they
	// must already be oriented to chain forward.
	ways := [][][2]float64{
		{{0, 0}, {1, 0}},
		{{1, 0}, {1, 1}},
		{{1, 1}, {0, 1}},
	}
	chains := stitchCoastlineChains(ways)
	if len(chains) != 1 {
		t.Fatalf("expected 1 chain, got %d", len(chains))
	}
	want := [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	if !reflect.DeepEqual(chains[0], want) {
		t.Errorf("chain = %v, want %v", chains[0], want)
	}
}

func TestStitchCoastlineChains_FindsHeadBeforeBuilding(t *testing.T) {
	// Given the middle way first, stitching should walk back to the
	// chain head and then forward through every way.
	ways := [][][2]float64{
		{{1, 0}, {1, 1}}, // middle
		{{0, 0}, {1, 0}}, // head
		{{1, 1}, {0, 1}}, // tail
	}
	chains := stitchCoastlineChains(ways)
	if len(chains) != 1 {
		t.Fatalf("expected 1 chain, got %d", len(chains))
	}
	want := [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	if !reflect.DeepEqual(chains[0], want) {
		t.Errorf("chain = %v, want %v", chains[0], want)
	}
}

func TestStitchCoastlineChains_DoesNotReverseWays(t *testing.T) {
	// Two ways that would chain only if the second were reversed; since
	// coastline orientation is sacred, they must be emitted as two
	// separate chains rather than fused.
	ways := [][][2]float64{
		{{0, 0}, {1, 0}, {1, 1}},
		{{0, 0}, {0, 1}, {1, 1}}, // ends where ways[0] ends — would need reversal to chain
	}
	chains := stitchCoastlineChains(ways)
	if len(chains) != 2 {
		t.Fatalf("expected 2 chains (no reversal allowed), got %d: %v", len(chains), chains)
	}
}

func TestStitchCoastlineChains_ClosedLoop(t *testing.T) {
	// A coastline that loops back to itself entirely inside the bbox.
	ways := [][][2]float64{
		{{0, 0}, {1, 0}},
		{{1, 0}, {1, 1}},
		{{1, 1}, {0, 1}},
		{{0, 1}, {0, 0}},
	}
	chains := stitchCoastlineChains(ways)
	if len(chains) != 1 {
		t.Fatalf("expected 1 chain, got %d", len(chains))
	}
	if !isClosedRing(chains[0]) {
		t.Errorf("expected closed ring, got %v", chains[0])
	}
}

func TestBBoxPerimeterPos_Corners(t *testing.T) {
	bbox := [4]float64{0, 0, 1, 1} // south,west,north,east
	// height = width = 1, perim = 4
	cases := []struct {
		p    [2]float64
		want float64
	}{
		{[2]float64{1, 1}, 0},      // NE
		{[2]float64{1, 0}, 1},      // SE
		{[2]float64{0, 0}, 2},      // SW
		{[2]float64{0, 1}, 3},      // NW
		{[2]float64{1, 0.5}, 0.5},  // mid east edge
		{[2]float64{0.5, 0}, 1.5},  // mid south edge
		{[2]float64{0, 0.5}, 2.5},  // mid west edge
		{[2]float64{0.5, 1}, 3.5},  // mid north edge
		{[2]float64{0.5, 0.5}, -1}, // interior
	}
	for _, c := range cases {
		got := bboxPerimeterPos(c.p, bbox)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("bboxPerimeterPos(%v) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestBBoxWalkCW(t *testing.T) {
	bbox := [4]float64{0, 0, 1, 1}
	cases := []struct {
		name string
		from [2]float64
		to   [2]float64
		want [][2]float64
	}{
		{
			name: "east edge to west edge, water south",
			from: [2]float64{1, 0.5},
			to:   [2]float64{0, 0.5},
			want: [][2]float64{{1, 0}, {0, 0}}, // SE, SW
		},
		{
			name: "west edge to east edge, water north",
			from: [2]float64{0, 0.5},
			to:   [2]float64{1, 0.5},
			want: [][2]float64{{0, 1}, {1, 1}}, // NW, NE
		},
		{
			name: "same edge, short CW step",
			from: [2]float64{1, 0.7},
			to:   [2]float64{1, 0.3},
			want: nil, // no corner needed
		},
		{
			name: "north to east across NE corner",
			from: [2]float64{0.5, 1},
			to:   [2]float64{1, 0.5},
			want: [][2]float64{{1, 1}}, // NE only
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bboxWalkCW(c.from, c.to, bbox)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("bboxWalkCW(%v, %v) = %v, want %v", c.from, c.to, got, c.want)
			}
		})
	}
}

func TestCloseCoastlineChain_NorthFacingCoast(t *testing.T) {
	// Coastline crosses the bbox west-to-east at y=0.5. By OSM
	// convention water is on the right of start→end direction, so
	// water is south. The closed polygon should enclose the southern
	// half of the bbox.
	bbox := [4]float64{0, 0, 1, 1}
	chain := [][2]float64{{0, 0.5}, {1, 0.5}}
	rings := closeCoastlineChain(context.Background(), chain, bbox)
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	want := [][2]float64{{0, 0.5}, {1, 0.5}, {1, 0}, {0, 0}, {0, 0.5}}
	if !reflect.DeepEqual(rings[0], want) {
		t.Errorf("ring = %v, want %v", rings[0], want)
	}
}

func TestCloseCoastlineChain_ClipsAndCloses(t *testing.T) {
	// Coastline extends beyond bbox at both ends. After clipping the
	// in-bbox segment runs from (0, 0.5) to (1, 0.5); closing should
	// then add the southern corners.
	bbox := [4]float64{0, 0, 1, 1}
	chain := [][2]float64{{-0.5, 0.5}, {1.5, 0.5}}
	rings := closeCoastlineChain(context.Background(), chain, bbox)
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	got := rings[0]
	want := [][2]float64{{0, 0.5}, {1, 0.5}, {1, 0}, {0, 0}, {0, 0.5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ring = %v, want %v", got, want)
	}
}

func TestCloseCoastlineChain_DropsInteriorChain(t *testing.T) {
	// A chain entirely inside the bbox with endpoints away from any
	// edge can't be closed using bbox-edge rules. Should be dropped.
	bbox := [4]float64{0, 0, 1, 1}
	chain := [][2]float64{{0.2, 0.5}, {0.8, 0.5}}
	rings := closeCoastlineChain(context.Background(), chain, bbox)
	if len(rings) != 0 {
		t.Errorf("expected interior chain to be dropped, got %d rings: %v", len(rings), rings)
	}
}

func TestCloseCoastlineChain_PassesThroughCWClosed(t *testing.T) {
	// CW closed coastline: walk goes north → east → south → west.
	// By the OSM right-hand-water rule, water is on the right of the
	// walk direction, which is the inside of the ring — a lake.
	// Should pass through unchanged.
	bbox := [4]float64{0, 0, 1, 1}
	chain := [][2]float64{{0.2, 0.2}, {0.2, 0.8}, {0.8, 0.8}, {0.8, 0.2}, {0.2, 0.2}}
	rings := closeCoastlineChain(context.Background(), chain, bbox)
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	if !reflect.DeepEqual(rings[0], chain) {
		t.Errorf("CW closed chain should pass through unchanged: got %v", rings[0])
	}
}

func TestCloseCoastlineChain_DropsCCWClosedRingAsIsland(t *testing.T) {
	// CCW closed coastline: walk goes east → north → west → south.
	// Water on the right places water OUTSIDE the ring, so this is
	// an island (land inside), not a water polygon. Must be dropped
	// with a warning naming the right-hand rule violation, otherwise
	// the island's land area would be subtracted from the city
	// boundary as if it were water.
	bbox := [4]float64{0, 0, 1, 1}
	chain := [][2]float64{{0.2, 0.2}, {0.8, 0.2}, {0.8, 0.8}, {0.2, 0.8}, {0.2, 0.2}}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := logs.WithLogger(context.Background(), logger)

	rings := closeCoastlineChain(ctx, chain, bbox)
	if len(rings) != 0 {
		t.Fatalf("expected CCW closed chain to be dropped, got %d rings: %v", len(rings), rings)
	}

	line := bytes.TrimSpace(buf.Bytes())
	if len(line) == 0 {
		t.Fatal("expected warn log line; got nothing")
	}
	var record struct {
		Msg      string `json:"msg"`
		Level    string `json:"level"`
		Vertices int    `json:"vertices"`
	}
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatalf("parse log line: %v: %s", err, line)
	}
	if record.Level != "WARN" {
		t.Errorf("level = %q, want WARN", record.Level)
	}
	if !strings.Contains(record.Msg, "CCW") {
		t.Errorf("msg = %q, want it to mention CCW orientation", record.Msg)
	}
	if record.Vertices != len(chain) {
		t.Errorf("vertices = %d, want %d", record.Vertices, len(chain))
	}
}

func TestRingIsCW(t *testing.T) {
	cases := []struct {
		name string
		ring [][2]float64
		want bool
	}{
		{
			name: "CW unit square (north→east→south→west)",
			ring: [][2]float64{{0, 0}, {0, 1}, {1, 1}, {1, 0}, {0, 0}},
			want: true,
		},
		{
			name: "CCW unit square (east→north→west→south)",
			ring: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}},
			want: false,
		},
		{
			name: "south-strip bbox closure (CW)",
			ring: [][2]float64{{0, 0.5}, {1, 0.5}, {1, 0}, {0, 0}, {0, 0.5}},
			want: true,
		},
		{
			name: "degenerate triangle below ring-vertex threshold",
			ring: [][2]float64{{0, 0}, {1, 0}, {0, 0}},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ringIsCW(c.ring); got != c.want {
				t.Errorf("ringIsCW(%v) = %v, want %v", c.ring, got, c.want)
			}
		})
	}
}

// TestParseWaterResponse_CoastlineClosedAtBBox is the integration test
// for the coastline pipeline: an open coastline way that exits the
// bbox at two points becomes a water polygon whose extra vertices walk
// the bbox boundary CW from the tail to the head.
func TestParseWaterResponse_CoastlineClosedAtBBox(t *testing.T) {
	body := `{"elements":[
		{"type":"way","id":1,"tags":{"natural":"coastline"},"geometry":[
			{"lat":0.5,"lon":0},
			{"lat":0.5,"lon":1}
		]}
	]}`
	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{0, 0, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Type        string           `json:"type"`
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse: %v: %s", err, result)
	}
	if len(parsed.Coordinates) != 1 {
		t.Fatalf("expected 1 polygon, got %d: %s", len(parsed.Coordinates), result)
	}
	got := parsed.Coordinates[0][0]
	want := [][2]float64{{0, 0.5}, {1, 0.5}, {1, 0}, {0, 0}, {0, 0.5}}
	if !ringsEquivalent(got, want) {
		t.Errorf("coastline ring not equivalent to expected south-strip: got %v want %v", got, want)
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
