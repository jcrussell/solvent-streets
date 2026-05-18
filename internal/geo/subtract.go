package geo

import (
	"errors"
	"fmt"

	"github.com/peterstace/simplefeatures/geom"
)

// SubtractGeoJSON returns boundaryGJSON with otherGJSON subtracted, both as
// lat/lon Polygon or MultiPolygon GeoJSON strings. Set operations run in a
// UTM projection picked from the boundary's bbox center so areas and
// intersections are metric-accurate; the result is reprojected back to
// lat/lon for storage.
//
// Returns boundaryGJSON unchanged when otherGJSON is empty so callers can
// pass an empty Overpass result through without branching.
func SubtractGeoJSON(boundaryGJSON, otherGJSON string) (string, error) {
	if boundaryGJSON == "" {
		return "", errors.New("boundary geojson is empty")
	}
	if otherGJSON == "" {
		return boundaryGJSON, nil
	}

	bbox, err := BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return "", fmt.Errorf("boundary bbox: %w", err)
	}
	lon, lat := CenterFromBBox(bbox)
	proj := NewUTMProjector(lon, lat)

	boundary, _, err := GeoJSONToProjectedGeometry(boundaryGJSON, proj)
	if err != nil {
		return "", fmt.Errorf("project boundary: %w", err)
	}
	other, _, err := GeoJSONToProjectedGeometry(otherGJSON, proj)
	if err != nil {
		return "", fmt.Errorf("project subtrahend: %w", err)
	}

	// ValidatePolygon (Buffer(0)) snaps near-collinear vertices and removes
	// self-intersection artifacts that crash JTS overlay. Real-world OSM
	// water polygons regularly have these defects.
	boundary, err = ValidatePolygon(boundary)
	if err != nil {
		return "", fmt.Errorf("clean boundary: %w", err)
	}
	other, err = ValidatePolygon(other)
	if err != nil {
		return "", fmt.Errorf("clean subtrahend: %w", err)
	}

	diff, err := geom.Difference(boundary, other)
	if err != nil {
		return "", fmt.Errorf("difference: %w", err)
	}
	if diff.IsEmpty() {
		return "", errors.New("difference is empty (subtrahend covers boundary)")
	}

	return GeometryToGeoJSON(diff, proj)
}
