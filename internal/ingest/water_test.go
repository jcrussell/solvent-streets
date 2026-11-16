package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/geo"
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

	result, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL, [4]float64{42.0, -72.0, 43.0, -71.0}, LandProbes{Points: [][2]float64{{-71.5, 42.5}}})
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

	result, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL, [4]float64{0, 0, 1, 1}, LandProbes{Points: [][2]float64{{0.5, 0.5}}})
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

	result, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL, [4]float64{0, 0, 1, 1}, LandProbes{Points: [][2]float64{{0.5, 0.5}}})
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

	_, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL, [4]float64{0, 0, 1, 1}, LandProbes{Points: [][2]float64{{0.5, 0.5}}})
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

	// bbox strictly contains the polygon so the per-outer 80%-of-bbox
	// area guard (acceptWaterPolygon) doesn't trip. Same pattern applies
	// to other parseWaterResponse tests below. landProbe is placed
	// OUTSIDE the unit-square water polygon so the new per-polygon land-
	// probe filter doesn't drop it.
	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{-1, -1, 2, 2}, LandProbes{Points: [][2]float64{{-0.5, -0.5}}})
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

	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{-5, -5, 15, 15}, LandProbes{Points: [][2]float64{{5, 5}}})
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

	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{0, 0, 10, 10}, LandProbes{Points: [][2]float64{{5, 5}}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result when relation cannot be stitched; got %q", result)
	}
}

// TestParseWaterResponse_DropsWayContainingLandProbe pins the per-
// polygon land-probe filter for closed `natural=water` ways: a way
// whose outer ring wrongly traces around city land (probe inside) is
// dropped with a structured WARN naming the way id and hit count.
// This is the root-cause defense against single-way coastline-style
// errors that the prior pipeline accepted as legitimate water.
func TestParseWaterResponse_DropsWayContainingLandProbe(t *testing.T) {
	// One closed natural=water way at unit-square (0..1, 0..1).
	body := `{"elements":[{
		"type":"way","id":101,"tags":{"natural":"water"},
		"geometry":[
			{"lat":0,"lon":0},{"lat":0,"lon":1},{"lat":1,"lon":1},{"lat":1,"lon":0},{"lat":0,"lon":0}
		]
	}]}`

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := logs.WithLogger(context.Background(), logger)

	// landProbe at (0.5, 0.5) is INSIDE the way's outer ring.
	result, err := parseWaterResponse(ctx, []byte(body), [4]float64{-1, -1, 2, 2}, LandProbes{Points: [][2]float64{{0.5, 0.5}}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result (way dropped); got %q", result)
	}
	var rec struct {
		Msg      string `json:"msg"`
		Way      int64  `json:"way"`
		LandHits int    `json:"land_hits"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("parse log line: %v: %s", err, buf.String())
	}
	if !strings.Contains(rec.Msg, "containing city land") {
		t.Errorf("log msg %q should mention land containment", rec.Msg)
	}
	if rec.Way != 101 || rec.LandHits != 1 {
		t.Errorf("log fields way=%d hits=%d; want 101 and 1", rec.Way, rec.LandHits)
	}
}

// TestParseWaterResponse_DropsRelationContainingLandProbe pins the
// per-polygon land-probe filter for `natural=water` relations: a
// relation whose stitched outer ring contains a city-land probe is
// dropped with the relation id and hit count. Symmetrical to the
// way case above but exercises the relation path of
// classifyWaterElement.
func TestParseWaterResponse_DropsRelationContainingLandProbe(t *testing.T) {
	// One natural=water relation with a single closed outer ring at
	// unit-square (0..1, 0..1) — no holes.
	body := `{"elements":[{
		"type":"relation","id":7777,"tags":{"natural":"water","type":"multipolygon"},
		"members":[
			{"type":"way","ref":1,"role":"outer","geometry":[
				{"lat":0,"lon":0},{"lat":0,"lon":1},{"lat":1,"lon":1},{"lat":1,"lon":0},{"lat":0,"lon":0}
			]}
		]
	}]}`

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := logs.WithLogger(context.Background(), logger)

	result, err := parseWaterResponse(ctx, []byte(body), [4]float64{-1, -1, 2, 2}, LandProbes{Points: [][2]float64{{0.5, 0.5}}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result (relation dropped); got %q", result)
	}
	var rec struct {
		Msg      string `json:"msg"`
		Relation int64  `json:"relation"`
		LandHits int    `json:"land_hits"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("parse log line: %v: %s", err, buf.String())
	}
	if !strings.Contains(rec.Msg, "containing city land") {
		t.Errorf("log msg %q should mention land containment", rec.Msg)
	}
	if rec.Relation != 7777 || rec.LandHits != 1 {
		t.Errorf("log fields relation=%d hits=%d; want 7777 and 1", rec.Relation, rec.LandHits)
	}
}

// TestParseWaterResponse_RelationHoleContainingProbeKeepsPolygon
// verifies the hole-awareness of the per-polygon land-probe filter:
// a probe that lies inside the polygon's OUTER ring but inside a
// hole is geometrically NOT in the water polygon (the hole is a
// non-water island), so the polygon must NOT be dropped. This pins
// countPolygonLandProbeHits's hole exclusion.
func TestParseWaterResponse_RelationHoleContainingProbeKeepsPolygon(t *testing.T) {
	// Outer at (0..10, 0..10) with a hole at (4..6, 4..6).
	body := `{"elements":[{
		"type":"relation","id":31,"tags":{"natural":"water","type":"multipolygon"},
		"members":[
			{"type":"way","ref":1,"role":"outer","geometry":[
				{"lat":0,"lon":0},{"lat":0,"lon":10},{"lat":10,"lon":10},{"lat":10,"lon":0},{"lat":0,"lon":0}
			]},
			{"type":"way","ref":2,"role":"inner","geometry":[
				{"lat":4,"lon":4},{"lat":4,"lon":6},{"lat":6,"lon":6},{"lat":6,"lon":4},{"lat":4,"lon":4}
			]}
		]
	}]}`

	// Probe at (5, 5) lies in the hole — not in the water polygon's
	// set-theoretic interior. Polygon must survive.
	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{-5, -5, 15, 15}, LandProbes{Points: [][2]float64{{5, 5}}})
	if err != nil {
		t.Fatal(err)
	}
	if result == "" {
		t.Fatal("polygon with probe-in-hole should survive the land-probe filter")
	}
	var parsed struct {
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v: %s", err, result)
	}
	if len(parsed.Coordinates) != 1 || len(parsed.Coordinates[0]) != 2 {
		t.Errorf("expected 1 polygon with outer+hole; got polygons=%d rings[0]=%d",
			len(parsed.Coordinates), len(parsed.Coordinates[0]))
	}
}

