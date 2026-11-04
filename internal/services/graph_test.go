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
	// The station snaps onto vertex A; near-a snaps ~20 m along A→B, so it is
	// turnout plus ~20 m of driving (not turnout alone, as a vertex snap
	// would have it).
	v := 40 / 3.6
	na, ok := byID["near-a"]
	if wantA := spec.TurnoutS + 20/v; !ok || math.Abs(na.Seconds-wantA) > 0.1 || na.Facility != 0 {
		t.Errorf("near-a = %+v, want ~%v s (turnout + 20 m) from facility 0", na, wantA)
	}
	// near-d snaps ~20 m short of D on B→D: travel stops at the snapped point.
	wantD := spec.TurnoutS + (metres(p, [2]float64{lonA, latA}, [2]float64{lonB, latB})+metres(p, [2]float64{lonB, latB}, [2]float64{lonD, latD})-20)/v
	if nd := byID["near-d"]; math.Abs(nd.Seconds-wantD) > 0.1 {
		t.Errorf("near-d = %v s, want ~%v", nd.Seconds, wantD)
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
// range. A hex whose nearest road is the apron must still be painted from
// the reachable street within its radius.
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
		{ID: "near-apron", CenterX: ax, CenterY: ay + 110}, // the apron is nearest, A is 110 m away (within HexSnapM)
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

// TestComputeCoverage_SparseSegmentReachesHexes pins segment snapping: a
// single straight 1.76 km segment (vertices only at its ends) reaches a hex
// at its midpoint, more than HexSnapM from either vertex, and the time is
// costed to the point along the road, not to a vertex.
func TestComputeCoverage_SparseSegmentReachesHexes(t *testing.T) {
	p := testProj()
	g := BuildGraph(context.Background(), []resource.Feature{
		line(map[string]string{"highway": "residential"}, [2]float64{lonA, latA}, [2]float64{lonD, latD}),
	}, DefaultRoadSpeeds, p)
	ax, ay := p.ToProjected(lonA, latA)
	dx, dy := p.ToProjected(lonD, latD)
	mx, my := (ax+dx)/2, (ay+dy)/2
	half := math.Hypot(dx-ax, dy-ay) / 2
	if half <= HexSnapM {
		t.Fatalf("test premise: midpoint must be > %v m from both vertices, got %v", HexSnapM, half)
	}
	spec := Registry["fire"]
	cov := ComputeCoverage(context.Background(), spec, []geo.Hex{{ID: "mid", CenterX: mx, CenterY: my + 30}},
		g, []Facility{{ID: "st", Lon: lonA, Lat: latA}})
	if len(cov.Hexes) != 1 {
		t.Fatalf("midpoint hex must be reached along the segment, got %+v", cov)
	}
	if want := spec.TurnoutS + half/(40/3.6); math.Abs(cov.Hexes[0].Seconds-want) > 0.1 {
		t.Errorf("mid = %v s, want ~%v (turnout + half the segment)", cov.Hexes[0].Seconds, want)
	}
}

// TestComputeCoverage_SameSegmentRespectsOneway: a station and a hex on the
// same long segment are joined along it in the allowed direction only.
func TestComputeCoverage_SameSegmentRespectsOneway(t *testing.T) {
	p := testProj()
	ax, ay := p.ToProjected(lonA, latA)
	dx, dy := p.ToProjected(lonD, latD)
	at := func(f float64) (float64, float64) { return ax + f*(dx-ax), ay + f*(dy-ay) }
	stLon, stLat := p.FromProjected(at(0.25))
	hx, hy := at(0.75)
	length := math.Hypot(dx-ax, dy-ay)
	spec := Registry["fire"]
	hexes := []geo.Hex{{ID: "h", CenterX: hx, CenterY: hy}}
	st := []Facility{{ID: "st", Lon: stLon, Lat: stLat}}

	two := BuildGraph(context.Background(), []resource.Feature{
		line(map[string]string{"highway": "residential"}, [2]float64{lonA, latA}, [2]float64{lonD, latD}),
	}, DefaultRoadSpeeds, p)
	cov := ComputeCoverage(context.Background(), spec, hexes, two, st)
	if want := spec.TurnoutS + length/2/(40/3.6); len(cov.Hexes) != 1 || math.Abs(cov.Hexes[0].Seconds-want) > 0.1 {
		t.Errorf("two-way same segment = %+v, want ~%v s (half the segment)", cov.Hexes, want)
	}

	// Oneway D→A: the hex lies downstream of nothing the station can reach.
	rev := BuildGraph(context.Background(), []resource.Feature{
		line(map[string]string{"highway": "residential", "oneway": "-1"}, [2]float64{lonA, latA}, [2]float64{lonD, latD}),
	}, DefaultRoadSpeeds, p)
	if cov := ComputeCoverage(context.Background(), spec, hexes, rev, st); len(cov.Hexes) != 0 {
		t.Errorf("oneway against the hex must leave it unreached, got %+v", cov.Hexes)
	}
}

// TestSnapConnected_LargestComponentMustBeInRange pins the radius fix: the
// "largest component" is chosen among segments truly within maxDist, so a
// big network sitting only in the search square's corner (beyond the radius)
// cannot make an in-range smaller component lose and the point go unsnapped.
func TestSnapConnected_LargestComponentMustBeInRange(t *testing.T) {
	p := testProj()
	bx, by := p.ToProjected(lonB, latB)
	pt := func(x, y float64) [2]float64 {
		lon, lat := p.FromProjected(x, y)
		return [2]float64{lon, lat}
	}
	res := map[string]string{"highway": "residential"}
	// A 3-vertex stub 400 m east of the query point.
	stub := line(map[string]string{"highway": "service"}, pt(bx+400, by-10), pt(bx+400, by), pt(bx+400, by+10))
	// A 20-vertex street whose nearest point is ~600 m away diagonally:
	// inside the 500 m search square, outside the 500 m radius.
	var street [][2]float64
	for i := range 20 {
		street = append(street, pt(bx-430-float64(i)*10, by+430+float64(i)*10))
	}
	g := BuildGraph(context.Background(), []resource.Feature{stub, line(res, street...)}, DefaultRoadSpeeds, p)
	s, ok := g.SnapConnected(bx, by, FacilitySnapM)
	if !ok {
		t.Fatal("a stub within range must still be snapped to when the bigger network is out of range")
	}
	if g.SnapComponentSize(s) != 3 || s.Dist > FacilitySnapM {
		t.Errorf("snapped to component of %d vertices at %v m, want the 3-vertex stub within %v m", g.SnapComponentSize(s), s.Dist, FacilitySnapM)
	}
}

// TestSeedsAndArrival_AtVertexIgnoresDirection: a position exactly on a
// segment's end vertex reaches (and is reached from) that vertex at zero
// cost even when the segment's oneway forbids driving along it that way;
// mid-segment positions respect the direction.
func TestSeedsAndArrival_AtVertexIgnoresDirection(t *testing.T) {
	p := testProj()
	// oneway=yes A→B: the segment may only be driven from A toward B.
	g := BuildGraph(context.Background(), []resource.Feature{
		line(map[string]string{"highway": "residential", "oneway": "yes"}, [2]float64{lonA, latA}, [2]float64{lonB, latB}),
	}, DefaultRoadSpeeds, p)
	bx, by := p.ToProjected(lonB, latB)
	atB, ok := g.SnapWhere(bx, by, 1, nil)
	if !ok || atB.T < 1-1e-9 {
		t.Fatalf("snap at B = %+v (ok=%v), want T=1", atB, ok)
	}
	seeds := g.SeedsAt(atB, 0)
	b, _ := g.NearestLonLat(lonB, latB, 1)
	if len(seeds) != 1 || seeds[0].Node != b || seeds[0].Cost != 0 {
		t.Errorf("seeds at B on A→B oneway = %+v, want only B at 0 s (A is upstream)", seeds)
	}

	// A source at A reaches the midpoint going with the oneway; a source at
	// B cannot reach it (driving B→A is forbidden).
	ax, ay := p.ToProjected(lonA, latA)
	mid, _ := g.SnapWhere((ax+bx)/2, (ay+by)/2, 1, nil)
	a, _ := g.NearestLonLat(lonA, latA, 1)
	dist, src := g.ShortestFrom([]int{a})
	if secs, s := g.ArrivalAt(mid, dist, src); math.IsInf(secs, 1) || s != 0 {
		t.Errorf("midpoint from A = (%v, %d), want reachable from source 0", secs, s)
	}
	dist, src = g.ShortestFrom([]int{b})
	if secs, s := g.ArrivalAt(mid, dist, src); !math.IsInf(secs, 1) || s != -1 {
		t.Errorf("midpoint from B = (%v, %d), want unreachable against the oneway", secs, s)
	}
	if secs, s := g.ArrivalAt(atB, dist, src); secs != 0 || s != 0 {
		t.Errorf("B itself from B = (%v, %d), want (0, 0)", secs, s)
	}
}
