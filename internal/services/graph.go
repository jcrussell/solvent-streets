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
// vertex is treated as off-network rather than snapped to something far away.
const (
	// HexSnapM bounds how far a hex centre may be from the nearest road
	// vertex; at the default 100 m hex edge every hex that holds any road is
	// within reach, and an empty hex (water, open space) is left unpainted.
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
// edges weighted in seconds, and a spatial index over the vertices for
// snapping.
type Graph struct {
	proj  geo.Projector
	xs    []float64
	ys    []float64
	adj   [][]edge
	index *geo.GeomIndex
}

type edge struct {
	to   int
	cost float64 // seconds
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
// reverse. Geometries that are not lines are skipped. Cancelling ctx stops the
// build and returns what was built so far.
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
		for _, seq := range lineSequences(r.GeometryJSON) {
			b.addLine(seq, mps, fwd, back)
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
// metres per second, in the allowed direction(s).
func (b *graphBuilder) addLine(seq geom.Sequence, mps float64, fwd, back bool) {
	g := b.g
	for i := 1; i < seq.Length(); i++ {
		a := b.node(seq.GetXY(i - 1))
		c := b.node(seq.GetXY(i))
		if a == c {
			continue
		}
		cost := math.Hypot(g.xs[a]-g.xs[c], g.ys[a]-g.ys[c]) / mps
		if fwd {
			g.adj[a] = append(g.adj[a], edge{to: c, cost: cost})
		}
		if back {
			g.adj[c] = append(g.adj[c], edge{to: a, cost: cost})
		}
	}
}

// finish builds the vertex spatial index and returns the graph.
func (b *graphBuilder) finish() *Graph {
	g := b.g
	pts := make([]geom.Geometry, len(g.xs))
	for i := range g.xs {
		pts[i] = geom.NewPoint(geom.Coordinates{XY: geom.XY{X: g.xs[i], Y: g.ys[i]}}).AsGeometry()
	}
	g.index = geo.NewGeomIndexFromGeoms(pts)
	return g
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
	if tags["junction"] == "roundabout" || tags["highway"] == "motorway" || tags["highway"] == "motorway_link" {
		return true, false
	}
	return true, true
}

// NodeCount is the number of vertices.
func (g *Graph) NodeCount() int { return len(g.xs) }

// Nearest returns the vertex closest to the projected point (x, y) within
// maxDist metres, or false when none is that close.
func (g *Graph) Nearest(x, y, maxDist float64) (int, bool) {
	if g.index == nil || g.index.Len() == 0 {
		return 0, false
	}
	env := geom.NewEnvelope(geom.XY{X: x - maxDist, Y: y - maxDist}, geom.XY{X: x + maxDist, Y: y + maxDist})
	best, bestD := -1, maxDist
	for _, id := range g.index.SearchIDs(env) {
		d := math.Hypot(g.xs[id]-x, g.ys[id]-y)
		if d <= bestD {
			best, bestD = id, d
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// NearestLonLat is Nearest for an unprojected position.
func (g *Graph) NearestLonLat(lon, lat, maxDist float64) (int, bool) {
	x, y := g.proj.ToProjected(lon, lat)
	return g.Nearest(x, y, maxDist)
}

// ShortestFrom runs a multi-source Dijkstra: every source vertex starts at
// zero and each vertex ends up with the travel seconds from its nearest
// source and that source's index into sources. Unreachable vertices carry
// +Inf and source -1.
func (g *Graph) ShortestFrom(sources []int) (dist []float64, src []int) {
	n := len(g.xs)
	dist = make([]float64, n)
	src = make([]int, n)
	for i := range dist {
		dist[i] = math.Inf(1)
		src[i] = -1
	}
	pq := &nodeHeap{}
	for i, s := range sources {
		if s < 0 || s >= n {
			continue
		}
		if dist[s] > 0 || src[s] == -1 {
			dist[s] = 0
			src[s] = i
			heap.Push(pq, item{node: s, dist: 0})
		}
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
