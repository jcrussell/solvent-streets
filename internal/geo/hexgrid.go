package geo

import (
	"fmt"
	"math"

	"github.com/peterstace/simplefeatures/geom"
)

// Hex represents a single hexagon in a flat-top hex grid.
type Hex struct {
	ID      string       // "hex:{col}:{row}"
	CenterX float64      // projected X
	CenterY float64      // projected Y
	Col     int
	Row     int
	Geom    geom.Geometry
}

// HexGrid generates a flat-top hex tiling over a projected bounding box.
// edge is the hex edge length in projected units (meters for UTM).
// Returns hexes that intersect the bounding box.
func HexGrid(minX, minY, maxX, maxY, edge float64) []Hex {
	// Flat-top hex dimensions
	w := 2 * edge                        // width of hex
	h := math.Sqrt(3) * edge             // height of hex
	colSpacing := w * 3 / 4              // horizontal distance between hex centers
	rowSpacing := h                      // vertical distance between hex centers

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
	for i := 0; i < 6; i++ {
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
	AreaSqFt     float64
	PctCovered   float64
}

// ComputeHexStats intersects each hex with the union geometry and computes coverage.
func ComputeHexStats(hexes []Hex, union geom.Geometry, resourceType string, proj Projector) []HexStat {
	unionEnv := union.Envelope()

	var stats []HexStat
	for _, h := range hexes {
		hexEnv := h.Geom.Envelope()

		// Fast envelope intersection check
		if !envelopesIntersect(unionEnv, hexEnv) {
			continue
		}

		intersection, err := geom.Intersection(h.Geom, union)
		if err != nil || intersection.IsEmpty() {
			continue
		}

		areaProjected := intersection.Area()
		if areaProjected <= 0 {
			continue
		}

		hexAreaProjected := h.Geom.Area()
		pct := 0.0
		if hexAreaProjected > 0 {
			pct = areaProjected / hexAreaProjected * 100
		}

		areaSqFt := AreaSqFtFromProjected(areaProjected, proj)

		stats = append(stats, HexStat{
			HexID:        h.ID,
			ResourceType: resourceType,
			AreaSqFt:     areaSqFt,
			PctCovered:   pct,
		})
	}
	return stats
}

func envelopesIntersect(a, b geom.Envelope) bool {
	aMin, aMax, aOk := a.MinMaxXYs()
	bMin, bMax, bOk := b.MinMaxXYs()
	if !aOk || !bOk {
		return false
	}
	return aMin.X <= bMax.X && aMax.X >= bMin.X &&
		aMin.Y <= bMax.Y && aMax.Y >= bMin.Y
}
