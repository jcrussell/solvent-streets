package geo

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/peterstace/simplefeatures/geom"
)

// Hex represents a single hexagon in a flat-top hex grid.
type Hex struct {
	ID      string  // "hex:{col}:{row}"
	CenterX float64 // projected X
	CenterY float64 // projected Y
	Col     int
	Row     int
	Geom    geom.Geometry
}

// HexGrid generates a flat-top hex tiling over a projected bounding box.
// edge is the hex edge length in projected units (meters for UTM).
// Returns hexes that intersect the bounding box.
func HexGrid(minX, minY, maxX, maxY, edge float64) []Hex {
	// Flat-top hex dimensions
	w := 2 * edge            // width of hex
	h := math.Sqrt(3) * edge // height of hex
	colSpacing := w * 3 / 4  // horizontal distance between hex centers
	rowSpacing := h          // vertical distance between hex centers

	// Determine grid bounds with some margin
	startCol := int(math.Floor((minX - edge) / colSpacing))
	endCol := int(math.Ceil((maxX + edge) / colSpacing))
	startRow := int(math.Floor((minY - h/2) / rowSpacing))
	endRow := int(math.Ceil((maxY + h/2) / rowSpacing))

	var hexes []Hex
	for col := startCol; col <= endCol; col++ {
		for row := startRow; row <= endRow; row++ {
			cx := float64(col) * colSpacing
			cy := float64(row) * rowSpacing
			// Odd columns are offset vertically by half a row
			if col%2 != 0 {
				cy += h / 2
			}

			// Quick envelope reject
			if cx+edge < minX || cx-edge > maxX || cy+h/2 < minY || cy-h/2 > maxY {
				continue
			}

			poly := hexPolygon(cx, cy, edge)
			hexes = append(hexes, Hex{
				ID:      fmt.Sprintf("hex:%d:%d", col, row),
				CenterX: cx,
				CenterY: cy,
				Col:     col,
				Row:     row,
				Geom:    poly,
			})
		}
	}
	return hexes
}

// hexPolygon creates a flat-top regular hexagon as a Geometry.
func hexPolygon(cx, cy, edge float64) geom.Geometry {
	// Flat-top hex vertices at angles 0, 60, 120, 180, 240, 300 degrees
	coords := make([]float64, 14) // 7 points * 2 coords (close the ring)
	for i := range 6 {
		angle := float64(i) * math.Pi / 3
		coords[i*2] = cx + edge*math.Cos(angle)
		coords[i*2+1] = cy + edge*math.Sin(angle)
	}
	// Close the ring
	coords[12] = coords[0]
	coords[13] = coords[1]

	seq := geom.NewSequence(coords, geom.DimXY)
	ring := geom.NewLineString(seq)
	poly := geom.NewPolygon([]geom.LineString{ring})
	return poly.AsGeometry()
}

// HexStat holds per-hex coverage statistics.
type HexStat struct {
	HexID        string
	ResourceType string
	Area         float64
	PctCovered   float64
}

// ComputeHexStats intersects each hex with the geometries indexed by idx and
// computes coverage using parallel workers. Candidates returned from the
// R-tree may overlap (e.g. when idx holds buffered feature polygons directly),
// so we union them per-hex before intersecting to avoid double-counting.
// If counter is non-nil it is incremented after each hex is processed.
// ctx cancellation stops dispatching further hexes; in-flight hexes complete.
func ComputeHexStats(ctx context.Context, hexes []Hex, idx *GeomIndex, resourceType string, counter *atomic.Int64) []HexStat {
	return ParallelMap(ctx, hexes, func(_ int, h Hex) []HexStat {
		hexEnv := h.Geom.Envelope()
		candidates := idx.Search(hexEnv)
		if len(candidates) == 0 {
			return nil
		}

		hexArea := h.Geom.Area()
		totalArea, ok := hexCoverageArea(h.Geom, candidates)
		if !ok || totalArea <= 0 {
			return nil
		}
		pct := totalArea / hexArea * 100
		if pct > 100 {
			pct = 100
		}

		return []HexStat{{
			HexID:        h.ID,
			ResourceType: resourceType,
			Area:         totalArea,
			PctCovered:   pct,
		}}
	}, counter)
}

// groupArea is the per-hex, per-group coverage result produced by
// ComputeHexCoverageByGroup before flattening and summing by label.
type groupArea struct {
	label string
	area  float64
}

