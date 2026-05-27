package geo

import (
	"errors"
	"fmt"

	"github.com/peterstace/simplefeatures/geom"
)

// BoundaryAreaSqM computes the area in square meters of a GeoJSON boundary polygon.
func BoundaryAreaSqM(boundaryGJSON string) (float64, error) {
	bbox, err := BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return 0, err
	}
	lon, lat := CenterFromBBox(bbox)
	proj := NewUTMProjector(lon, lat)
	g, _, err := GeoJSONToProjectedGeometry(boundaryGJSON, proj)
	if err != nil {
		return 0, err
	}
	if g.IsEmpty() {
		return 0, nil
	}
	return g.Area(), nil
}

// InteriorPoints returns one lon/lat point per sub-polygon of the
// polygon or multipolygon described by boundaryGJSON. For a Polygon
// the slice has one element; for a MultiPolygon it has one element
// per sub-polygon. Each point is computed via simplefeatures'
// PointOnSurface, which guarantees a point in the sub-polygon's
// interior (not on its boundary).
//
// Callers treat the returned points as presumed-land probes: a
// candidate water polygon that contains any of them is land-side
// (wrong). Sampling per sub-polygon catches cities whose boundary
// includes multiple disjoint landmasses (NYC's five boroughs, SF
// proper vs. Treasure Island), where a single point would miss
// landmasses other than the largest.
//
// Returns an error when the GeoJSON cannot be parsed, the geometry
// is empty, or the type is neither Polygon nor MultiPolygon.
func InteriorPoints(boundaryGJSON string) ([][2]float64, error) {
	g, err := geom.UnmarshalGeoJSON([]byte(boundaryGJSON))
	if err != nil {
		return nil, fmt.Errorf("parse geojson: %w", err)
	}
	if g.IsEmpty() {
		return nil, errors.New("empty geometry")
	}
	var probes [][2]float64
	add := func(p geom.Point) {
		xy, ok := p.XY()
		if ok {
			probes = append(probes, [2]float64{xy.X, xy.Y})
		}
	}
	switch g.Type() { //nolint:exhaustive // boundary geometries are only ever Polygon/MultiPolygon; other types fall through to the default error
	case geom.TypePolygon:
		add(g.MustAsPolygon().PointOnSurface())
	case geom.TypeMultiPolygon:
		mp := g.MustAsMultiPolygon()
		for i := range mp.NumPolygons() {
			poly := mp.PolygonN(i)
			if poly.IsEmpty() {
				continue
			}
			add(poly.PointOnSurface())
		}
	default:
		return nil, fmt.Errorf("unsupported geometry type for interior points: %s", g.Type())
	}
	if len(probes) == 0 {
		return nil, errors.New("no interior points produced")
	}
	return probes, nil
}
