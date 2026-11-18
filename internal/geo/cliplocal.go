package geo

import (
	"context"
	"math"
	"runtime"
	"sync"

	"github.com/peterstace/simplefeatures/geom"
)

// localClipLeafHexes is how many straddling hexes a quadtree cell may hold
// before ClipHexesToBoundaryLocal splits it. Small leaves keep each hex's
// boundary piece small; the split itself costs one overlay per child against
// its parent's (already cut down) piece.
const localClipLeafHexes = 8

// localClipMaxDepth caps the quadtree: hex centres are a lattice, so cells
// stop splitting long before this, but a cap keeps a degenerate input (every
// centre on one point) from recursing without end.
const localClipMaxDepth = 24

// ClipHexesToBoundaryLocal clips hexes to boundary with the result of
// ClipHexesToBoundary, but much faster for a long boundary: the hexes the
// boundary passes through are clipped against a local piece of it rather
// than the whole of it.
//
// ClipHexesToBoundary intersects every straddling hex with a whole boundary
// part. The overlay converts and indexes all of that part's vertices on every
// call, so for a 57,000-vertex boundary (Charlotte) the clip costs
// O(straddling hexes × boundary vertices) and runs for tens of seconds. Here
// the straddling hexes are grouped in a quadtree over their centres, and each
// cell's piece of the boundary is its parent's piece cut to the cell, grown
// by the largest hex's reach beyond its centre. Every hex lies inside its
// cell's grown rectangle, so hex ∩ boundary = hex ∩ (boundary ∩ rectangle):
// the result is the same set of hexes with the same shapes, to floating-point
// rounding (an original boundary segment that crosses a rectangle edge is
// carried as the part of it inside, whose endpoint on the rectangle edge is
// rounded). Interior and exterior hexes take ClipHexesToBoundary's own fast
// path unchanged, and the output is in input order, as there.
//
// Compute keeps ClipHexesToBoundary, whose output its stored results are
// defined on; this is for redrawing that grid (the exporter's hexgrid.geojson
// and the hex ids it draws), where rounding-level differences in a clipped
// edge are invisible. A piece that fails to cut falls back to its parent's
// piece, which is slower but the same clip.
func ClipHexesToBoundaryLocal(ctx context.Context, hexes []Hex, boundary geom.Geometry) []Hex {
	segs := boundarySegments(boundary)
	edgeIdx := NewGeomIndexFromGeoms(segs)
	maxX := math.Inf(1)
	if _, hi, ok := boundary.Envelope().MinMaxXYs(); ok {
		maxX = hi.X
	}

	// Classify every hex as ClipHexesToBoundary does: no boundary edge in its
	// envelope means it is wholly inside (kept as is) or wholly outside
	// (dropped), decided by its centre; the rest straddle the boundary.
	const (
		drop int8 = iota
		keep
		straddle
	)
	classes := ParallelMap(ctx, hexes, func(_ int, h Hex) []int8 {
		if len(edgeIdx.SearchIDs(h.Geom.Envelope())) > 0 {
			return []int8{straddle}
		}
		if centreInside(h.Geom.Centroid(), segs, edgeIdx, maxX) {
			return []int8{keep}
		}
		return []int8{drop}
	}, nil)
	if len(classes) != len(hexes) {
		// Cancelled part-way: ParallelMap leaves the rest unprocessed.
		return nil
	}

	var straddling []int
	for i, c := range classes {
		if c == straddle {
			straddling = append(straddling, i)
		}
	}
	clipped := make([]*Hex, len(hexes))
	if len(straddling) > 0 {
		lc := newLocalClipper(hexes, straddling)
		lc.clip(ctx, lc.root, boundary, straddling, 0, clipped)
		lc.wg.Wait()
	}

	out := make([]Hex, 0, len(hexes))
	for i := range hexes {
		switch classes[i] {
		case keep:
			out = append(out, hexes[i])
		case straddle:
			if clipped[i] != nil {
				out = append(out, *clipped[i])
			}
		}
	}
	return out
}

// centreInside reports whether c lies inside the boundary whose ring
// segments are segs (indexed by idx), for a centre no segment comes near: a
// ray cast to the right, through the segments the index finds on it. It is
// the answer ClipHexesToBoundary's geom.Intersects gives for such a point
// (every ring of a valid polygon counts by parity, holes included), without
// walking every boundary vertex per hex: for a 33,000-vertex boundary and
// 90,000 hexes that walk alone takes seconds.
func centreInside(c geom.Point, segs []geom.Geometry, idx *GeomIndex, maxX float64) bool {
	p, ok := c.XY()
	if !ok || p.X > maxX {
		return false
	}
	ray := geom.NewEnvelope(p, geom.XY{X: maxX, Y: p.Y})
	inside := false
	for _, id := range idx.SearchIDs(ray) {
		ls, ok := segs[id].AsLineString()
		if !ok {
			continue
		}
		seq := ls.Coordinates()
		a, b := seq.GetXY(0), seq.GetXY(1)
		if (a.Y > p.Y) == (b.Y > p.Y) {
			continue // the segment does not cross the ray's line
		}
		if x := a.X + (p.Y-a.Y)*(b.X-a.X)/(b.Y-a.Y); x > p.X {
			inside = !inside
		}
	}
	return inside
}

