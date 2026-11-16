package geo

import (
	"fmt"

	"github.com/peterstace/simplefeatures/geom"
)

// LonLatPolygon is a polygon as rings of [lon, lat] (outer first, then
// holes), each ring closed.
type LonLatPolygon [][][2]float64

// IntersectLonLat returns a ∩ b for polygon sets given in lon/lat, treated
// as planar coordinates (no projection): the water strip's coastline faces
// (internal/ingest) are cut along the coastline's own lon/lat vertices, so
// the pieces tile their parent exactly. Each input is cleaned
// (ValidatePolygon) before the overlay, and only the 2-D part of the result
// is kept (RetainPolygonal). An empty result is a nil slice.
func IntersectLonLat(a, b []LonLatPolygon) ([]LonLatPolygon, error) {
	ga, err := lonLatGeometry(a)
	if err != nil {
		return nil, fmt.Errorf("first operand: %w", err)
	}
	gb, err := lonLatGeometry(b)
	if err != nil {
		return nil, fmt.Errorf("second operand: %w", err)
	}
	if ga.IsEmpty() || gb.IsEmpty() {
		return nil, nil
	}
	inter, err := geom.Intersection(ga, gb)
	if err != nil {
		return nil, fmt.Errorf("intersection: %w", err)
	}
	inter, err = RetainPolygonal(inter)
	if err != nil {
		return nil, fmt.Errorf("intersection (polygonal-only): %w", err)
	}
	return lonLatPolygons(inter), nil
}

func lonLatGeometry(polys []LonLatPolygon) (geom.Geometry, error) {
	var ps []geom.Polygon
	for _, p := range polys {
		if len(p) == 0 || len(p[0]) < 4 {
			continue
		}
		rings := make([]geom.LineString, len(p))
		for i, r := range p {
			rings[i] = geom.NewLineString(coordsToSequence(r))
		}
		ps = append(ps, geom.NewPolygon(rings))
	}
	if len(ps) == 0 {
		return geom.Geometry{}, nil
	}
	return ValidatePolygon(geom.NewMultiPolygon(ps).AsGeometry())
}

func lonLatPolygons(g geom.Geometry) []LonLatPolygon {
	var out []LonLatPolygon
	for _, part := range g.Dump() {
		poly, ok := part.AsPolygon()
		if !ok || poly.IsEmpty() {
			continue
		}
		var p LonLatPolygon
		for _, ring := range poly.DumpRings() {
			seq := ring.Coordinates()
			r := make([][2]float64, seq.Length())
			for i := range r {
				xy := seq.GetXY(i)
				r[i] = [2]float64{xy.X, xy.Y}
			}
			p = append(p, r)
		}
		out = append(out, p)
	}
	return out
}
