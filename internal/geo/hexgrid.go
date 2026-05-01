package geo

import (
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
	AreaSqM      float64
	PctCovered   float64
}

// ComputeHexStats intersects each hex with the geometries indexed by idx and
// computes coverage using parallel workers. Candidates returned from the
// R-tree may overlap (e.g. when idx holds buffered feature polygons directly),
// so we union them per-hex before intersecting to avoid double-counting.
// If counter is non-nil it is incremented after each hex is processed.
func ComputeHexStats(hexes []Hex, idx *GeomIndex, resourceType string, counter *atomic.Int64) []HexStat {
	return ParallelMap(hexes, func(_ int, h Hex) []HexStat {
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
			AreaSqM:      totalArea,
			PctCovered:   pct,
		}}
	}, counter)
}

// hexCoverageArea returns the area of intersection between h and the union of
// candidates. Unioning locally (tens-to-hundreds of polygons per hex) dedupes
// overlap between adjacent buffered features — two roads crossing at a
// junction share a width² patch that must be counted once, not twice.
func hexCoverageArea(h geom.Geometry, candidates []geom.Geometry) (float64, bool) {
	if len(candidates) == 0 {
		return 0, false
	}
	var unioned geom.Geometry
	if len(candidates) == 1 {
		unioned = candidates[0]
	} else {
		u, err := geom.UnionMany(candidates)
		if err != nil {
			return 0, false
		}
		unioned = u
	}
	inter, err := geom.Intersection(h, unioned)
	if err != nil || inter.IsEmpty() {
		return 0, false
	}
	return inter.Area(), true
}

// clipHexToCandidates intersects a hex with candidate boundary fragments and
// returns the clipped hex. The second return value is false if the hex has no
// intersection with any candidate.
func clipHexToCandidates(h Hex, candidates []geom.Geometry) (Hex, bool) {
	var clipped geom.Geometry
	for _, cand := range candidates {
		inter, err := geom.Intersection(h.Geom, cand)
		if err != nil || inter.IsEmpty() {
			continue
		}
		clipped = mergeClipped(clipped, inter)
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

// ClipHexesToBoundary intersects each hex with the boundary geometry in
// parallel using a spatial index. Hexes with no intersection are dropped;
// hexes partially inside are clipped. If counter is non-nil it is incremented
// after each hex is processed.
func ClipHexesToBoundary(hexes []Hex, boundary geom.Geometry, counter *atomic.Int64) []Hex {
	idx := NewGeomIndex(boundary)

	return ParallelMap(hexes, func(_ int, h Hex) []Hex {
		hexEnv := h.Geom.Envelope()
		candidates := idx.Search(hexEnv)
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
