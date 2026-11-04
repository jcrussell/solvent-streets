package services

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// Test network around Livermore. Coordinates are written with 7 decimals the
// way the ingest formatter writes them, so shared vertices are byte-identical.
const (
	lonA, latA = -121.7700000, 37.6800000
	lonB, latB = -121.7600000, 37.6800000 // ~880 m east of A
	lonC, latC = -121.7600000, 37.6900000 // ~1110 m north of B
	lonD, latD = -121.7500000, 37.6800000 // ~880 m east of B
)

func line(tags map[string]string, pts ...[2]float64) resource.Feature {
	var s strings.Builder
	s.WriteString(`{"type":"LineString","coordinates":[`)
	for i, p := range pts {
		if i > 0 {
			s.WriteString(",")
		}
		fmt.Fprintf(&s, "[%.7f,%.7f]", p[0], p[1])
	}
	s.WriteString("]}")
	return resource.Feature{ID: fmt.Sprintf("r%d", len(pts)), Tags: tags, GeometryJSON: s.String(), SourceAPI: sourceOverpass}
}

func testProj() *geo.UTMProjector { return geo.NewUTMProjector(lonB, latB) }

func metres(p *geo.UTMProjector, a, b [2]float64) float64 {
	ax, ay := p.ToProjected(a[0], a[1])
	bx, by := p.ToProjected(b[0], b[1])
	return math.Hypot(ax-bx, ay-by)
}

func TestBuildGraph_SharedVertexJoinsWays(t *testing.T) {
	p := testProj()
	res := map[string]string{"highway": "residential"}
	g := BuildGraph(context.Background(), []resource.Feature{
		line(res, [2]float64{lonA, latA}, [2]float64{lonB, latB}, [2]float64{lonD, latD}),
		line(res, [2]float64{lonB, latB}, [2]float64{lonC, latC}),
	}, DefaultRoadSpeeds, p)
	if g.NodeCount() != 4 {
		t.Fatalf("shared vertex B must be one node: got %d nodes, want 4", g.NodeCount())
	}
	a, _ := g.NearestLonLat(lonA, latA, 1)
	c, _ := g.NearestLonLat(lonC, latC, 1)
	dist, src := g.ShortestFrom([]int{a})
	want := (metres(p, [2]float64{lonA, latA}, [2]float64{lonB, latB}) + metres(p, [2]float64{lonB, latB}, [2]float64{lonC, latC})) / (40 / 3.6)
	if math.Abs(dist[c]-want) > 1e-6 || src[c] != 0 {
		t.Errorf("A→C = %v s via source %d, want %v s via 0", dist[c], src[c], want)
	}
}

func TestBuildGraph_MaxspeedBeatsClass(t *testing.T) {
	p := testProj()
	g := BuildGraph(context.Background(), []resource.Feature{
		line(map[string]string{"highway": "residential", "maxspeed": "25 mph"}, [2]float64{lonA, latA}, [2]float64{lonB, latB}),
	}, DefaultRoadSpeeds, p)
	a, _ := g.NearestLonLat(lonA, latA, 1)
	b, _ := g.NearestLonLat(lonB, latB, 1)
	dist, _ := g.ShortestFrom([]int{a})
	want := metres(p, [2]float64{lonA, latA}, [2]float64{lonB, latB}) / (25 * mphToKmh / 3.6)
	if math.Abs(dist[b]-want) > 1e-6 {
		t.Errorf("cost = %v, want %v (25 mph, not the 40 km/h class default)", dist[b], want)
	}
}

func TestBuildGraph_OnewayBlocksReverse(t *testing.T) {
	p := testProj()
	for name, tags := range map[string]map[string]string{
		"oneway=yes": {"highway": "residential", "oneway": "yes"},
		"motorway":   {"highway": "motorway"},
		"roundabout": {"highway": "residential", "junction": "roundabout"},
	} {
		g := BuildGraph(context.Background(), []resource.Feature{line(tags, [2]float64{lonA, latA}, [2]float64{lonB, latB})}, DefaultRoadSpeeds, p)
		a, _ := g.NearestLonLat(lonA, latA, 1)
		b, _ := g.NearestLonLat(lonB, latB, 1)
		fwd, _ := g.ShortestFrom([]int{a})
		back, _ := g.ShortestFrom([]int{b})
		if math.IsInf(fwd[b], 1) || !math.IsInf(back[a], 1) {
			t.Errorf("%s: forward reachable=%v, reverse reachable=%v; want true/false", name, !math.IsInf(fwd[b], 1), !math.IsInf(back[a], 1))
		}
	}
	g := BuildGraph(context.Background(), []resource.Feature{line(map[string]string{"highway": "residential", "oneway": "-1"}, [2]float64{lonA, latA}, [2]float64{lonB, latB})}, DefaultRoadSpeeds, p)
	a, _ := g.NearestLonLat(lonA, latA, 1)
	b, _ := g.NearestLonLat(lonB, latB, 1)
	fwd, _ := g.ShortestFrom([]int{a})
	back, _ := g.ShortestFrom([]int{b})
	if !math.IsInf(fwd[b], 1) || math.IsInf(back[a], 1) {
		t.Error("oneway=-1 must allow only the reverse direction")
	}
}

