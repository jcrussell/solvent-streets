package db

import (
	"errors"
	"fmt"

	"github.com/peterstace/simplefeatures/geom"
)

// geometryPrecision is the number of decimal places features.geometry keeps:
// 1e-7 degrees (about 1 cm), OSM's own precision. TWKB records the precision
// in each geometry's header, so a source that needs more can be stored at
// another precision without changing the rows already stored.
const geometryPrecision = 7

// encodeGeometry is a feature's GeoJSON geometry as stored: TWKB at
// geometryPrecision. The GeoJSON is not validated, because OSM holds invalid
// shapes (self-intersecting polygons) that must round-trip as they came.
func encodeGeometry(geoJSON string) ([]byte, error) {
	if geoJSON == "" {
		return nil, errors.New("geometry: empty GeoJSON")
	}
	g, err := geom.UnmarshalGeoJSON([]byte(geoJSON), geom.NoValidate{})
	if err != nil {
		return nil, fmt.Errorf("geometry: parse GeoJSON: %w", err)
	}
	var opts []geom.TWKBWriterOption
	if hasOpenRing(g) {
		// TWKB leaves out a ring's closing point and the reader closes the
		// ring with its first one, which would drop the last vertex of a ring
		// that was never closed. Writing every point keeps them all; the
		// reader then appends the closing point.
		opts = append(opts, geom.TWKBCloseRings())
	}
	b, err := geom.MarshalTWKB(g, geometryPrecision, opts...)
	if err != nil {
		return nil, fmt.Errorf("geometry: encode TWKB: %w", err)
	}
	return b, nil
}

// decodeGeometry reads a stored geometry, unvalidated like encodeGeometry.
func decodeGeometry(b []byte) (geom.Geometry, error) {
	g, err := geom.UnmarshalTWKB(b, geom.NoValidate{})
	if err != nil {
		return geom.Geometry{}, fmt.Errorf("geometry: decode TWKB: %w", err)
	}
	return g, nil
}

// geometryGeoJSON is a stored geometry as the GeoJSON string Feature carries.
func geometryGeoJSON(b []byte) (string, error) {
	g, err := decodeGeometry(b)
	if err != nil {
		return "", err
	}
	raw, err := g.MarshalJSON()
	if err != nil {
		return "", fmt.Errorf("geometry: marshal GeoJSON: %w", err)
	}
	return string(raw), nil
}

// hasOpenRing reports whether any polygon ring of g has distinct first and
// last points.
func hasOpenRing(g geom.Geometry) bool {
	switch g.Type() {
	case geom.TypePolygon:
		return polygonHasOpenRing(g.MustAsPolygon())
	case geom.TypeMultiPolygon:
		mp := g.MustAsMultiPolygon()
		for i := range mp.NumPolygons() {
			if polygonHasOpenRing(mp.PolygonN(i)) {
				return true
			}
		}
	case geom.TypeGeometryCollection:
		gc := g.MustAsGeometryCollection()
		for i := range gc.NumGeometries() {
			if hasOpenRing(gc.GeometryN(i)) {
				return true
			}
		}
	case geom.TypePoint, geom.TypeLineString, geom.TypeMultiPoint, geom.TypeMultiLineString:
		// no rings
	}
	return false
}

func polygonHasOpenRing(p geom.Polygon) bool {
	for _, seq := range p.Coordinates() {
		if n := seq.Length(); n >= 2 && seq.Get(0) != seq.Get(n-1) {
			return true
		}
	}
	return false
}
