package services

import (
	"container/heap"
	"context"
	"math"

	"github.com/peterstace/simplefeatures/geom"

	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// Snap radii. A hex centre or a facility more than this far from any road
// segment is treated as off-network rather than snapped to something far
// away. Distances are to the nearest point ALONG a segment, not to its
// vertices, so a long straight road with sparse vertices still reaches the
// hexes it passes through.
const (
	// HexSnapM bounds how far a hex centre may be from the nearest road; at
	// the default 100 m hex edge every hex that holds any road is within
	// reach, and an empty hex (water, open space) is left unpainted.
	HexSnapM = 300.0
	// FacilitySnapM bounds how far a facility point may be from the road
	// network; stations sit on a parcel beside the street, not on it.
	FacilitySnapM = 500.0
)

// Facility is one point of service for a coverage computation.
type Facility struct {
	ID     string
	Name   string
	Lon    float64
	Lat    float64
	Source string // "osm" or "config"
}

// Graph is a routable road network: vertices keyed by position, directed
// edges weighted in seconds, the undirected segments those edges came from,
// and spatial indexes over the vertices and segments for snapping.
type Graph struct {
	proj  geo.Projector
	xs    []float64
	ys    []float64
	adj   [][]edge
	index *geo.GeomIndex
	// segs are the road segments (one per consecutive vertex pair of a way)
	// and segIndex their R-tree, so a point snaps to the nearest position
	// along a road rather than to the nearest vertex.
	segs     []segment
	segIndex *geo.GeomIndex
	// comp is each vertex's weakly connected component id and compSize the
	// vertex count per component, so snapping can prefer the street network
	// over an isolated driveway or parking aisle that happens to be closer.
	comp     []int
	compSize []int
}

// MinFacilityComponent is the component size below which a facility's
// snapped vertex is reported as isolated: a station whose nearest road
// fragment is a stub of a few vertices is almost certainly mapped beside an
// apron that OSM does not join to the street.
const MinFacilityComponent = 10

type edge struct {
	to   int
	cost float64 // seconds
}

// segment is one road segment between vertices a and c: its projected
// length, its speed, which directions it may be driven (fwd: a→c,
// back: c→a), and whether it is a motorway carriageway, which snapping
// avoids (see SnapWhere).
type segment struct {
	a, c      int
	length    float64 // metres
	mps       float64
	fwd, back bool
	motorway  bool
}

// Snap is a position on the network: the point at fraction T (0 at the
// segment's a end, 1 at its c end) along segment Seg, Dist metres from the
// query point.
type Snap struct {
	Seg  int
	T    float64
	Dist float64
}

// Seed is a Dijkstra start: Node is reached at Cost seconds from source
// index Source.
type Seed struct {
	Node   int
	Cost   float64
	Source int
}

// sourceOverpass is the SourceAPI label the Overpass ingest writes.
const sourceOverpass = "overpass"

// PreferredRoads picks the road rows the graph is built from: the Overpass
// rows when any exist, else everything (an ArcGIS-only city). A city that
// ingested both sources holds two complete networks of the same streets whose
// vertices never coincide; mixing them would yield two disjoint graphs, with
// a hex snapped to one and a station to the other unreachable. Returns the
// chosen rows and the source label used.
func PreferredRoads(roads []resource.Feature) ([]resource.Feature, string) {
	var osm []resource.Feature
	for _, r := range roads {
		if r.SourceAPI == sourceOverpass {
			osm = append(osm, r)
		}
	}
	if len(osm) > 0 {
		return osm, sourceOverpass
	}
	return roads, "all"
}

// BuildGraph assembles the network from road features. Vertices are keyed by
// their unprojected lon/lat as parsed from the stored GeoJSON: ways that share
// an OSM node were written from the same node coordinates by the same
// formatter, so they parse to bit-identical floats and join; a bridge over a
// street has distinct nodes and stays separate. Each consecutive vertex pair
// becomes an edge whose cost is its projected length over the speed for the
// road's tags (speeds.SpeedKmh). oneway=yes/true/1 (and the implied oneway of
// motorways and roundabouts) keeps only the forward edge, oneway=-1 only the
// reverse. Motorway and motorway_link segments are marked so snapping can
// pass over them (SnapWhere). Geometries that are not lines are skipped.
// Cancelling ctx stops the build and returns what was built so far.
func BuildGraph(ctx context.Context, roads []resource.Feature, speeds SpeedTable, proj geo.Projector) *Graph {
	b := &graphBuilder{g: &Graph{proj: proj}, nodeAt: make(map[[2]float64]int)}
	for _, r := range roads {
		if ctx.Err() != nil {
			break
		}
		mps := speeds.SpeedKmh(r.Tags) / 3.6
		if mps <= 0 {
			continue
		}
		fwd, back := direction(r.Tags)
		motorway := isMotorway(r.Tags)
		for _, seq := range lineSequences(r.GeometryJSON) {
			b.addLine(seq, mps, fwd, back, motorway)
		}
	}
	return b.finish()
}

// graphBuilder accumulates vertices (deduplicated by lon/lat) and edges.
type graphBuilder struct {
	g      *Graph
	nodeAt map[[2]float64]int
}

// node returns the vertex index for an unprojected position, adding it on
// first sight.
func (b *graphBuilder) node(xy geom.XY) int {
	key := [2]float64{xy.X, xy.Y}
	if i, ok := b.nodeAt[key]; ok {
		return i
	}
	px, py := b.g.proj.ToProjected(xy.X, xy.Y)
	i := len(b.g.xs)
	b.nodeAt[key] = i
	b.g.xs = append(b.g.xs, px)
	b.g.ys = append(b.g.ys, py)
	b.g.adj = append(b.g.adj, nil)
	return i
}

// addLine adds one edge per consecutive vertex pair of seq, costed at mps
// metres per second, in the allowed direction(s); motorway marks the
// segments as carriageway for snapping.
func (b *graphBuilder) addLine(seq geom.Sequence, mps float64, fwd, back, motorway bool) {
	g := b.g
	for i := 1; i < seq.Length(); i++ {
		a := b.node(seq.GetXY(i - 1))
		c := b.node(seq.GetXY(i))
		if a == c {
			continue
		}
		length := math.Hypot(g.xs[a]-g.xs[c], g.ys[a]-g.ys[c])
		cost := length / mps
		g.segs = append(g.segs, segment{a: a, c: c, length: length, mps: mps, fwd: fwd, back: back, motorway: motorway})
		if fwd {
			g.adj[a] = append(g.adj[a], edge{to: c, cost: cost})
		}
		if back {
			g.adj[c] = append(g.adj[c], edge{to: a, cost: cost})
		}
	}
}

// finish builds the vertex and segment spatial indexes and the component
// labels and returns the graph.
func (b *graphBuilder) finish() *Graph {
	g := b.g
	pts := make([]geom.Geometry, len(g.xs))
	for i := range g.xs {
		pts[i] = geom.NewPoint(geom.Coordinates{XY: geom.XY{X: g.xs[i], Y: g.ys[i]}}).AsGeometry()
	}
	g.index = geo.NewGeomIndexFromGeoms(pts)
	lines := make([]geom.Geometry, len(g.segs))
	for i, sg := range g.segs {
		seq := geom.NewSequence([]float64{g.xs[sg.a], g.ys[sg.a], g.xs[sg.c], g.ys[sg.c]}, geom.DimXY)
		lines[i] = geom.NewLineString(seq).AsGeometry()
	}
	g.segIndex = geo.NewGeomIndexFromGeoms(lines)
	g.labelComponents()
	return g
}

// labelComponents assigns weakly connected component ids (edges treated as
// undirected: a one-way street still joins its ends) with union-find.
func (g *Graph) labelComponents() {
	n := len(g.xs)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for a, edges := range g.adj {
		for _, e := range edges {
			ra, rb := find(a), find(e.to)
			if ra != rb {
				parent[ra] = rb
			}
		}
	}
	g.comp = make([]int, n)
	ids := map[int]int{}
	for i := range parent {
		r := find(i)
		id, ok := ids[r]
		if !ok {
			id = len(ids)
			ids[r] = id
			g.compSize = append(g.compSize, 0)
		}
		g.comp[i] = id
		g.compSize[id]++
	}
}

// ComponentSize returns the vertex count of n's connected component.
func (g *Graph) ComponentSize(n int) int {
	if n < 0 || n >= len(g.comp) {
		return 0
	}
	return g.compSize[g.comp[n]]
}

// lineSequences returns the coordinate sequences of a LineString or
// MultiLineString GeoJSON geometry; anything else yields none.
func lineSequences(gjson string) []geom.Sequence {
	g, err := geom.UnmarshalGeoJSON([]byte(gjson), geom.NoValidate{})
	if err != nil {
		return nil
	}
	switch {
	case g.IsLineString():
		return []geom.Sequence{g.MustAsLineString().Coordinates()}
	case g.IsMultiLineString():
		ml := g.MustAsMultiLineString()
		out := make([]geom.Sequence, 0, ml.NumLineStrings())
		for i := range ml.NumLineStrings() {
			out = append(out, ml.LineStringN(i).Coordinates())
		}
		return out
	}
	return nil
}

// direction reports which way a road may be driven: both unless oneway (or
// an implied oneway: motorways, their links, and roundabouts) says otherwise.
func direction(tags map[string]string) (fwd, back bool) {
	switch tags["oneway"] {
	case "yes", "true", "1":
		return true, false
	case "-1", "reverse":
		return false, true
	case "no", "false", "0":
		return true, true
	}
	if tags["junction"] == "roundabout" || isMotorway(tags) {
		return true, false
	}
	return true, true
}

// isMotorway reports whether a road is a motorway carriageway or one of its
// links: implicitly one-way and grade-separated, so a point placed on one can
// only be reached or left in its direction of travel. Trunk roads are not
// included; they are often surface arterials with frontage.
func isMotorway(tags map[string]string) bool {
	return tags["highway"] == "motorway" || tags["highway"] == "motorway_link"
}

// NodeCount is the number of vertices.
func (g *Graph) NodeCount() int { return len(g.xs) }

// Nearest returns the vertex closest to the projected point (x, y) within
// maxDist metres, or false when none is that close.
func (g *Graph) Nearest(x, y, maxDist float64) (int, bool) {
	if g.index == nil || g.index.Len() == 0 {
		return 0, false
	}
	env := squareAround(x, y, maxDist)
	best, bestD := -1, maxDist
	for _, id := range g.index.SearchIDs(env) {
		if d := math.Hypot(g.xs[id]-x, g.ys[id]-y); d <= bestD {
			best, bestD = id, d
		}
	}
	return best, best >= 0
}

// NearestLonLat is Nearest for an unprojected position.
func (g *Graph) NearestLonLat(lon, lat, maxDist float64) (int, bool) {
	x, y := g.proj.ToProjected(lon, lat)
	return g.Nearest(x, y, maxDist)
}

// squareAround is the envelope of half-side d centred on (x, y): the R-tree
// prefilter for a radius-d search.
func squareAround(x, y, d float64) geom.Envelope {
	return geom.NewEnvelope(geom.XY{X: x - d, Y: y - d}, geom.XY{X: x + d, Y: y + d})
}

// snapCandidates returns the nearest position on every segment within
// maxDist metres (true distance, not the search square) of (x, y).
func (g *Graph) snapCandidates(x, y, maxDist float64) []Snap {
	if g.segIndex == nil || g.segIndex.Len() == 0 {
		return nil
	}
	env := squareAround(x, y, maxDist)
	var out []Snap
	for _, id := range g.segIndex.SearchIDs(env) {
		sg := g.segs[id]
		ax, ay := g.xs[sg.a], g.ys[sg.a]
		dx, dy := g.xs[sg.c]-ax, g.ys[sg.c]-ay
		t := 0.0
		if l2 := dx*dx + dy*dy; l2 > 0 {
			t = math.Max(0, math.Min(1, ((x-ax)*dx+(y-ay)*dy)/l2))
		}
		if d := math.Hypot(ax+t*dx-x, ay+t*dy-y); d <= maxDist {
			out = append(out, Snap{Seg: id, T: t, Dist: d})
		}
	}
	return out
}

// SnapWhere returns the nearest network position to (x, y) within maxDist
// that ok accepts (nil accepts all), or false when there is none. Used to
// snap a hex to the nearest position a facility can actually reach, rather
// than to an unreachable fragment that is marginally closer. A motorway
// carriageway (or link) is taken only when no other accepted road is within
// maxDist: it is one-way and has no frontage, so a point beside a freeway
// snapped onto it would be timed as reached only in the carriageway's
// direction of travel, minutes off its true time via the local street.
func (g *Graph) SnapWhere(x, y, maxDist float64, ok func(Snap) bool) (Snap, bool) {
	var best, motorway Snap
	found, foundMotorway := false, false
	for _, c := range g.snapCandidates(x, y, maxDist) {
		if ok != nil && !ok(c) {
			continue
		}
		if g.segs[c.Seg].motorway {
			if !foundMotorway || c.Dist < motorway.Dist {
				motorway, foundMotorway = c, true
			}
			continue
		}
		if !found || c.Dist < best.Dist {
			best, found = c, true
		}
	}
	if found {
		return best, true
	}
	return motorway, foundMotorway
}

// SnapConnected is SnapWhere biased toward the street network: among the
// segments within maxDist it returns the nearest position on one that
// belongs to the LARGEST connected component in range, passing over
// motorway carriageways as SnapWhere does. A station beside an
// unjoined apron or driveway therefore snaps to the street a little farther
// away instead of to the stub, which would otherwise swallow the whole
// station silently. Only segments actually within maxDist compete for
// "largest", so the choice never lands on a component that is out of range.
func (g *Graph) SnapConnected(x, y, maxDist float64) (Snap, bool) {
	cands := g.snapCandidates(x, y, maxDist)
	bestSize := 0
	for _, c := range cands {
		if s := g.SnapComponentSize(c); s > bestSize {
			bestSize = s
		}
	}
	if bestSize == 0 {
		return Snap{}, false
	}
	return g.SnapWhere(x, y, maxDist, func(c Snap) bool { return g.SnapComponentSize(c) == bestSize })
}

// SnapConnectedLonLat is SnapConnected for an unprojected position.
func (g *Graph) SnapConnectedLonLat(lon, lat, maxDist float64) (Snap, bool) {
	x, y := g.proj.ToProjected(lon, lat)
	return g.SnapConnected(x, y, maxDist)
}

// SnapComponentSize is ComponentSize for the segment s lies on.
func (g *Graph) SnapComponentSize(s Snap) int {
	if s.Seg < 0 || s.Seg >= len(g.segs) {
		return 0
	}
	return g.ComponentSize(g.segs[s.Seg].a)
}

// snapEps is the along-segment distance (metres) under which a snapped
// position is treated as sitting on the segment's end vertex.
const snapEps = 1e-6

// SeedsAt returns the Dijkstra seeds for a source at network position s:
// each end vertex the source can drive to along its segment (respecting
// oneway), at the travel time to get there. A position on a vertex reaches
// that vertex at zero cost whatever the direction.
func (g *Graph) SeedsAt(s Snap, source int) []Seed {
	sg := g.segs[s.Seg]
	toA, toC := s.T*sg.length, (1-s.T)*sg.length
	var out []Seed
	if sg.back || toA <= snapEps {
		out = append(out, Seed{Node: sg.a, Cost: toA / sg.mps, Source: source})
	}
	if sg.fwd || toC <= snapEps {
		out = append(out, Seed{Node: sg.c, Cost: toC / sg.mps, Source: source})
	}
	return out
}

// ArrivalAt returns the travel seconds to network position s given the
// vertex times dist/src from ShortestFromSeeds, and the source index that
// achieves it; (+Inf, -1) when s is unreachable. Arriving from the a end
// requires the segment to allow a→c, from the c end c→a — except at the
// vertex itself, which needs no driving along the segment.
func (g *Graph) ArrivalAt(s Snap, dist []float64, src []int) (float64, int) {
	sg := g.segs[s.Seg]
	fromA, fromC := s.T*sg.length, (1-s.T)*sg.length
	best, bestSrc := math.Inf(1), -1
	if src[sg.a] >= 0 && (sg.fwd || fromA <= snapEps) {
		if c := dist[sg.a] + fromA/sg.mps; c < best {
			best, bestSrc = c, src[sg.a]
		}
	}
	if src[sg.c] >= 0 && (sg.back || fromC <= snapEps) {
		if c := dist[sg.c] + fromC/sg.mps; c < best {
			best, bestSrc = c, src[sg.c]
		}
	}
	return best, bestSrc
}

// AlongSegment returns the seconds to drive from position from to position
// to when both lie on the same segment and its direction allows it, else
// +Inf. It covers a hex and a facility on one long segment, which the
// vertex-based ArrivalAt would route out to an end vertex and back.
func (g *Graph) AlongSegment(from, to Snap) float64 {
	if from.Seg != to.Seg {
		return math.Inf(1)
	}
	sg := g.segs[from.Seg]
	d := (to.T - from.T) * sg.length
	switch {
	case math.Abs(d) <= snapEps:
		return 0
	case d > 0 && sg.fwd, d < 0 && sg.back:
		return math.Abs(d) / sg.mps
	}
	return math.Inf(1)
}

// ShortestFrom runs a multi-source Dijkstra with every source vertex
// starting at zero; see ShortestFromSeeds.
func (g *Graph) ShortestFrom(sources []int) (dist []float64, src []int) {
	seeds := make([]Seed, 0, len(sources))
	for i, s := range sources {
		seeds = append(seeds, Seed{Node: s, Source: i})
	}
	return g.ShortestFromSeeds(seeds)
}

// ShortestFromSeeds runs a multi-source Dijkstra from seeds: each vertex ends
// up with the travel seconds from its nearest seed and that seed's Source.
// Unreachable vertices carry +Inf and source -1. Out-of-range seed nodes are
// ignored.
func (g *Graph) ShortestFromSeeds(seeds []Seed) (dist []float64, src []int) {
	n := len(g.xs)
	dist = make([]float64, n)
	src = make([]int, n)
	for i := range dist {
		dist[i] = math.Inf(1)
		src[i] = -1
	}
	pq := &nodeHeap{}
	for _, s := range seeds {
		if s.Node < 0 || s.Node >= n || s.Cost >= dist[s.Node] {
			continue
		}
		dist[s.Node] = s.Cost
		src[s.Node] = s.Source
		heap.Push(pq, item{node: s.Node, dist: s.Cost})
	}
	for pq.Len() > 0 {
		cur, ok := heap.Pop(pq).(item)
		if !ok || cur.dist > dist[cur.node] {
			continue // stale entry
		}
		for _, e := range g.adj[cur.node] {
			nd := cur.dist + e.cost
			if nd < dist[e.to] {
				dist[e.to] = nd
				src[e.to] = src[cur.node]
				heap.Push(pq, item{node: e.to, dist: nd})
			}
		}
	}
	return dist, src
}

type item struct {
	node int
	dist float64
}

type nodeHeap []item

func (h *nodeHeap) Len() int           { return len(*h) }
func (h *nodeHeap) Less(i, j int) bool { return (*h)[i].dist < (*h)[j].dist }
func (h *nodeHeap) Swap(i, j int)      { (*h)[i], (*h)[j] = (*h)[j], (*h)[i] }
func (h *nodeHeap) Push(x any) {
	if it, ok := x.(item); ok {
		*h = append(*h, it)
	}
}
func (h *nodeHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}
