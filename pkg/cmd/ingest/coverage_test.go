package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
)

// road builds a minimal road feature whose first coord is (lon, lat).
// Only fields the gate inspects are populated.
func road(id string, lon, lat float64) db.Feature {
	return db.Feature{
		ID:           id,
		GeometryJSON: fmt.Sprintf(`{"type":"LineString","coordinates":[[%v,%v],[%v,%v]]}`, lon, lat, lon+0.001, lat+0.001),
	}
}

// unitSquareBoundary returns a GeoJSON Polygon that's the [0,0]→[1,1] square.
// Handy for tests that don't need a real city's shape.
const unitSquareBoundary = `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}`

// shiftedSquareBoundary is the [10,10]→[11,11] square — disjoint from
// the unit square so a road set built for [0,1]² lands entirely outside.
const shiftedSquareBoundary = `{"type":"Polygon","coordinates":[[[10,10],[11,10],[11,11],[10,11],[10,10]]]}`

// bigSquareBoundary is the [0,2]² Nominatim shape (B) of the
// inversion-recovery fixtures.
const bigSquareBoundary = `{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,2],[0,2],[0,0]]]}`

func TestValidateBoundaryAgainstRoads_AllInside(t *testing.T) {
	roads := []db.Feature{
		road("a", 0.2, 0.2),
		road("b", 0.5, 0.5),
		road("c", 0.8, 0.8),
	}
	ratio, ok := validateBoundaryAgainstRoads(unitSquareBoundary, roads)
	if !ok {
		t.Fatal("expected ok=true with non-empty roads and valid boundary")
	}
	if ratio != 1.0 {
		t.Errorf("expected ratio 1.0, got %.3f", ratio)
	}
}

func TestValidateBoundaryAgainstRoads_AllOutside(t *testing.T) {
	roads := []db.Feature{
		road("a", 0.2, 0.2),
		road("b", 0.5, 0.5),
		road("c", 0.8, 0.8),
	}
	ratio, ok := validateBoundaryAgainstRoads(shiftedSquareBoundary, roads)
	if !ok {
		t.Fatal("expected ok=true with non-empty roads and valid boundary")
	}
	if ratio != 0 {
		t.Errorf("expected ratio 0, got %.3f", ratio)
	}
}

func TestValidateBoundaryAgainstRoads_NoRoadsReturnsNotOK(t *testing.T) {
	if _, ok := validateBoundaryAgainstRoads(unitSquareBoundary, nil); ok {
		t.Error("expected ok=false with no roads")
	}
	if _, ok := validateBoundaryAgainstRoads(unitSquareBoundary, []db.Feature{}); ok {
		t.Error("expected ok=false with empty roads")
	}
}

func TestValidateBoundaryAgainstRoads_UnparseableBoundary(t *testing.T) {
	if _, ok := validateBoundaryAgainstRoads("not json", []db.Feature{road("a", 0, 0)}); ok {
		t.Error("expected ok=false with unparseable boundary")
	}
	// A non-polygonal GeoJSON type (Point) is also a "can't gate" case.
	if _, ok := validateBoundaryAgainstRoads(`{"type":"Point","coordinates":[0,0]}`, []db.Feature{road("a", 0, 0)}); ok {
		t.Error("expected ok=false with non-polygonal boundary type")
	}
}

func TestValidateBoundaryAgainstRoads_MultiPolygon(t *testing.T) {
	// Two disjoint squares: [0,1]² and [10,11]². Roads land in both.
	multi := `{"type":"MultiPolygon","coordinates":[` +
		`[[[0,0],[1,0],[1,1],[0,1],[0,0]]],` +
		`[[[10,10],[11,10],[11,11],[10,11],[10,10]]]]}`
	roads := []db.Feature{
		road("a", 0.5, 0.5),   // inside sub-polygon 1
		road("b", 10.5, 10.5), // inside sub-polygon 2
		road("c", 5, 5),       // outside both
		road("d", 100, 100),   // way outside
	}
	ratio, ok := validateBoundaryAgainstRoads(multi, roads)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ratio != 0.5 {
		t.Errorf("expected ratio 0.5 (2/4 inside), got %.3f", ratio)
	}
}

