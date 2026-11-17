package db

import (
	"errors"
	"fmt"

	"github.com/peterstace/simplefeatures/geom"
)

// geometryPrecision is the number of decimal places features.geometry keeps:
// 1e-7 degrees (about 1 cm), OSM's own precision. It is also TWKB's maximum:
// the header holds the precision as a 4-bit zigzag (-8..7), so a source that
// needs finer coordinates would need another encoding.
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
	// TWKB normally leaves out a ring's closing point and the reader closes
	// the ring again from its first point, which loses a doubled closing
	// vertex or a 2-point ring. Writing every point keeps each ring as it
	// came, except one whose ends differ (open, or apart only in Z): the
	// reader appends the first point to that one.
	b, err := geom.MarshalTWKB(g, geometryPrecision, geom.TWKBCloseRings())
	if err != nil {
		return nil, fmt.Errorf("geometry: encode TWKB: %w", err)
	}
	return b, nil
}

// CheckGeometry reports whether UpsertFeatures would store geoJSON, with the
// same error it would fail on. Ingest calls it on geometry it did not build
// itself (ArcGIS, TIGERweb), to drop one bad row instead of failing a layer.
func CheckGeometry(geoJSON string) error {
	_, err := encodeGeometry(geoJSON)
	return err
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
