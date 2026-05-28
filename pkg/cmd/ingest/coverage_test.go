package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
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

// Fixtures for the inversion-recovery path. bigSquareBoundary is the
// [0,2]² Nominatim shape (B). rightHalfBoundary is the [1,2]×[0,2]
// subset that models an inverted strip (B-water came back as the
// water/right half), so its complement B-rightHalf = the [0,1]×[0,2]
// left half recovers the land where the roads live.
const bigSquareBoundary = `{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,2],[0,2],[0,0]]]}`
const rightHalfBoundary = `{"type":"Polygon","coordinates":[[[1,0],[2,0],[2,2],[1,2],[1,0]]]}`

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

// applyRoadCoverageGate orchestration tests below.

func TestApplyRoadCoverageGate_StripPasses_KeepsBoundary(t *testing.T) {
	// All 10 roads inside the stripped boundary → ratio 1.0 ≥ 0.15.
	// No SaveBoundary call expected.
	var saveCalls []saveCall
	store := &dbtest.MockStore{
		SaveBoundaryFunc: func(_ context.Context, gjson, src string) error {
			saveCalls = append(saveCalls, saveCall{gjson: gjson, source: src})
			return nil
		},
	}
	roads := buildRoadsInUnitSquare(10)
	fresh := &freshBoundary{
		Nominatim:        shiftedSquareBoundary, // would-be rollback target (different)
		SavedSource:      "nominatim+osm-water",
		UnstrippedSource: "nominatim",
	}
	var stderr bytes.Buffer
	err := applyRoadCoverageGate(context.Background(), store, "Working City", unitSquareBoundary, fresh, roads, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(saveCalls) != 0 {
		t.Errorf("expected no SaveBoundary call on passing strip, got %d", len(saveCalls))
	}
	if stderr.Len() != 0 {
		t.Errorf("expected no stderr output on passing strip, got %q", stderr.String())
	}
}

func TestApplyRoadCoverageGate_StripInverted_RecoversViaComplement(t *testing.T) {
	// Inverted strip: the stored boundary is the [1,2]×[0,2] right half
	// (the "water" the inversion left behind) and contains no roads. The
	// complement B-(right half) = the left half recovers the land where
	// the roads live, so the gate saves the complement — NOT the
	// water-inclusive unstripped Nominatim. solvent-streets-e5mk.
	var saveCalls []saveCall
	store := &dbtest.MockStore{
		SaveBoundaryFunc: func(_ context.Context, gjson, src string) error {
			saveCalls = append(saveCalls, saveCall{gjson: gjson, source: src})
			return nil
		},
	}
	// Roads in the left half [0,1]×[0,2]: outside the inverted strip,
	// inside the complement.
	roads := []db.Feature{road("a", 0.3, 0.3), road("b", 0.5, 1.0), road("c", 0.2, 1.7)}
	fresh := &freshBoundary{
		Nominatim:        bigSquareBoundary,
		SavedSource:      "nominatim+osm-water",
		UnstrippedSource: "nominatim",
	}
	var stderr bytes.Buffer
	err := applyRoadCoverageGate(context.Background(), store, "San Francisco, CA", rightHalfBoundary, fresh, roads, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(saveCalls) != 1 {
		t.Fatalf("expected one SaveBoundary call (complement), got %d", len(saveCalls))
	}
	if saveCalls[0].source != "nominatim+osm-water-inverted" {
		t.Errorf("expected complement source %q, got %q", "nominatim+osm-water-inverted", saveCalls[0].source)
	}
	// The saved complement must actually contain the roads (it's the land).
	if ratio, ok := validateBoundaryAgainstRoads(saveCalls[0].gjson, roads); !ok || ratio < stripCoverageMinRatio {
		t.Errorf("expected saved complement to cover the roads, got ratio=%.3f ok=%v", ratio, ok)
	}
	if !strings.Contains(stderr.String(), "San Francisco, CA") || !strings.Contains(stderr.String(), "complement") {
		t.Errorf("expected recovery notice naming the city and complement, got: %q", stderr.String())
	}
}

func TestApplyRoadCoverageGate_ComplementAlsoFails_RollsBackToNominatim(t *testing.T) {
	// Only 2 of 10 bbox roads are in-city (the other 8 are neighbors).
	// The inverted strip (left cell) holds 1, its complement (right cell)
	// holds the other 1 — both below 15% — but the full Nominatim holds
	// both (20% ≥ 15%), so the gate falls back to the unstripped shape.
	var saveCalls []saveCall
	store := &dbtest.MockStore{
		SaveBoundaryFunc: func(_ context.Context, gjson, src string) error {
			saveCalls = append(saveCalls, saveCall{gjson: gjson, source: src})
			return nil
		},
	}
	wideBoundary := `{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,1],[0,1],[0,0]]]}`
	leftCell := `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1],[0,0]]]}`
	roads := []db.Feature{road("in1", 0.5, 0.5), road("in2", 1.5, 0.5)}
	for i := range 8 {
		roads = append(roads, road(fmt.Sprintf("out%d", i), float64(100+i), 100))
	}
	fresh := &freshBoundary{
		Nominatim:        wideBoundary,
		SavedSource:      "nominatim+osm-water",
		UnstrippedSource: "nominatim",
	}
	var stderr bytes.Buffer
	err := applyRoadCoverageGate(context.Background(), store, "Brisbane, CA", leftCell, fresh, roads, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(saveCalls) != 1 {
		t.Fatalf("expected one SaveBoundary call (rollback), got %d", len(saveCalls))
	}
	if saveCalls[0].source != "nominatim" {
		t.Errorf("expected rollback source %q, got %q", "nominatim", saveCalls[0].source)
	}
	if saveCalls[0].gjson != wideBoundary {
		t.Errorf("expected rollback boundary to be the unstripped Nominatim")
	}
	if !strings.Contains(stderr.String(), "Brisbane, CA") {
		t.Errorf("expected rollback warning to name the city, got: %q", stderr.String())
	}
}

func TestApplyRoadCoverageGate_BothFail_ReturnsHardError(t *testing.T) {
	// Both stripped and Nominatim are shifted away from where the roads
	// live → both gate-fail → hard error with the inversion sentinel.
	saveCalled := false
	store := &dbtest.MockStore{
		SaveBoundaryFunc: func(_ context.Context, _, _ string) error {
			saveCalled = true
			return nil
		},
	}
	roads := buildRoadsInUnitSquare(10)
	fresh := &freshBoundary{
		Nominatim:        shiftedSquareBoundary, // also wrong
		SavedSource:      "nominatim+osm-water",
		UnstrippedSource: "nominatim",
	}
	var stderr bytes.Buffer
	err := applyRoadCoverageGate(context.Background(), store, "Nowhere, USA", shiftedSquareBoundary, fresh, roads, &stderr)
	if err == nil {
		t.Fatal("expected ErrBoundaryInvertedVsRoads")
	}
	if !errors.Is(err, ErrBoundaryInvertedVsRoads) {
		t.Errorf("expected errors.Is ErrBoundaryInvertedVsRoads, got %v", err)
	}
	if !strings.Contains(err.Error(), "Nowhere, USA") {
		t.Errorf("expected error to name the city, got: %v", err)
	}
	if saveCalled {
		t.Errorf("expected no SaveBoundary call when rollback also fails")
	}
}

func TestApplyRoadCoverageGate_NoRoads_NoOp(t *testing.T) {
	// Empty roads → gate abstains, no save, no error.
	saveCalled := false
	store := &dbtest.MockStore{
		SaveBoundaryFunc: func(_ context.Context, _, _ string) error {
			saveCalled = true
			return nil
		},
	}
	fresh := &freshBoundary{
		Nominatim:        unitSquareBoundary,
		SavedSource:      "nominatim+osm-water",
		UnstrippedSource: "nominatim",
	}
	var stderr bytes.Buffer
	err := applyRoadCoverageGate(context.Background(), store, "Test", unitSquareBoundary, fresh, nil, &stderr)
	if err != nil {
		t.Errorf("expected no error with no roads, got %v", err)
	}
	if saveCalled {
		t.Errorf("expected no SaveBoundary call when no roads available")
	}
}

func TestRoadsForCoverageGate_UsesJustIngestedForRoads(t *testing.T) {
	// When the run is for roads, the gate uses the freshly-fetched
	// features, not a DB call (avoids a round-trip through the store).
	listCalled := false
	store := &dbtest.MockStore{
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			listCalled = true
			return nil, nil
		},
	}
	justIngested := []db.Feature{road("a", 0, 0)}
	opts := &Options{ResourceType: &resource.Pavement{}}
	got, ok := roadsForCoverageGate(context.Background(), store, opts, justIngested)
	if !ok {
		t.Fatal("expected ok=true with non-empty just-ingested roads")
	}
	if len(got) != 1 {
		t.Errorf("expected 1 road, got %d", len(got))
	}
	if listCalled {
		t.Error("expected no DB roundtrip when run is for roads")
	}
}

func TestRoadsForCoverageGate_ReadsFromDBForNonRoads(t *testing.T) {
	// When the run is for parking, the gate looks up roads in the DB.
	want := []db.Feature{road("from-db", 0.5, 0.5)}
	store := &dbtest.MockStore{
		ListFeaturesFunc: func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
			if rt != resource.TypeRoads {
				t.Errorf("expected ListFeatures(\"roads\"), got %q", rt)
			}
			return want, nil
		},
	}
	opts := &Options{ResourceType: &resource.Parking{}}
	got, ok := roadsForCoverageGate(context.Background(), store, opts, nil)
	if !ok {
		t.Fatal("expected ok=true when DB has roads")
	}
	if len(got) != 1 || got[0].ID != "from-db" {
		t.Errorf("expected the DB roads, got %+v", got)
	}
}

func TestRoadsForCoverageGate_AbstainsWhenNoRoadsAvailable(t *testing.T) {
	// Parking ingest, but no roads in DB → gate abstains. This preserves
	// the per-resource workflow for users who haven't yet run roads
	// ingest on this city — they get current behavior, not a new error.
	store := &dbtest.MockStore{
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return nil, nil
		},
	}
	opts := &Options{ResourceType: &resource.Parking{}}
	if _, ok := roadsForCoverageGate(context.Background(), store, opts, nil); ok {
		t.Error("expected ok=false when no roads available")
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

type saveCall struct {
	gjson  string
	source string
}