func TestValidateBoundaryAgainstRoads_HoleExcludesPoint(t *testing.T) {
	// Outer square [0,4]² with hole [1,3]². A road inside the hole is NOT
	// inside the polygon. A road inside the outer but outside the hole IS.
	withHole := `{"type":"Polygon","coordinates":[` +
		`[[0,0],[4,0],[4,4],[0,4],[0,0]],` +
		`[[1,1],[3,1],[3,3],[1,3],[1,1]]]}`
	roads := []db.Feature{
		road("inside-hole", 2, 2),      // in the hole → outside
		road("outside-hole", 0.5, 0.5), // outer, not hole → inside
	}
	ratio, ok := validateBoundaryAgainstRoads(withHole, roads)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ratio != 0.5 {
		t.Errorf("expected ratio 0.5 (1 of 2 inside polygon, hole excludes the other), got %.3f", ratio)
	}
}

// chooseBoundary tests below. chooseBoundary measures every candidate
// against the roads touching the stripped boundary's bbox (the roads a fetch
// over that bbox returns, which the 0.15 threshold is calibrated on), so each
// fixture keeps the roads it is about inside that bbox.

func TestChooseBoundary_StripPasses_KeepsBoundary(t *testing.T) {
	// All 10 roads inside the stripped boundary → ratio 1.0 ≥ 0.15.
	roads := buildRoadsInUnitSquare(10)
	gjson, source, note, err := chooseBoundary(context.Background(), "Working City",
		shiftedSquareBoundary, unitSquareBoundary, "nominatim+osm-water", "nominatim", roads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson != unitSquareBoundary || source != "nominatim+osm-water" {
		t.Errorf("chose %s (source=%s), want the stripped boundary kept", gjson, source)
	}
	if note != "" {
		t.Errorf("expected no note on a passing strip, got %q", note)
	}
}

// invertedStripBoundary models an inverted strip of bigSquareBoundary: the
// right half plus a thin strip along the top (B-water came back as the
// water), so its bbox is all of B while the roads in the lower left lie
// outside it, and its complement B-strip = [0,1]×[0,1.9] is the land.
const invertedStripBoundary = `{"type":"Polygon","coordinates":[[[1,0],[2,0],[2,2],[0,2],[0,1.9],[1,1.9],[1,0]]]}`

func TestChooseBoundary_StripInverted_RecoversViaComplement(t *testing.T) {
	// The complement recovers the land where the roads live, so it is
	// chosen — NOT the water-inclusive unstripped Nominatim.
	roads := []db.Feature{road("a", 0.3, 0.3), road("b", 0.5, 1.0), road("c", 0.2, 1.7)}
	gjson, source, note, err := chooseBoundary(context.Background(), "San Francisco, CA",
		bigSquareBoundary, invertedStripBoundary, "nominatim+osm-water", "nominatim", roads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != "nominatim+osm-water-inverted" {
		t.Errorf("expected complement source %q, got %q", "nominatim+osm-water-inverted", source)
	}
	// The chosen complement must actually contain the roads (it's the land).
	if ratio, ok := validateBoundaryAgainstRoads(gjson, roads); !ok || ratio < stripCoverageMinRatio {
		t.Errorf("expected the complement to cover the roads, got ratio=%.3f ok=%v", ratio, ok)
	}
	// docs/troubleshooting.md quotes the note's opening for users to search
	// for; it names the city and the source it saved instead.
	if !strings.HasPrefix(note, "Water-strip inversion recovered for San Francisco, CA") || !strings.Contains(note, source) {
		t.Errorf("expected recovery note naming the city and the saved source, got: %q", note)
	}
}

func TestChooseBoundary_ComplementAlsoFails_RollsBackToNominatim(t *testing.T) {
	// Two squares make the unstripped boundary; the stripped one keeps a
	// sliver of each, so its bbox spans the gap between them. Of the 10
	// roads in that bbox, 8 are in the gap (neighbors), one is in the
	// stripped shape and one in its complement — both below 15% — but the
	// unstripped boundary holds both (20% ≥ 15%), so the gate falls back
	// to it.
	twoSquares := `{"type":"MultiPolygon","coordinates":[` +
		`[[[0,0],[1,0],[1,1],[0,1],[0,0]]],[[[9,0],[10,0],[10,1],[9,1],[9,0]]]]}`
	slivers := `{"type":"MultiPolygon","coordinates":[` +
		`[[[0.4,0.4],[0.6,0.4],[0.6,0.5],[0.4,0.5],[0.4,0.4]]],[[[9.4,0.4],[9.6,0.4],[9.6,0.6],[9.4,0.6],[9.4,0.4]]]]}`
	roads := []db.Feature{road("in1", 0.5, 0.45), road("in2", 0.5, 0.55)}
	for i := range 8 {
		roads = append(roads, road(fmt.Sprintf("out%d", i), 5+0.1*float64(i), 0.5))
	}
	gjson, source, note, err := chooseBoundary(context.Background(), "Brisbane, CA",
		twoSquares, slivers, "nominatim+osm-water", "nominatim", roads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if source != "nominatim" || gjson != twoSquares {
		t.Errorf("chose source %q, want the unstripped Nominatim (%q)", source, "nominatim")
	}
	// docs/troubleshooting.md quotes this opening too.
	if !strings.HasPrefix(note, "Water-strip rollback for Brisbane, CA") || !strings.Contains(note, "source="+source) {
		t.Errorf("expected rollback note naming the city and the saved source, got: %q", note)
	}
}

func TestChooseBoundary_BothFail_ReturnsHardError(t *testing.T) {
	// The unstripped boundary is a triangle and the stripped one the same
	// triangle less a corner square; every road in their bbox lies outside
	// the triangle → both gate-fail → hard error with the inversion
	// sentinel.
	triangle := `{"type":"Polygon","coordinates":[[[10,10],[11,10],[10,11],[10,10]]]}`
	notched := `{"type":"Polygon","coordinates":[[[10.1,10],[11,10],[10,11],[10,10.1],[10.1,10.1],[10.1,10]]]}`
	var roads []db.Feature
	for i := range 10 {
		roads = append(roads, road(fmt.Sprintf("r%d", i), 10.7+0.02*float64(i), 10.8))
	}
	_, _, _, err := chooseBoundary(context.Background(), "Nowhere, USA",
		triangle, notched, "nominatim+osm-water", "nominatim", roads)
	if !errors.Is(err, ErrBoundaryInvertedVsRoads) {
		t.Fatalf("expected errors.Is ErrBoundaryInvertedVsRoads, got %v", err)
	}
	if !strings.Contains(err.Error(), "Nowhere, USA") {
		t.Errorf("expected error to name the city, got: %v", err)
	}
}

func TestChooseBoundary_NoRoads_KeepsStripped(t *testing.T) {
	// Empty roads → gate abstains: the stripped boundary, no note, no error.
	gjson, source, note, err := chooseBoundary(context.Background(), "Test",
		bigSquareBoundary, unitSquareBoundary, "nominatim+osm-water", "nominatim", nil)
	if err != nil || gjson != unitSquareBoundary || source != "nominatim+osm-water" || note != "" {
		t.Errorf("chooseBoundary = (%s, %q, %q, %v), want the stripped boundary unchanged", gjson, source, note, err)
	}
}

func buildRoadsInUnitSquare(n int) []db.Feature {
	// n roads scattered across the unit square.
	out := make([]db.Feature, n)
	for i := range n {
		t := float64(i+1) / float64(n+1)
		out[i] = road(fmt.Sprintf("r%d", i), t, t)
	}
	return out
}