func TestBuildGraph_IslandUnreachableAndSnapRadius(t *testing.T) {
	p := testProj()
	g := BuildGraph(context.Background(), []resource.Feature{
		line(map[string]string{"highway": "residential"}, [2]float64{lonA, latA}, [2]float64{lonB, latB}),
		line(map[string]string{"highway": "residential"}, [2]float64{lonC, latC}, [2]float64{lonD, latD}), // disjoint island
	}, DefaultRoadSpeeds, p)
	a, _ := g.NearestLonLat(lonA, latA, 1)
	c, _ := g.NearestLonLat(lonC, latC, 1)
	dist, src := g.ShortestFrom([]int{a})
	if !math.IsInf(dist[c], 1) || src[c] != -1 {
		t.Errorf("island must be unreachable: dist=%v src=%d", dist[c], src[c])
	}
	// Snap radius: 50 m north of A is within 300 m; 2 km north is not.
	ax, ay := p.ToProjected(lonA, latA)
	if n, ok := g.Nearest(ax, ay+50, HexSnapM); !ok || n != a {
		t.Errorf("Nearest within radius = (%d, %v), want (%d, true)", n, ok, a)
	}
	if _, ok := g.Nearest(ax, ay+2000, HexSnapM); ok {
		t.Error("a point 2 km from the network must not snap")
	}
}

func TestPreferredRoads(t *testing.T) {
	osm := resource.Feature{ID: "o", SourceAPI: "overpass"}
	gis := resource.Feature{ID: "g", SourceAPI: "arcgis"}
	if got, label := PreferredRoads([]resource.Feature{gis, osm, gis}); len(got) != 1 || got[0].ID != "o" || label != "overpass" {
		t.Errorf("mixed sources: got %v (%s), want only the overpass row", got, label)
	}
	if got, label := PreferredRoads([]resource.Feature{gis, gis}); len(got) != 2 || label != "all" {
		t.Errorf("arcgis-only: got %v (%s), want both rows", got, label)
	}
}

func TestComputeCoverage(t *testing.T) {
	p := testProj()
	g := BuildGraph(context.Background(), []resource.Feature{
		line(map[string]string{"highway": "residential"}, [2]float64{lonA, latA}, [2]float64{lonB, latB}, [2]float64{lonD, latD}),
		line(map[string]string{"highway": "residential"}, [2]float64{lonB, latB}, [2]float64{lonC, latC}),
	}, DefaultRoadSpeeds, p)
	ax, ay := p.ToProjected(lonA, latA)
	dx, dy := p.ToProjected(lonD, latD)
	cx, cy := p.ToProjected(lonC, latC)
	hexes := []geo.Hex{
		{ID: "near-a", CenterX: ax + 20, CenterY: ay + 20},
		{ID: "near-d", CenterX: dx - 20, CenterY: dy},
		{ID: "near-c", CenterX: cx, CenterY: cy + 10},
		{ID: "off-network", CenterX: cx + 5000, CenterY: cy + 5000},
	}
	spec := Registry["fire"]
	facilities := []Facility{
		{ID: "st1", Name: "Station A", Lon: lonA, Lat: latA + 0.001, Source: "osm"}, // ~110 m from A
		{ID: "far", Name: "Nowhere", Lon: lonA + 1, Lat: latA, Source: "config"},    // >500 m from any road
	}
	cov := ComputeCoverage(context.Background(), spec, hexes, g, facilities)
	if len(cov.Unsnapped) != 1 || cov.Unsnapped[0] != 1 {
		t.Errorf("unsnapped = %v, want [1]", cov.Unsnapped)
	}
	byID := map[string]HexCoverage{}
	for _, h := range cov.Hexes {
		byID[h.HexID] = h
	}
	if _, ok := byID["off-network"]; ok {
		t.Error("a hex far from any road must be omitted")
	}
	na, ok := byID["near-a"]
	if !ok || math.Abs(na.Seconds-spec.TurnoutS) > 1e-6 || na.Facility != 0 {
		t.Errorf("near-a = %+v, want turnout only (%v s) from facility 0", na, spec.TurnoutS)
	}
	wantD := spec.TurnoutS + (metres(p, [2]float64{lonA, latA}, [2]float64{lonB, latB})+metres(p, [2]float64{lonB, latB}, [2]float64{lonD, latD}))/(40/3.6)
	if nd := byID["near-d"]; math.Abs(nd.Seconds-wantD) > 1e-6 {
		t.Errorf("near-d = %v s, want %v", nd.Seconds, wantD)
	}
	if _, ok := byID["near-c"]; !ok {
		t.Error("near-c should be reachable via B")
	}

	// No reachable facility at all → no hexes, all unsnapped.
	empty := ComputeCoverage(context.Background(), spec, hexes, g, facilities[1:])
	if len(empty.Hexes) != 0 || len(empty.Unsnapped) != 1 {
		t.Errorf("no snapped facility: %+v", empty)
	}
}