// localClipper carries one ClipHexesToBoundaryLocal quadtree walk.
type localClipper struct {
	hexes []Hex
	// reach is how far any straddling hex extends beyond its centre, on
	// either axis, plus a margin: a cell grown by it contains the whole
	// envelope of every hex whose centre lies in the cell.
	reach float64
	root  geom.Envelope
	// sem bounds the goroutines the walk runs its subtrees on; a subtree
	// that finds no slot runs on the caller's goroutine.
	sem chan struct{}
	wg  sync.WaitGroup
}

func newLocalClipper(hexes []Hex, straddling []int) *localClipper {
	var reach float64
	xys := make([]geom.XY, 0, len(straddling))
	for _, i := range straddling {
		h := &hexes[i]
		c := geom.XY{X: h.CenterX, Y: h.CenterY}
		xys = append(xys, c)
		if lo, hi, ok := h.Geom.Envelope().MinMaxXYs(); ok {
			reach = math.Max(reach, math.Max(math.Max(c.X-lo.X, hi.X-c.X), math.Max(c.Y-lo.Y, hi.Y-c.Y)))
		}
	}
	// The margin keeps each rectangle's edges clear of the hexes it serves,
	// so the rounding where a cut meets the boundary is never on a hex.
	reach = reach*1.01 + 1
	return &localClipper{
		hexes: hexes,
		reach: reach,
		root:  geom.NewEnvelope(xys...),
		sem:   make(chan struct{}, runtime.NumCPU()),
	}
}

// grown is cell expanded by the clipper's reach on every side.
func (lc *localClipper) grown(cell geom.Envelope) geom.Envelope {
	lo, hi, _ := cell.MinMaxXYs()
	return geom.NewEnvelope(
		geom.XY{X: lo.X - lc.reach, Y: lo.Y - lc.reach},
		geom.XY{X: hi.X + lc.reach, Y: hi.Y + lc.reach},
	)
}

// clip clips members (indices of straddling hexes whose centres lie in cell)
// against piece, the boundary already cut to cell's grown rectangle or a
// superset of it, writing each kept hex into out at its index.
func (lc *localClipper) clip(ctx context.Context, cell geom.Envelope, piece geom.Geometry, members []int, depth int, out []*Hex) {
	if ctx.Err() != nil {
		return
	}
	if piece.IsEmpty() {
		return // no boundary near any member: every one is outside
	}
	if len(members) <= localClipLeafHexes || depth >= localClipMaxDepth {
		lc.clipLeaf(piece, members, out)
		return
	}
	lo, hi, _ := cell.MinMaxXYs()
	mid := geom.XY{X: (lo.X + hi.X) / 2, Y: (lo.Y + hi.Y) / 2}
	var quads [4][]int
	for _, i := range members {
		h := &lc.hexes[i]
		q := 0
		if h.CenterX > mid.X {
			q |= 1
		}
		if h.CenterY > mid.Y {
			q |= 2
		}
		quads[q] = append(quads[q], i)
	}
	for q, sub := range quads {
		if len(sub) == 0 {
			continue
		}
		qlo, qhi := lo, mid
		if q&1 != 0 {
			qlo.X, qhi.X = mid.X, hi.X
		}
		if q&2 != 0 {
			qlo.Y, qhi.Y = mid.Y, hi.Y
		}
		subCell := geom.NewEnvelope(qlo, qhi)
		run := func() {
			lc.clip(ctx, subCell, lc.cut(piece, subCell), sub, depth+1, out)
		}
		select {
		case lc.sem <- struct{}{}:
			lc.wg.Go(func() {
				defer func() { <-lc.sem }()
				run()
			})
		default:
			run()
		}
	}
}

// cut is piece cut to cell's grown rectangle, or piece itself when the cut
// fails (the clip is then the same, only slower).
func (lc *localClipper) cut(piece geom.Geometry, cell geom.Envelope) geom.Geometry {
	inter, err := geom.Intersection(piece, lc.grown(cell).AsGeometry())
	if err != nil {
		return piece
	}
	poly, err := RetainPolygonal(inter)
	if err != nil {
		return piece
	}
	return poly
}

// clipLeaf clips each member against the parts of piece its envelope meets,
// as ClipHexesToBoundary clips against the boundary parts it meets.
func (lc *localClipper) clipLeaf(piece geom.Geometry, members []int, out []*Hex) {
	parts := piece.Dump()
	for _, i := range members {
		h := lc.hexes[i]
		env := h.Geom.Envelope()
		var candidates []geom.Geometry
		for _, p := range parts {
			if p.IsPolygon() && p.Envelope().Intersects(env) {
				candidates = append(candidates, p)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		if c, ok := clipHexToCandidates(h, candidates); ok {
			out[i] = &c
		}
	}
}
