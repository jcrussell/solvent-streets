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

	// Clip the subtrahend to the boundary before subtracting. Any
	// subtrahend area outside the boundary cannot affect the difference
	// mathematically, but a malformed subtrahend (e.g. a mis-stitched
	// OSM water polygon covering most of the bbox by accident) only gets
	// to delete what it actually intersects — never phantom area outside
	// the city. This is the last defense behind per-polygon validation
	// in internal/ingest/water.go: even if a bad polygon leaks through
	// every upstream check, it can only carve out city interior, not
	// reduce the boundary to a sliver. Part of solvent-streets-vtcs.
	otherClipped, err := geom.Intersection(other, boundary)
	if err != nil {
		return "", fmt.Errorf("clip subtrahend to boundary: %w", err)
	}
	if otherClipped.IsEmpty() {
		// Subtrahend lies entirely outside the boundary; nothing to do.
		return GeometryToGeoJSON(boundary, proj)
	}

	diff, err := geom.Difference(boundary, otherClipped)
	if err != nil {
		return "", fmt.Errorf("difference: %w", err)
	}
	if diff.IsEmpty() {
		return "", errors.New("difference is empty (subtrahend covers boundary)")
	}

	return GeometryToGeoJSON(diff, proj)
}