// ComputeHexCoverageByGroup computes per-group coverage area in a SINGLE pass
// over the hex grid. geoms and labels are parallel slices: geoms[i] belongs to
// group labels[i] (len(geoms) must equal len(labels)). It builds one combined
// R-tree over all geoms, and for each hex searches that index once, groups the
// hex's candidates by label, and computes hexCoverageArea per label for that
// hex. Per-label areas are summed across all hexes into the returned map.
//
// The returned map has exactly ONE KEY PER DISTINCT INPUT LABEL: every label
// appearing in labels is present, and a label whose geoms cover no clipped hex
// (e.g. features that fall in the bbox margin but outside the boundary) maps to
// 0.0 rather than being dropped. This matches the old per-class cohort loop,
// whose callers persist one row and a feature count per distinct class — a
// zero-coverage class must still produce that row.
//
// This is exactly equivalent to running ComputeHexStats once per group against
// a per-group index (same hexCoverageArea clip-first dedup), but pays the
// per-hex envelope + R-tree search ONCE instead of once per group. Each geom
// belongs to exactly one group, so the heavy per-hex union work is partitioned
// by label and not duplicated. Per-label areas therefore equal running
// ComputeHexStats once per label.
//
// Per-group areas are clipped to the same hex grid as a whole-grid "all" total
// would be, so groups sum consistently with that total. Intra-group overlaps
// are dedup'd per-hex (two same-label features overlapping in a hex count once);
// different-label overlaps are counted separately per label.
//
// If counter is non-nil it is incremented after each hex is processed. ctx
// cancellation stops dispatching further hexes; in-flight hexes complete.
func ComputeHexCoverageByGroup(ctx context.Context, hexes []Hex, geoms []geom.Geometry, labels []string, counter *atomic.Int64) map[string]float64 {
	idx := NewGeomIndexFromGeoms(geoms)

	perHex := ParallelMap(ctx, hexes, func(_ int, h Hex) []groupArea {
		ids := idx.SearchIDs(h.Geom.Envelope())
		if len(ids) == 0 {
			return nil
		}
		byLabel := make(map[string][]geom.Geometry)
		for _, id := range ids {
			label := labels[id]
			byLabel[label] = append(byLabel[label], idx.parts[id])
		}
		out := make([]groupArea, 0, len(byLabel))
		for label, groupGeoms := range byLabel {
			area, ok := hexCoverageArea(h.Geom, groupGeoms)
			if !ok || area <= 0 {
				continue
			}
			out = append(out, groupArea{label: label, area: area})
		}
		return out
	}, counter)

	// Pre-seed every distinct input label at 0.0 so the key set equals "one key
	// per distinct label", independent of coverage. A label whose geoms never
	// cover a clipped hex (e.g. features in the bbox margin but outside the
	// boundary) thus survives as a 0.0 entry instead of being dropped. This
	// mirrors the old per-class loop, which keyed off every class regardless of
	// whether it covered any hex. Seed from labels directly so even empty or
	// invalid geoms still produce a key.
	areas := make(map[string]float64)
	for _, label := range labels {
		areas[label] = 0
	}
	for _, ga := range perHex {
		areas[ga.label] += ga.area
	}
	return areas
}

// hexCoverageArea returns the area covered by the candidate features within h.
// It clips first: each candidate is intersected with h, reduced to its
// polygonal parts, and only the small in-hex fragments are unioned. This is
// exactly area-preserving versus unioning the candidates at full extent and
// then intersecting (Area(h ∩ ∪cᵢ) == Area(∪(h ∩ cᵢ)) by distributivity), but
// the union runs on hex-sized inputs instead of re-noding each city-spanning
// buffered feature in every hex its envelope touches.
//
// Unioning the fragments dedupes overlap between adjacent buffered features —
// two roads crossing at a junction share a width² patch that must be counted
// once, not twice. RetainPolygonal drops the 1-D shared-edge artifacts that
// Intersection can emit as mixed-dimension GeometryCollections. UnionMany
// accepts those collections without error (v0.59.0) and Area() ignores their
// 1-D parts, so the strip isn't strictly required for the area this function
// returns; it is kept for parity with clipHexToCandidates, where it IS
// required because the result geometry is retained.
func hexCoverageArea(h geom.Geometry, candidates []geom.Geometry) (float64, bool) {
	var clipped []geom.Geometry
	for _, cand := range candidates {
		inter, err := geom.Intersection(h, cand)
		if err != nil || inter.IsEmpty() {
			continue
		}
		poly, err := RetainPolygonal(inter)
		if err != nil || poly.IsEmpty() {
			continue
		}
		clipped = append(clipped, poly)
	}
	if len(clipped) == 0 {
		return 0, false
	}
	if len(clipped) == 1 {
		return clipped[0].Area(), true
	}
	unioned, err := geom.UnionMany(clipped)
	if err != nil {
		return 0, false
	}
	return unioned.Area(), true
}