// TestParseWaterResponse_AcceptsLargeCoastlineRingInWaterDominatedBBox: a
// city whose admin boundary extends into open water (Foster City reaching
// into San Francisco Bay) produces a query bbox that is mostly water, so the
// legitimate bay ring covers far more than maxOuterBboxAreaFraction (0.8) of
// the bbox. Rejecting it would leave ~40 km² of bay in the paved_share
// denominator. Coastline rings are validated by land probes + CW orientation
// in closeCoastlineChain, so the bbox-fraction cap is not applied to them and
// the large water ring must be accepted.
func TestParseWaterResponse_AcceptsLargeCoastlineRingInWaterDominatedBBox(t *testing.T) {
	// bbox lon[0,10] lat[0,10] (area 100). A single open coastline way runs
	// from the south edge (lon=2,lat=0) to the west edge (lon=0,lat=2),
	// bowing toward the origin. Its two bbox-edge closures partition the
	// bbox into a small SW land triangle (~2) and the rest as water (~98 =
	// 98% of bbox, well over the 0.8 cap). The land probe sits in the SW
	// triangle, so the water-side ring is the large one.
	body := `{"elements":[
		{"type":"way","id":1,"tags":{"natural":"coastline"},"geometry":[
			{"lat":0,"lon":2},
			{"lat":1,"lon":1},
			{"lat":2,"lon":0}
		]}
	]}`

	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{0, 0, 10, 10}, LandProbes{Points: [][2]float64{{0.5, 0.5}}})
	if err != nil {
		t.Fatal(err)
	}
	if result == "" {
		t.Fatal("large coastline water ring was dropped; expected it to be accepted (bd-86aa)")
	}
	var parsed struct {
		Type        string           `json:"type"`
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result json: %v: %s", err, result)
	}
	if len(parsed.Coordinates) != 1 {
		t.Fatalf("expected exactly 1 water polygon, got %d: %s", len(parsed.Coordinates), result)
	}
	// Confirm the accepted ring is the large (water) side, > 0.8 of the
	// bbox — i.e. exactly the ring the old cap rejected.
	const bboxArea = 100.0
	area := math.Abs(ringSignedArea(parsed.Coordinates[0][0]))
	if frac := area / bboxArea; frac <= maxOuterBboxAreaFraction {
		t.Errorf("expected the accepted ring to exceed the bbox-fraction cap (frac > %.2f); got frac=%.3f",
			maxOuterBboxAreaFraction, frac)
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

	if _, err := parseWaterResponse(ctx, []byte(body), [4]float64{0, 0, 10, 10}, LandProbes{Points: [][2]float64{{5, 5}}}); err != nil {
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

// relWith builds a water relation overpassElement from member ways given
// as (role, coords) pairs. Mirrors the inline JSON fixtures above but in
// Go so relationToPolygons can be exercised directly.
func relWith(id int64, members ...struct {
	role   string
	ref    int64
	coords [][2]float64
}) overpassElement {
	e := overpassElement{Type: elementRelation, ID: id}
	for _, m := range members {
		geom := make([]overpassGeometryPoint, len(m.coords))
		for i, c := range m.coords {
			geom[i] = overpassGeometryPoint{Lon: c[0], Lat: c[1]}
		}
		e.Members = append(e.Members, overpassRelationMember{
			Type: elementWay, Ref: m.ref, Role: m.role, Geometry: geom,
		})
	}
	return e
}

type relMember = struct {
	role   string
	ref    int64
	coords [][2]float64
}

// TestRelationToPolygons_MultipleDisjointOuters pins MultiPolygon
// semantics: a relation with two disjoint outer rings (two separate
// water bodies under one relation) must yield TWO waterPolygons, not a
// merged or dropped result. Calls relationToPolygons directly with a
// nil acceptOuter (accept any closed ring) so the assertion is about
// ring assembly, not the area/probe filters layered above it.
func TestRelationToPolygons_MultipleDisjointOuters(t *testing.T) {
	// Two unit squares far apart so neither contains the other's vertices.
	outerA := [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}
	outerB := [][2]float64{{10, 10}, {11, 10}, {11, 11}, {10, 11}, {10, 10}}
	rel := relWith(1,
		relMember{role: "outer", ref: 1, coords: outerA},
		relMember{role: "outer", ref: 2, coords: outerB},
	)

	polys := relationToPolygons(context.Background(), rel, stitchRings, nil)
	if len(polys) != 2 {
		t.Fatalf("expected 2 disjoint outer polygons, got %d", len(polys))
	}
	// Each outer should be one of the two squares, with no holes.
	for _, p := range polys {
		if len(p.holes) != 0 {
			t.Errorf("disjoint outer should have no holes, got %d", len(p.holes))
		}
		if !ringsEquivalent(p.outer, outerA) && !ringsEquivalent(p.outer, outerB) {
			t.Errorf("outer ring %v is neither expected square", p.outer)
		}
	}
}

// TestRelationToPolygons_DroppedWaysNoPolygons pins the skip+warn
// behavior when a relation's member ways cannot be stitched into any
// closed ring: relationToPolygons returns NO polygons (so the feature
// is skipped upstream) AND logs a WARN naming the relation id and the
// dropped member-way ids. The warning sink is captured exactly as the
// existing TestParseWaterResponse_LogsDroppedMemberWays does — a
// bytes.Buffer JSON slog handler at WARN level.
func TestRelationToPolygons_DroppedWaysNoPolygons(t *testing.T) {
	// Two outer ways that form a partial chain but never close back to
	// the start, so stitchRings drops both and returns no ring.
	rel := relWith(515,
		relMember{role: "outer", ref: 11, coords: [][2]float64{{0, 0}, {1, 0}}},
		relMember{role: "outer", ref: 22, coords: [][2]float64{{1, 0}, {2, 0}}},
	)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := logs.WithLogger(context.Background(), logger)

	polys := relationToPolygons(ctx, rel, stitchRings, nil)
	if len(polys) != 0 {
		t.Fatalf("expected no polygons for unstitchable relation, got %d", len(polys))
	}

	line := bytes.TrimSpace(buf.Bytes())
	if len(line) == 0 {
		t.Fatal("expected a dropped-ways WARN line; got nothing")
	}
	var rec struct {
		Msg          string  `json:"msg"`
		Level        string  `json:"level"`
		Relation     int64   `json:"relation"`
		DroppedOuter []int64 `json:"dropped_outer"`
		DroppedInner []int64 `json:"dropped_inner"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("parse log line: %v: %s", err, line)
	}
	if rec.Level != "WARN" {
		t.Errorf("level = %q, want WARN", rec.Level)
	}
	if !strings.Contains(rec.Msg, "dropped") {
		t.Errorf("msg = %q, want it to mention dropping", rec.Msg)
	}
	if rec.Relation != 515 {
		t.Errorf("relation = %d, want 515", rec.Relation)
	}
	wantDropped := map[int64]bool{11: true, 22: true}
	if len(rec.DroppedOuter) != len(wantDropped) {
		t.Fatalf("dropped_outer = %v, want ids %v", rec.DroppedOuter, wantDropped)
	}
	for _, id := range rec.DroppedOuter {
		if !wantDropped[id] {
			t.Errorf("unexpected dropped outer id %d (want %v)", id, wantDropped)
		}
	}
	if len(rec.DroppedInner) != 0 {
		t.Errorf("dropped_inner = %v, want empty", rec.DroppedInner)
	}
}

// bboxTag formats a bbox the way buildWaterQuery embeds it (%f → 6
// decimals), so a mock Overpass handler can key its response on which
// (sub-)bbox a query targets.
func bboxTag(bbox [4]float64) string {
	return fmt.Sprintf("%f,%f,%f,%f", bbox[0], bbox[1], bbox[2], bbox[3])
}

// truncatedWaterBody is a well-formed Overpass response carrying a
// server-side timeout remark — HTTP 200 with partial (here empty)
// elements, the shape that fetchSplitting must split-and-retry.
const truncatedWaterBody = `{"remark":"runtime error: Query timed out in \"query\" at line 1 after 60 seconds.","elements":[]}`

// closedWayBody returns a single closed natural=water way with the given
// id and ring coords, as an Overpass response body.
func closedWayBody(id int64, ring [][2]float64) string {
	pts := make([]string, len(ring))
	for i, c := range ring {
		pts[i] = fmt.Sprintf(`{"lat":%g,"lon":%g}`, c[1], c[0])
	}
	return fmt.Sprintf(`{"elements":[{"type":"way","id":%d,"tags":{"natural":"water"},"geometry":[%s]}]}`,
		id, strings.Join(pts, ","))
}

// TestFetchOSMWater_SplitsOnTruncation is the qus4 regression: the
// full-bbox water query truncates (timeout remark), so fetchOSMWater
// must split into quadrants, collect their raw elements, dedup a water
// body that spans a quadrant boundary, and assemble ONCE against the
// original bbox. Asserts (a) the result is built from the merged
// quadrant elements, and (b) the boundary-spanning element is not
// double-counted.
func TestFetchOSMWater_SplitsOnTruncation(t *testing.T) {
	full := [4]float64{0, 0, 2, 2}
	quads := splitBBox(full) // SW, SE, NW, NE — midLat=1, midLon=1

	// Way 1 straddles the SW/SE quadrant boundary (crosses lon=1), so
	// both the SW and SE quadrant queries return it with full geometry —
	// the dedup target. It sits in the lower-left, away from the probe.
	spanning := [][2]float64{{0.4, 0.4}, {1.6, 0.4}, {1.6, 0.6}, {0.4, 0.6}, {0.4, 0.4}}
	// Way 2 lives only in the NE quadrant — a distinct second polygon.
	neOnly := [][2]float64{{1.4, 1.4}, {1.6, 1.4}, {1.6, 1.6}, {1.4, 1.6}, {1.4, 1.4}}

	swTag, seTag, nwTag, neTag := bboxTag(quads[0]), bboxTag(quads[1]), bboxTag(quads[2]), bboxTag(quads[3])
	fullTag := bboxTag(full)

	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		q := r.PostForm.Get("data")
		reqCount++
		w.WriteHeader(http.StatusOK)
		switch {
		case strings.Contains(q, fullTag):
			_, _ = w.Write([]byte(truncatedWaterBody))
		case strings.Contains(q, swTag), strings.Contains(q, seTag):
			// Both SW and SE return the same spanning way (id 1).
			_, _ = w.Write([]byte(closedWayBody(1, spanning)))
		case strings.Contains(q, neTag):
			_, _ = w.Write([]byte(closedWayBody(2, neOnly)))
		case strings.Contains(q, nwTag):
			_, _ = w.Write([]byte(`{"elements":[]}`))
		default:
			t.Errorf("unexpected query bbox: %q", q)
			_, _ = w.Write([]byte(`{"elements":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	// Probe well outside both water polygons (NW corner) so neither is
	// dropped by the land-probe filter.
	result, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL, full, LandProbes{Points: [][2]float64{{0.1, 1.9}}})
	if err != nil {
		t.Fatal(err)
	}
	if reqCount != 5 { // 1 full (truncated) + 4 quadrants
		t.Errorf("expected 5 Overpass requests (1 full + 4 quadrants), got %d", reqCount)
	}

	var parsed struct {
		Type        string           `json:"type"`
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result json: %v: %s", err, result)
	}
	if parsed.Type != "MultiPolygon" {
		t.Errorf("type = %q, want MultiPolygon", parsed.Type)
	}
	// Exactly 2 polygons: the spanning way (deduped from 2 quadrant hits
	// to 1) plus the NE-only way. A dedup failure would yield 3.
	if got := len(parsed.Coordinates); got != 2 {
		t.Fatalf("expected 2 polygons after dedup, got %d: %s", got, result)
	}
}

// TestFetchOSMWater_RecursesDeeperOnQuadrantTruncation verifies a
// quadrant that itself truncates is split AGAIN (sub-quadrants) rather
// than failing, so deeply dense areas recover by descending further.
func TestFetchOSMWater_RecursesDeeperOnQuadrantTruncation(t *testing.T) {
	full := [4]float64{0, 0, 2, 2}
	quads := splitBBox(full)
	swSubs := splitBBox(quads[0]) // sub-quadrants of the SW quadrant

	fullTag, swTag := bboxTag(full), bboxTag(quads[0])

	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		q := r.PostForm.Get("data")
		reqCount++
		w.WriteHeader(http.StatusOK)
		switch {
		case strings.Contains(q, fullTag), strings.Contains(q, swTag):
			// Full bbox AND the SW quadrant both truncate.
			_, _ = w.Write([]byte(truncatedWaterBody))
		case strings.Contains(q, bboxTag(swSubs[0])):
			// One SW sub-quadrant has a small water polygon.
			_, _ = w.Write([]byte(closedWayBody(7, [][2]float64{
				{0.1, 0.1}, {0.4, 0.1}, {0.4, 0.4}, {0.1, 0.4}, {0.1, 0.1},
			})))
		default:
			_, _ = w.Write([]byte(`{"elements":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	result, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL, full, LandProbes{Points: [][2]float64{{1.9, 1.9}}})
	if err != nil {
		t.Fatalf("expected recovery via deeper recursion, got error: %v", err)
	}
	// 1 full + 4 quadrants (SW truncates) + 4 SW sub-quadrants = 9.
	if reqCount != 9 {
		t.Errorf("expected 9 requests (full + 4 quads + 4 SW sub-quads), got %d", reqCount)
	}
	var parsed struct {
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v: %s", err, result)
	}
	if len(parsed.Coordinates) != 1 {
		t.Errorf("expected 1 polygon from the recovered sub-quadrant, got %d", len(parsed.Coordinates))
	}
}

// TestFetchOSMWater_TruncatedAtMaxDepthErrors pins fail-loud behavior: a
// query that keeps truncating all the way down past maxSplitDepth
// surfaces errOverpassTruncated rather than silently under-reporting
// water (which would inflate land area).
func TestFetchOSMWater_TruncatedAtMaxDepthErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Every query at every depth truncates.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(truncatedWaterBody))
	}))
	t.Cleanup(srv.Close)

	_, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL, [4]float64{0, 0, 2, 2}, LandProbes{Points: [][2]float64{{1, 1}}})
	if err == nil {
		t.Fatal("expected an error when truncation persists to max depth")
	}
	if !errors.Is(err, errOverpassTruncated) {
		t.Errorf("expected errOverpassTruncated, got: %v", err)
	}
}

// TestFetchOSMWater_SplitsOnCutOffJSON: a full-bbox response cut off
// mid-stream (malformed JSON) splits into quadrants like a truncation remark
// does, and the quadrants' elements are assembled into the result.
func TestFetchOSMWater_SplitsOnCutOffJSON(t *testing.T) {
	full := [4]float64{0, 0, 2, 2}
	fullTag := bboxTag(full)
	neTag := bboxTag(splitBBox(full)[3])
	neOnly := [][2]float64{{1.4, 1.4}, {1.6, 1.4}, {1.6, 1.6}, {1.4, 1.6}, {1.4, 1.4}}
	whole := closedWayBody(2, neOnly)

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		q := r.PostForm.Get("data")
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		switch {
		case strings.Contains(q, fullTag):
			_, _ = w.Write([]byte(whole[:len(whole)/2]))
		case strings.Contains(q, neTag):
			_, _ = w.Write([]byte(whole))
		default:
			_, _ = w.Write([]byte(`{"elements":[]}`))
		}
	}))
	t.Cleanup(srv.Close)

	result, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL, full, LandProbes{Points: [][2]float64{{0.1, 1.9}}})
	if err != nil {
		t.Fatalf("fetchOSMWater: %v (a cut-off response must split, not fail)", err)
	}
	if requests != 5 {
		t.Errorf("made %d requests, want 5 (1 cut off + 4 quadrants)", requests)
	}
	var parsed struct {
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result json: %v: %s", err, result)
	}
	if len(parsed.Coordinates) != 1 {
		t.Errorf("got %d polygons, want the NE quadrant's 1: %s", len(parsed.Coordinates), result)
	}
}

// TestFetchOSMWater_CutOffJSONAtMaxDepthErrors: a response that stays
// malformed at every depth stops at maxSplitDepth with the parse error.
func TestFetchOSMWater_CutOffJSONAtMaxDepthErrors(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"elements":[{"type":"way","id":`))
	}))
	t.Cleanup(srv.Close)

	_, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL, [4]float64{0, 0, 2, 2}, LandProbes{})
	if !errors.Is(err, errOverpassParse) {
		t.Fatalf("err = %v, want errOverpassParse", err)
	}
	// The first quadrant fails at each depth, so the descent is one request
	// per level: depths 0 through maxSplitDepth.
	if requests != maxSplitDepth+1 {
		t.Errorf("made %d requests, want %d", requests, maxSplitDepth+1)
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
	// Water is south; land is north. landProbe must be on the land side
	// so the water-side ring (south) is the one selected.
	rings := closeCoastlineChain(context.Background(), chain, bbox, newLandProbeIndex([][2]float64{{0.5, 0.75}}))
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
	rings := closeCoastlineChain(context.Background(), chain, bbox, newLandProbeIndex([][2]float64{{0.5, 0.75}}))
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
	rings := closeCoastlineChain(context.Background(), chain, bbox, newLandProbeIndex([][2]float64{{0.5, 0.5}}))
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
	// Lake at (0.2-0.8); land is OUTSIDE the lake (e.g. near the bbox
	// corner). landProbe must be outside the ring or the new closed-ring
	// land-probe check would drop the lake as a wrongly-traced ring.
	rings := closeCoastlineChain(context.Background(), chain, bbox, newLandProbeIndex([][2]float64{{0.9, 0.9}}))
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	if !reflect.DeepEqual(rings[0], chain) {
		t.Errorf("CW closed chain should pass through unchanged: got %v", rings[0])
	}
}

// TestCloseCoastlineChain_DropsCWClosedRingContainingLandProbe pins
// the closed-ring land-probe guard: a CW closed coastline ring that
// nevertheless encloses a known-land point is a wrongly-traced ring
// (OSM data error where land was enclosed instead of water) and must
// be dropped, otherwise its area would be subtracted as water and
// strip the city's land.
func TestCloseCoastlineChain_DropsCWClosedRingContainingLandProbe(t *testing.T) {
	bbox := [4]float64{0, 0, 1, 1}
	// Same CW ring as the PassesThrough test (so the ringIsCW guard
	// doesn't trip), but the probe is placed INSIDE the ring at
	// (0.5, 0.5). The land-probe check must drop it.
	chain := [][2]float64{{0.2, 0.2}, {0.2, 0.8}, {0.8, 0.8}, {0.8, 0.2}, {0.2, 0.2}}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := logs.WithLogger(context.Background(), logger)

	rings := closeCoastlineChain(ctx, chain, bbox, newLandProbeIndex([][2]float64{{0.5, 0.5}}))
	if len(rings) != 0 {
		t.Fatalf("expected ring containing land probe to be dropped, got %d rings: %v", len(rings), rings)
	}
	line := bytes.TrimSpace(buf.Bytes())
	if len(line) == 0 {
		t.Fatal("expected warn log line; got nothing")
	}
	var rec struct {
		Msg       string `json:"msg"`
		LandHits  int    `json:"land_hits"`
		NumProbes int    `json:"land_probes"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("parse log line: %v: %s", err, line)
	}
	if !strings.Contains(rec.Msg, "containing city land") {
		t.Errorf("log message %q should mention land containment", rec.Msg)
	}
	if rec.LandHits != 1 || rec.NumProbes != 1 {
		t.Errorf("log fields: land_hits=%d land_probes=%d, want 1 and 1",
			rec.LandHits, rec.NumProbes)
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

	rings := closeCoastlineChain(ctx, chain, bbox, newLandProbeIndex([][2]float64{{0.9, 0.9}}))
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
	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{0, 0, 1, 1}, LandProbes{Points: [][2]float64{{0.5, 0.5}}})
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

// TestParseWaterResponse_DegenerateRingDoesNotCrash exercises a closed
// way whose four coordinates are collinear (zero geometric area). The
// length+endpoint check in isClosedRing accepts it and the ring is
// emitted into the GeoJSON; downstream simplefeatures parsing may
// reject zero-area polygons in some operations. This test pins only
// that the parse layer doesn't crash and emits valid JSON — assertion
// of *what* is emitted is intentionally absent so a future degeneracy
// filter at this layer doesn't have to be coordinated with the test.
func TestParseWaterResponse_DegenerateRingDoesNotCrash(t *testing.T) {
	body := `{"elements":[
		{"type":"way","id":1,"tags":{"natural":"water"},"geometry":[
			{"lat":0,"lon":0},
			{"lat":0,"lon":1},
			{"lat":0,"lon":2},
			{"lat":0,"lon":0}
		]}
	]}`
	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{0, 0, 1, 1}, LandProbes{Points: [][2]float64{{0.5, 0.5}}})
	if err != nil {
		t.Fatalf("parseWaterResponse: %v", err)
	}
	if result == "" {
		return // filtered at parse layer is acceptable
	}
	var parsed struct {
		Type        string           `json:"type"`
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Errorf("output not parseable JSON: %v: %s", err, result)
	}
	if parsed.Type != "MultiPolygon" {
		t.Errorf("type = %q, want MultiPolygon", parsed.Type)
	}
}

// TestCloseCoastlineChain_SameEdgeEndpointsDropped pins the current
// behavior for a coastline whose endpoints both lie on the same bbox
// edge with no corner between them in the CW direction. The walk
// returns no corners, the ring degenerates to three vertices, and the
// chain is silently dropped. This is correct when water-on-right
// points outside the bbox; it is wrong when water points inside (the
// long CCW arc would be needed). The current pipeline does not
// distinguish these cases — see solvent-streets-85nn for the gap.
func TestCloseCoastlineChain_SameEdgeEndpointsDropped(t *testing.T) {
	bbox := [4]float64{0, 0, 1, 1}
	chain := [][2]float64{{0.2, 0}, {0.8, 0}} // both on south edge
	rings := closeCoastlineChain(context.Background(), chain, bbox, newLandProbeIndex([][2]float64{{0.5, 0.5}}))
	if len(rings) != 0 {
		t.Errorf("expected same-edge coastline to be dropped, got %d rings: %v", len(rings), rings)
	}
}

// TestCloseCoastlineChain_EndpointAtBBoxCorner verifies that a
// coastline ending exactly at a bbox corner closes correctly. The
// corner sits on two edges; bboxPerimeterPos resolves it to a single
// canonical perimeter position (the east-edge case fires first), and
// bboxWalkCW emits only the corners strictly between (so the touched
// corner is never duplicated).
func TestCloseCoastlineChain_EndpointAtBBoxCorner(t *testing.T) {
	bbox := [4]float64{0, 0, 1, 1}
	// West edge → NE corner. Water-on-right of west→east is south.
	// Land is north of the diagonal chain; pick a probe well above it.
	chain := [][2]float64{{0, 0.5}, {1, 1}}
	rings := closeCoastlineChain(context.Background(), chain, bbox, newLandProbeIndex([][2]float64{{0.2, 0.95}}))
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d: %v", len(rings), rings)
	}
	want := [][2]float64{{0, 0.5}, {1, 1}, {1, 0}, {0, 0}, {0, 0.5}}
	if !reflect.DeepEqual(rings[0], want) {
		t.Errorf("ring = %v, want %v", rings[0], want)
	}
}

// TestPolygonsFromRelation_NestedOuterFirstMatchWins pins the
// first-match-wins inner-to-outer assignment. When a relation has
// nested outers (outer B fully inside outer A) and an inner ring lying
// inside both, the inner attaches to whichever outer appears first in
// the relation members — outer A here. OSM convention prefers the
// smallest containing outer, but this pipeline does not implement that
// because real natural=water relations very rarely nest outers; the
// brittleness is documented in solvent-streets-85nn.
func TestPolygonsFromRelation_NestedOuterFirstMatchWins(t *testing.T) {
	// Outer A: 0..10 square. Outer B: 2..6 square, fully inside A.
	// Inner I: 3..5 square, fully inside B (and inside A).
	body := `{"elements":[{
		"type":"relation","id":99,"tags":{"natural":"water","type":"multipolygon"},
		"members":[
			{"type":"way","ref":1,"role":"outer","geometry":[
				{"lat":0,"lon":0},{"lat":0,"lon":10},{"lat":10,"lon":10},{"lat":10,"lon":0},{"lat":0,"lon":0}
			]},
			{"type":"way","ref":2,"role":"outer","geometry":[
				{"lat":2,"lon":2},{"lat":2,"lon":6},{"lat":6,"lon":6},{"lat":6,"lon":2},{"lat":2,"lon":2}
			]},
			{"type":"way","ref":3,"role":"inner","geometry":[
				{"lat":3,"lon":3},{"lat":3,"lon":5},{"lat":5,"lon":5},{"lat":5,"lon":3},{"lat":3,"lon":3}
			]}
		]
	}]}`
	// landProbe placed OUTSIDE both outer rings (in the bbox's NW
	// corner) so the per-polygon land-probe filter doesn't drop them —
	// this test pins inner-ring assignment, not the land filter.
	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{-5, -5, 15, 15}, LandProbes{Points: [][2]float64{{-3, -3}}})
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
	if len(parsed.Coordinates) != 2 {
		t.Fatalf("expected 2 polygons, got %d", len(parsed.Coordinates))
	}
	// Outer A is listed first → it receives the inner.
	if got := len(parsed.Coordinates[0]); got != 2 {
		t.Errorf("outer A: rings = %d, want 2 (outer + inner)", got)
	}
	// Outer B receives no inner because first-match-wins consumed it.
	if got := len(parsed.Coordinates[1]); got != 1 {
		t.Errorf("outer B: rings = %d, want 1 (outer only)", got)
	}
}

// TestStitchRings_ManySegments verifies stitchRings handles a
// real-OSM-scale relation whose outer boundary is fragmented across
// many short member ways and presented out of order. Real Boston
// harbor relations have 20+ outer members per ring; the synthetic
// 3-segment tests don't exercise the O(n²) extension loop or scaled
// chain construction.
func TestStitchRings_ManySegments(t *testing.T) {
	const n = 24
	// Walk a unit-square perimeter in n equal steps so vertices are
	// rationals that compare with exact float equality.
	verts := make([][2]float64, n+1)
	for i := 0; i <= n; i++ {
		f := float64(i) / float64(n)
		switch {
		case f < 0.25:
			verts[i] = [2]float64{4 * f, 0}
		case f < 0.5:
			verts[i] = [2]float64{1, 4 * (f - 0.25)}
		case f < 0.75:
			verts[i] = [2]float64{1 - 4*(f-0.5), 1}
		default:
			verts[i] = [2]float64{0, 1 - 4*(f-0.75)}
		}
	}
	verts[n] = verts[0]
	ordered := make([]stitchInput, n)
	for i := range n {
		ordered[i] = stitchInput{id: int64(100 + i), coords: [][2]float64{verts[i], verts[i+1]}}
	}
	// Permute via (i*7+3) mod n — coprime with n so it's a bijection,
	// and 7 is far enough from n that adjacent segments never end up
	// adjacent in the permutation.
	ways := make([]stitchInput, n)
	for i := range n {
		ways[i] = ordered[(i*7+3)%n]
	}
	rings, dropped := stitchRings(ways)
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want empty", dropped)
	}
	if len(rings) != 1 {
		t.Fatalf("rings = %d, want 1", len(rings))
	}
	if len(rings[0]) != n+1 {
		t.Errorf("ring length = %d, want %d", len(rings[0]), n+1)
	}
	if !isClosedRing(rings[0]) {
		t.Errorf("ring not closed: first=%v last=%v", rings[0][0], rings[0][len(rings[0])-1])
	}
	if !ringsEquivalent(rings[0], verts) {
		t.Errorf("stitched ring not equivalent to source square: got %v", rings[0])
	}
}

// TestCloseCoastlineChain_EastToWestPicksNorthSide: a coastline running
// east→west at y=0.5 places water on the right of forward direction = NORTH.
// The closing direction is derived from the coastline orientation (the
// right-hand rule), so the result is the north strip (the water side).
// Blindly walking CW from tail to head would produce the south strip (the
// LAND side), which then gets subtracted from the city boundary and erases
// real city area.
func TestCloseCoastlineChain_EastToWestPicksNorthSide(t *testing.T) {
	bbox := [4]float64{0, 0, 1, 1}
	chain := [][2]float64{{1, 0.5}, {0, 0.5}}
	// East-to-west → water-on-right is north. Land is south; probe south.
	rings := closeCoastlineChain(context.Background(), chain, bbox, newLandProbeIndex([][2]float64{{0.5, 0.25}}))
	if len(rings) != 1 {
		t.Fatalf("expected 1 ring, got %d", len(rings))
	}
	// CCW from (0, 0.5) back to (1, 0.5) visits NW (0,1), NE (1,1).
	want := [][2]float64{{1, 0.5}, {0, 0.5}, {0, 1}, {1, 1}, {1, 0.5}}
	if !reflect.DeepEqual(rings[0], want) {
		t.Errorf("ring = %v, want %v", rings[0], want)
	}
}

func TestAcceptWaterPolygon(t *testing.T) {
	// Use bbox area = 100 so a fraction = area/100.
	const bboxArea = 100.0
	cases := []struct {
		name      string
		outer     [][2]float64
		wantOK    bool
		wantInMsg string // substring of reason when wantOK is false
	}{
		{
			name:   "small CW polygon",
			outer:  [][2]float64{{0, 0}, {0, 1}, {1, 1}, {1, 0}, {0, 0}},
			wantOK: true,
		},
		{
			name:   "small CCW polygon accepted (orientation not enforced here)",
			outer:  [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}},
			wantOK: true,
		},
		{
			name:      "ring not closed",
			outer:     [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}},
			wantOK:    false,
			wantInMsg: "not closed",
		},
		{
			name:      "outer covers entire bbox (100%)",
			outer:     [][2]float64{{0, 0}, {0, 10}, {10, 10}, {10, 0}, {0, 0}},
			wantOK:    false,
			wantInMsg: "covers",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, reason := acceptWaterPolygon(c.outer, bboxArea)
			if ok != c.wantOK {
				t.Errorf("ok = %v, want %v (reason=%q)", ok, c.wantOK, reason)
			}
			if !c.wantOK && !strings.Contains(reason, c.wantInMsg) {
				t.Errorf("reason = %q, want substring %q", reason, c.wantInMsg)
			}
		})
	}
}

func TestAcceptWaterPolygon_ZeroBboxAreaSkipsSizeCheck(t *testing.T) {
	// Defensive: if a caller passes bboxArea=0 (degenerate bbox or
	// missing-data sentinel), the area-fraction check is skipped rather
	// than dividing by zero.
	outer := [][2]float64{{0, 0}, {0, 100}, {100, 100}, {100, 0}, {0, 0}}
	ok, reason := acceptWaterPolygon(outer, 0)
	if !ok {
		t.Errorf("expected accept when bboxArea is 0; got reject reason=%q", reason)
	}
}

func TestBBoxWalkCCW(t *testing.T) {
	bbox := [4]float64{0, 0, 1, 1}
	cases := []struct {
		name string
		from [2]float64
		to   [2]float64
		want [][2]float64
	}{
		{
			name: "east edge to west edge CCW visits NE then NW",
			from: [2]float64{1, 0.5},
			to:   [2]float64{0, 0.5},
			want: [][2]float64{{1, 1}, {0, 1}},
		},
		{
			name: "west edge to east edge CCW visits SW then SE",
			from: [2]float64{0, 0.5},
			to:   [2]float64{1, 0.5},
			want: [][2]float64{{0, 0}, {1, 0}},
		},
		{
			name: "same edge short CCW step no corners",
			from: [2]float64{1, 0.3},
			to:   [2]float64{1, 0.7},
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bboxWalkCCW(c.from, c.to, bbox)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("bboxWalkCCW(%v, %v) = %v, want %v", c.from, c.to, got, c.want)
			}
		})
	}
}

// TestParseWaterResponse_RejectsOversizedRelation pins the
// per-polygon area guard at the relation construction site: an outer
// ring covering most of the bbox is rejected with a warn naming the
// relation id. Without this guard a single mis-stitched relation could
// reduce a city's paved_share denominator to a sliver.
func TestParseWaterResponse_RejectsOversizedRelation(t *testing.T) {
	body := `{"elements":[{
		"type":"relation","id":777,"tags":{"natural":"water","type":"multipolygon"},
		"members":[
			{"type":"way","ref":1,"role":"outer","geometry":[
				{"lat":0,"lon":0},{"lat":0,"lon":1},{"lat":1,"lon":1},{"lat":1,"lon":0},{"lat":0,"lon":0}
			]}
		]
	}]}`

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := logs.WithLogger(context.Background(), logger)

	// bbox is the same as the outer → 100% coverage, well over 80% limit.
	result, err := parseWaterResponse(ctx, []byte(body), [4]float64{0, 0, 1, 1}, LandProbes{Points: [][2]float64{{0.5, 0.5}}})
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result when only outer is rejected; got %q", result)
	}

	line := bytes.TrimSpace(buf.Bytes())
	if len(line) == 0 {
		t.Fatal("expected warn log line; got nothing")
	}
	var record struct {
		Msg      string `json:"msg"`
		Level    string `json:"level"`
		Relation int64  `json:"relation"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(line, &record); err != nil {
		t.Fatalf("parse log line: %v: %s", err, line)
	}
	if record.Level != "WARN" {
		t.Errorf("level = %q, want WARN", record.Level)
	}
	if record.Relation != 777 {
		t.Errorf("relation = %d, want 777", record.Relation)
	}
	if !strings.Contains(record.Reason, "covers") {
		t.Errorf("reason = %q, want it to mention coverage", record.Reason)
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

// TestPolygonsFromRelation_InnerFallsThroughRejectedOuter pins the
// solvent-streets-sqlo fix. Before the fix, an inner ring contained by
// a rejected outer was silently dropped along with the outer because
// the filter ran after inner-ring assignment. Now the outer-ring filter
// runs inside relationToPolygons before inner assignment, so the inner
// falls through to the next accepted outer that contains its first
// vertex.
//
// Geometry: bboxArea=100. Outer A is a 9×9 ring (area 81 > 80% bbox →
// REJECTED). Outer B is a 2×2 ring fully inside A (area 4 → ACCEPTED).
// Inner I sits inside B (and therefore inside A too). Old behavior: I
// is assigned to A first, A is rejected, I is lost. New behavior: A is
// rejected at filter time, B survives, I is assigned to B's holes.
func TestPolygonsFromRelation_InnerFallsThroughRejectedOuter(t *testing.T) {
	bbox := [4]float64{0, 0, 10, 10}
	bboxArea := bboxLonLatArea(bbox)
	ring := func(pts ...[2]float64) []overpassGeometryPoint {
		out := make([]overpassGeometryPoint, len(pts))
		for i, p := range pts {
			out[i] = overpassGeometryPoint{Lon: p[0], Lat: p[1]}
		}
		return out
	}
	outerA := ring(
		[2]float64{0.5, 0.5}, [2]float64{9.5, 0.5}, [2]float64{9.5, 9.5}, [2]float64{0.5, 9.5}, [2]float64{0.5, 0.5},
	)
	outerB := ring(
		[2]float64{2, 2}, [2]float64{4, 2}, [2]float64{4, 4}, [2]float64{2, 4}, [2]float64{2, 2},
	)
	innerI := ring(
		[2]float64{2.5, 2.5}, [2]float64{3, 2.5}, [2]float64{3, 3}, [2]float64{2.5, 3}, [2]float64{2.5, 2.5},
	)
	rel := overpassElement{
		Type: elementRelation, ID: 42,
		Members: []overpassRelationMember{
			{Type: elementWay, Ref: 1, Role: "outer", Geometry: outerA},
			{Type: elementWay, Ref: 2, Role: "outer", Geometry: outerB},
			{Type: elementWay, Ref: 3, Role: "inner", Geometry: innerI},
		},
	}

	polys := polygonsFromRelation(context.Background(), rel, bboxArea)
	if len(polys) != 1 {
		t.Fatalf("polys count: got %d, want 1 (outer B accepted)", len(polys))
	}
	if len(polys[0].holes) != 1 {
		t.Fatalf("holes on accepted outer: got %d, want 1 (inner I should fall through from rejected A to B)", len(polys[0].holes))
	}
	// Sanity: holes[0] starts at inner I's first vertex.
	if got := polys[0].holes[0][0]; got != [2]float64{2.5, 2.5} {
		t.Errorf("hole first vertex: got %v, want [2.5 2.5]", got)
	}
}

// TestFetchOSMWater_OversizedBodySplitsQuadrants: an oversized water response
// must reach fetchSplitting's quadrant splitting, not error out of
// postOverpass and hard-fail the ingest for a bbox that splitting handles.
func TestFetchOSMWater_OversizedBodySplitsQuadrants(t *testing.T) {
	prev := maxResponseBodyBytes
	maxResponseBodyBytes = 512
	t.Cleanup(func() { maxResponseBodyBytes = prev })

	// One small water square per quadrant, offset so they don't merge.
	quadrantBody := func(id int) string {
		lat := 42.36 + float64(id)*0.01
		return fmt.Sprintf(`{"elements":[{"type":"way","id":%d,"tags":{"natural":"water"},"geometry":[`+
			`{"lat":%[2]f,"lon":-71.06},{"lat":%[2]f,"lon":-71.05},`+
			`{"lat":%f,"lon":-71.05},{"lat":%[4]f,"lon":-71.06},`+
			`{"lat":%[2]f,"lon":-71.06}]}]}`, id, lat, lat+0.002, lat+0.002)
	}

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		if n == 1 {
			// Comfortably over the 512-byte cap set above.
			_, _ = w.Write([]byte(`{"elements":[],"padding":"` + strings.Repeat("x", 1024) + `"}`))
			return
		}
		_, _ = w.Write([]byte(quadrantBody(n)))
	}))
	t.Cleanup(srv.Close)

	result, err := fetchOSMWater(context.Background(), uaClient(srv), srv.URL,
		[4]float64{42.0, -72.0, 43.0, -71.0}, LandProbes{Points: [][2]float64{{-71.5, 42.5}}})
	if err != nil {
		t.Fatalf("fetchOSMWater: %v (an oversized body must split, not hard-fail)", err)
	}
	// 1 oversized request at depth 0 + one per quadrant at depth 1.
	if requests != 5 {
		t.Errorf("made %d requests, want 5 (1 oversized + 4 quadrants)", requests)
	}
	if !strings.HasPrefix(result, `{"type":"MultiPolygon"`) {
		t.Errorf("expected MultiPolygon GeoJSON assembled from the quadrants; got %q", result)
	}
}

// relationBody returns one natural=water multipolygon relation with a single
// outer member way, as an Overpass response body.
func relationBody(id int64, outer [][2]float64) string {
	pts := make([]string, len(outer))
	for i, c := range outer {
		pts[i] = fmt.Sprintf(`{"lat":%g,"lon":%g}`, c[1], c[0])
	}
	return fmt.Sprintf(`{"elements":[{"type":"relation","id":%d,"tags":{"natural":"water","type":"multipolygon"},`+
		`"members":[{"type":"way","ref":1,"role":"outer","geometry":[%s]}]}]}`, id, strings.Join(pts, ","))
}

// bigLake is a lake relation's outer ring 16 times the [0,1]² city bbox,
// covering the city's east part (lon > 0.6) and reaching far past it.
var bigLake = [][2]float64{{0.6, -1.5}, {4.6, -1.5}, {4.6, 2.5}, {0.6, 2.5}, {0.6, -1.5}}

// shoreRoads are dense road probes on the city's land, west of the lake.
func shoreRoads(extra ...[2]float64) LandProbes {
	var pts [][2]float64
	for i := range 10 {
		for j := range 10 {
			pts = append(pts, [2]float64{0.05 + 0.05*float64(i), 0.05 + 0.09*float64(j)})
		}
	}
	pts = append(pts, extra...)
	return LandProbes{Points: pts, MaxHits: waterLandHitAllowance(len(pts)), Dense: true}
}

// TestAssembleWater_LakeLargerThanBBoxAcceptedWithRoadProbes pins
// solvent-streets-z3si: a lake far larger than the city's bbox (Lake
// Champlain is 15 times Burlington's) is real water when none of the
// city's roads lie in it, so with road probes the bbox-area cap does not
// apply, and the subtract removes the lake's part of the city.
func TestAssembleWater_LakeLargerThanBBoxAcceptedWithRoadProbes(t *testing.T) {
	ctx := context.Background()
	bbox := [4]float64{0, 0, 1, 1}
	for name, body := range map[string]string{"relation": relationBody(132216, bigLake), "way": closedWayBody(9, bigLake)} {
		t.Run(name, func(t *testing.T) {
			water, err := parseWaterResponse(ctx, []byte(body), bbox, shoreRoads())
			if err != nil {
				t.Fatal(err)
			}
			if water == "" {
				t.Fatal("lake rejected; want it accepted with no road probe in it")
			}
			city := `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}`
			stripped, err := geo.SubtractGeoJSON(ctx, city, water)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := geo.BoundaryArea(ctx, city)
			after, _ := geo.BoundaryArea(ctx, stripped)
			if r := after / before; math.Abs(r-0.6) > 0.02 {
				t.Errorf("stripped/original area = %.3f, want about 0.6 (the lake's 40%% removed)", r)
			}
		})
	}
}

// TestAssembleWater_LakeLargerThanBBoxRejectedWithInteriorProbes: with the
// sparse interior-point probes (a city with no roads) the cap still
// applies, as before road probes existed.
func TestAssembleWater_LakeLargerThanBBoxRejectedWithInteriorProbes(t *testing.T) {
	var buf bytes.Buffer
	ctx := logs.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	probes, err := InteriorLandProbes(`{"type":"Polygon","coordinates":[[[0,0],[0.5,0],[0.5,1],[0,1],[0,0]]]}`)
	if err != nil {
		t.Fatal(err)
	}
	water, err := parseWaterResponse(ctx, []byte(relationBody(132216, bigLake)), [4]float64{0, 0, 1, 1}, probes)
	if err != nil {
		t.Fatal(err)
	}
	if water != "" || !strings.Contains(buf.String(), "covers") {
		t.Errorf("water = %q, log = %s; want the lake rejected by the bbox-area cap", water, buf.String())
	}
}

// TestAssembleWater_LakeHoldingRoadsDropped: the cap's job, catching a
// mis-tagged polygon that covers land, falls to the road probes: a "lake"
// holding more of the city's roads than the allowance is dropped as
// containing city land.
func TestAssembleWater_LakeHoldingRoadsDropped(t *testing.T) {
	var buf bytes.Buffer
	ctx := logs.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	var inLake [][2]float64
	for i := range 10 {
		inLake = append(inLake, [2]float64{0.7 + 0.02*float64(i), 0.5})
	}
	water, err := parseWaterResponse(ctx, []byte(relationBody(132216, bigLake)), [4]float64{0, 0, 1, 1}, shoreRoads(inLake...))
	if err != nil {
		t.Fatal(err)
	}
	if water != "" || !strings.Contains(buf.String(), "containing city land") {
		t.Errorf("water = %q, log = %s; want the lake dropped as containing city land", water, buf.String())
	}
}

// coastlineBody returns natural=coastline ways, one per coordinate list, as
// an Overpass response body.
func coastlineBody(ways ...[][2]float64) string {
	els := make([]string, len(ways))
	for i, w := range ways {
		pts := make([]string, len(w))
		for j, c := range w {
			pts[j] = fmt.Sprintf(`{"lat":%g,"lon":%g}`, c[1], c[0])
		}
		els[i] = fmt.Sprintf(`{"type":"way","id":%d,"tags":{"natural":"coastline"},"geometry":[%s]}`, 500+i, strings.Join(pts, ","))
	}
	return `{"elements":[` + strings.Join(els, ",") + `]}`
}

// islandRing is a CCW closed coastline ring around [0.3,0.7]²: an island,
// with the water on the right of the walk, outside.
var islandRing = [][2]float64{{0.3, 0.3}, {0.7, 0.3}, {0.7, 0.7}, {0.3, 0.7}, {0.3, 0.3}}

// gridProbes returns dense probes on an n×n grid over [x0,x1]×[y0,y1].
func gridProbes(n int, x0, y0, x1, y1 float64) [][2]float64 {
	var pts [][2]float64
	for i := range n {
		for j := range n {
			pts = append(pts, [2]float64{x0 + (x1-x0)*(float64(i)+0.5)/float64(n), y0 + (y1-y0)*(float64(j)+0.5)/float64(n)})
		}
	}
	return pts
}

func denseProbes(pts [][2]float64) LandProbes {
	return LandProbes{Points: pts, MaxHits: waterLandHitAllowance(len(pts)), Dense: true}
}

// TestAssembleWater_IslandCityStripsSurroundingSea pins solvent-streets-gu2k
// (Alameda): when the only coastline in the bbox is an island's closed CCW
// ring, everything in the bbox off the island is sea. With road probes on
// the island, the sea around it is water, so the boundary is stripped to
// the island.
func TestAssembleWater_IslandCityStripsSurroundingSea(t *testing.T) {
	ctx := context.Background()
	bbox := [4]float64{0, 0, 1, 1}
	boundary := `{"type":"Polygon","coordinates":[[[0.1,0.1],[0.9,0.1],[0.9,0.9],[0.1,0.9],[0.1,0.1]]]}`
	body := coastlineBody(islandRing)
	water, err := parseWaterResponse(ctx, []byte(body), bbox, denseProbes(gridProbes(10, 0.32, 0.32, 0.68, 0.68)))
	if err != nil {
		t.Fatal(err)
	}
	if water == "" {
		t.Fatal("no water; want the sea around the island")
	}
	stripped, err := geo.SubtractGeoJSON(ctx, boundary, water)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := geo.BoundaryArea(ctx, boundary)
	after, _ := geo.BoundaryArea(ctx, stripped)
	if r := after / before; math.Abs(r-0.25) > 0.01 {
		t.Errorf("stripped/original area = %.3f, want 0.25: the boundary cut to the island", r)
	}

	// The sparse interior-point fallback keeps today's rule: the island is
	// dropped and nothing is stripped.
	sparse, err := InteriorLandProbes(boundary)
	if err != nil {
		t.Fatal(err)
	}
	if water, err := parseWaterResponse(ctx, []byte(body), bbox, sparse); err != nil || water != "" {
		t.Errorf("sparse probes: water = %q, err = %v; want none", water, err)
	}
}

// TestAssembleWater_SeaAroundIslandsHoldingRoadsDropped: when the city's
// roads also lie off the island (the coastline is not the whole story, say
// an unmapped mainland shore), the sea face holds city land and is dropped,
// leaving the boundary unchanged.
func TestAssembleWater_SeaAroundIslandsHoldingRoadsDropped(t *testing.T) {
	var buf bytes.Buffer
	ctx := logs.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	pts := append(gridProbes(10, 0.32, 0.32, 0.68, 0.68), gridProbes(3, 0.1, 0.75, 0.25, 0.9)...)
	water, err := parseWaterResponse(ctx, []byte(coastlineBody(islandRing)), [4]float64{0, 0, 1, 1}, denseProbes(pts))
	if err != nil {
		t.Fatal(err)
	}
	if water != "" || !strings.Contains(buf.String(), "dropped sea around islands containing city land") {
		t.Errorf("water = %q, log = %s; want the sea face dropped", water, buf.String())
	}
}

// TestAssembleWater_IslandInSeaIsAHole: an island inside the sea an open
// coastline chain closes is a hole in it, so the island's roads do not make
// the sea read as land, and the subtract keeps the island.
func TestAssembleWater_IslandInSeaIsAHole(t *testing.T) {
	ctx := context.Background()
	// The mainland coast runs north along x = 0.25, water on its right
	// (east). The island [0.5,0.7]×[0.4,0.6] sits in that sea.
	coast := [][2]float64{{0.25, -0.5}, {0.25, 1.5}}
	island := [][2]float64{{0.5, 0.4}, {0.7, 0.4}, {0.7, 0.6}, {0.5, 0.6}, {0.5, 0.4}}
	pts := append(gridProbes(10, 0.02, 0.02, 0.23, 0.98), gridProbes(5, 0.52, 0.42, 0.68, 0.58)...)
	water, err := parseWaterResponse(ctx, []byte(coastlineBody(coast, island)), [4]float64{0, 0, 1, 1}, denseProbes(pts))
	if err != nil {
		t.Fatal(err)
	}
	if water == "" {
		t.Fatal("no water; want the sea east of the coast, with the island as a hole")
	}
	boundary := `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}`
	stripped, err := geo.SubtractGeoJSON(ctx, boundary, water)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := geo.BoundaryArea(ctx, boundary)
	after, _ := geo.BoundaryArea(ctx, stripped)
	if r := after / before; math.Abs(r-0.29) > 0.01 {
		t.Errorf("stripped/original area = %.3f, want 0.29: the mainland strip and the island", r)
	}
}

// TestParseWaterResponse_AcceptsLargeCoastlineRingWithRoadProbes is the
// road-probe twin of the Foster City guard (bd-86aa): the coastline path
// stays uncapped with dense probes, and roads in the small land triangle
// pick the large water side.
func TestParseWaterResponse_AcceptsLargeCoastlineRingWithRoadProbes(t *testing.T) {
	body := coastlineBody([][2]float64{{2, 0}, {1, 1}, {0, 2}})
	var pts [][2]float64
	for _, p := range gridProbes(10, 0, 0, 1, 1) {
		if p[0]+p[1] < 1.9 { // inside the SW land triangle
			pts = append(pts, p)
		}
	}
	result, err := parseWaterResponse(context.Background(), []byte(body), [4]float64{0, 0, 10, 10}, denseProbes(pts))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil || len(parsed.Coordinates) != 1 {
		t.Fatalf("result = %s (err %v); want one water polygon", result, err)
	}
	if frac := math.Abs(ringSignedArea(parsed.Coordinates[0][0])) / 100; frac <= maxOuterBboxAreaFraction {
		t.Errorf("accepted ring covers %.3f of the bbox; want the large water side", frac)
	}
}

// TestAssembleWater_NoSeaFaceBesideAnOpenCoastline: the bbox-less-islands
// sea is only for a bbox no open coastline crosses. Here a mainland coast
// does, and the sea it closes (with the island as a hole) is the water;
// the bbox-wide face would also claim the mainland, so it must not be
// added.
func TestAssembleWater_NoSeaFaceBesideAnOpenCoastline(t *testing.T) {
	coast := [][2]float64{{0.7, -0.5}, {0.7, 1.5}}
	island := [][2]float64{{0.8, 0.4}, {0.9, 0.4}, {0.9, 0.6}, {0.8, 0.6}, {0.8, 0.4}}
	water, err := parseWaterResponse(context.Background(), []byte(coastlineBody(coast, island)), [4]float64{0, 0, 1, 1},
		denseProbes(gridProbes(5, 0.82, 0.42, 0.88, 0.58)))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(water), &parsed); err != nil {
		t.Fatalf("parse %q: %v", water, err)
	}
	if len(parsed.Coordinates) != 1 || len(parsed.Coordinates[0]) != 2 {
		t.Fatalf("water = %s; want one polygon, the sea east of the coast with the island as its hole", water)
	}
	if pointInRing([2]float64{0.3, 0.5}, parsed.Coordinates[0][0]) {
		t.Errorf("the water %v claims the mainland", parsed.Coordinates[0][0])
	}
}

// TestCloseCoastlineChain_FewerHitsBeatsSmallerArea: when both closures of
// an open chain are within the dense allowance, the one holding fewer
// probes is the water, even when it is the larger.
func TestCloseCoastlineChain_FewerHitsBeatsSmallerArea(t *testing.T) {
	chain := [][2]float64{{0.3, 0}, {0.3, 1}}
	pts := append(gridProbes(10, 2, 2, 3, 3), [2]float64{0.1, 0.5}, [2]float64{0.2, 0.5}) // two probes west of the chain
	idx := newLandProbeIndexFor(LandProbes{Points: pts, MaxHits: 3, Dense: true})
	rings := closeCoastlineChain(context.Background(), chain, [4]float64{0, 0, 1, 1}, idx)
	if len(rings) != 1 || !pointInRing([2]float64{0.8, 0.5}, rings[0]) {
		t.Errorf("rings = %v; want the larger east closure, which holds no probe", rings)
	}
}
