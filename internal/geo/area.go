package geo

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
	return AreaInProjectedUnits(g), nil
}