// clipHexToCandidates intersects a hex with candidate boundary fragments and
// returns the clipped hex. The second return value is false if the hex has no
// intersection with any candidate.
//
// Each Intersection result is reduced to its polygonal parts via
// RetainPolygonal before merging. When a hex shares a boundary edge segment
// that lies outside the 2-D overlap, geom.Intersection emits a mixed-dimension
// GeometryCollection (the 2-D overlap plus the 1-D shared edge). The strip is
// REQUIRED here: unlike Difference, geom.Union/UnionMany propagate the 1-D
// parts into their output, so without it the retained h.Geom becomes a
// GeometryCollection carrying a stray LineString and serializes as a malformed
// hex feature. (On v0.59.0 the overlay ops no longer error on a mixed GC — this
// is about keeping the stored geometry clean, not avoiding a panic.) For the
// normal clean-polygon case Intersection already returns a pure polygon and
// RetainPolygonal is a no-op.
func clipHexToCandidates(h Hex, candidates []geom.Geometry) (Hex, bool) {
	var clipped geom.Geometry
	for _, cand := range candidates {
		inter, err := geom.Intersection(h.Geom, cand)
		if err != nil || inter.IsEmpty() {
			continue
		}
		poly, err := RetainPolygonal(inter)
		if err != nil || poly.IsEmpty() {
			continue
		}
		clipped = mergeClipped(clipped, poly)
	}
	if clipped.IsEmpty() {
		return h, false
	}
	h.Geom = clipped
	return h, true
}

func mergeClipped(existing, addition geom.Geometry) geom.Geometry {
	if existing.IsEmpty() {
		return addition
	}
	merged, err := geom.Union(existing, addition)
	if err != nil {
		if addition.Area() > existing.Area() {
			return addition
		}
		return existing
	}
	return merged
}

// boundarySegments decomposes every ring (exterior + interior) of every
// Polygon in g into 2-point LineStrings, one per consecutive vertex pair.
// Indexing these instead of whole rings lets a hex-envelope Search return only
// the boundary edges near that hex (a thin perimeter band) rather than the
// entire boundary, which is what makes interior-hex pruning possible.
func boundarySegments(g geom.Geometry) []geom.Geometry {
	var segs []geom.Geometry
	for _, part := range g.Dump() {
		if !part.IsPolygon() {
			continue
		}
		poly, ok := part.AsPolygon()
		if !ok {
			continue
		}
		for _, ring := range poly.DumpRings() {
			seq := ring.Coordinates()
			n := seq.Length()
			for i := 0; i+1 < n; i++ {
				a := seq.GetXY(i)
				b := seq.GetXY(i + 1)
				coords := []float64{a.X, a.Y, b.X, b.Y}
				ls := geom.NewLineString(geom.NewSequence(coords, geom.DimXY))
				segs = append(segs, ls.AsGeometry())
			}
		}
	}
	return segs
}

// ClipHexesToBoundary clips each hex against the boundary geometry in parallel.
// Hexes wholly outside the boundary are dropped, hexes straddling it are
// clipped, and hexes wholly inside are kept unchanged. If counter is non-nil
// it is incremented after each hex is processed. ctx cancellation stops
// dispatching further hexes; in-flight hexes complete.
//
// The grid is generated over the boundary's own bbox, so the vast majority of
// hexes are fully interior and intersecting them returns the hex unchanged.
// To avoid paying a full overlay against the multi-thousand-vertex boundary
// for every such hex, we index the boundary's individual EDGE SEGMENTS (not
// whole rings). A hex whose envelope touches no boundary edge cannot transition
// in/out of the boundary anywhere inside it — every point of the hex is on the
// same side — so a single point-in-polygon test of the hex center classifies
// it: inside keeps it unclipped, outside drops it. Only hexes whose envelope
// actually contains a boundary edge run the full intersection clip.
//
// CORRECTNESS INVARIANT: the center-classification fast path runs ONLY when no
// boundary edge lies in the hex envelope. A concave notch puts boundary edges
// into the envelope of any hex near it, so such a hex always falls through to
// the full-intersection path. The classic "hex center is inside but the
// envelope straddles a concavity" failure mode therefore CANNOT occur here.
// Segment-bbox overlap is a conservative superset (a segment whose bbox touches
// hexEnv may not actually enter the hex) — those hexes merely get the correct
// but slightly more expensive full intersection; never the reverse.
func ClipHexesToBoundary(ctx context.Context, hexes []Hex, boundary geom.Geometry, counter *atomic.Int64) []Hex {
	// Edge index: 2-point segments of every ring, for proximity pruning.
	edgeIdx := NewGeomIndexFromGeoms(boundarySegments(boundary))
	// Polygon-part index: whole boundary parts, for clipping straddling hexes.
	polyIdx := NewGeomIndex(boundary)

	return ParallelMap(ctx, hexes, func(_ int, h Hex) []Hex {
		hexEnv := h.Geom.Envelope()

		if len(edgeIdx.Search(hexEnv)) == 0 {
			// No boundary edge in the hex envelope: the whole hex is on one
			// side of the boundary. Classify by the hex center.
			center := h.Geom.Centroid().AsGeometry()
			if geom.Intersects(center, boundary) {
				return []Hex{h} // fully interior, keep unclipped
			}
			return nil // fully exterior, drop
		}

		// Boundary edges straddle the hex envelope: clip against the
		// overlapping boundary polygon part(s) exactly as before.
		candidates := polyIdx.Search(hexEnv)
		if len(candidates) == 0 {
			return nil
		}
		clipped, ok := clipHexToCandidates(h, candidates)
		if !ok {
			return nil
		}
		return []Hex{clipped}
	}, counter)
}