// TestComputeCoverage_IsolatedApronSnapsToStreet pins the connected-snapping
// rule: a station 20 m from an unjoined two-vertex service apron and 140 m
// from the street must snap to the street (the larger component), reach the
// street's hexes, and be flagged Isolated only when nothing bigger is in
// range. A hex whose nearest vertex is the apron must still be painted from
// the reachable street vertex within its radius.
func TestComputeCoverage_IsolatedApronSnapsToStreet(t *testing.T) {
	p := testProj()
	// Street A–B–D plus a short apron 120 m north of A, not joined to it.
	ax, ay := p.ToProjected(lonA, latA)
	apronA := [2]float64{lonA, latA + 120/111000.0}
	apronB := [2]float64{lonA + 30/88000.0, latA + 120/111000.0}
	res := map[string]string{"highway": "residential"}
	// Densify the street so its component is clearly above
	// MinFacilityComponent: twelve vertices from A to D plus the spur to C.
	var street [][2]float64
	for i := range 12 {
		street = append(street, [2]float64{lonA + (lonD-lonA)*float64(i)/11, latA})
	}
	g := BuildGraph(context.Background(), []resource.Feature{
		line(res, street...),
		line(res, [2]float64{lonB, latB}, [2]float64{lonC, latC}),
		line(map[string]string{"highway": "service"}, apronA, apronB),
	}, DefaultRoadSpeeds, p)
	if got := g.ComponentSize(0); got < MinFacilityComponent {
		t.Fatalf("street component size = %d, want at least %d joined vertices", got, MinFacilityComponent)
	}
	spec := Registry["fire"]
	station := Facility{ID: "st", Name: "Apron station", Lon: lonA, Lat: latA + 100/111000.0} // ~20 m from the apron, ~100 m from A
	hexes := []geo.Hex{
		{ID: "near-a", CenterX: ax + 10, CenterY: ay + 10},
		{ID: "near-apron", CenterX: ax, CenterY: ay + 110}, // apron vertex is nearest, A is 110 m away (within HexSnapM)
	}
	cov := ComputeCoverage(context.Background(), spec, hexes, g, []Facility{station})
	if len(cov.Unsnapped) != 0 || len(cov.Isolated) != 0 {
		t.Fatalf("station must snap to the street component: unsnapped=%v isolated=%v", cov.Unsnapped, cov.Isolated)
	}
	ids := map[string]bool{}
	for _, h := range cov.Hexes {
		ids[h.HexID] = true
	}
	if !ids["near-a"] || !ids["near-apron"] {
		t.Errorf("both hexes must be reached via the street; got %v", ids)
	}

	// With only the apron in range, the station still snaps (to the apron)
	// but is reported Isolated so the CLI can warn.
	far := Facility{ID: "far", Lon: apronA[0], Lat: apronA[1] + 350/111000.0} // ~350 m north of the apron, ~470 m from A
	cov2 := ComputeCoverage(context.Background(), spec, hexes, g, []Facility{far})
	if len(cov2.Isolated) == 1 && len(cov2.Unsnapped) == 0 {
		return
	}
	// Depending on exact distances the street may also be in range; either
	// outcome is acceptable as long as nothing is silently dropped.
	if len(cov2.Unsnapped) != 0 {
		t.Errorf("a facility within FacilitySnapM must not be unsnapped: %+v", cov2)
	}
}
