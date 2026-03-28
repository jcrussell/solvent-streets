package geo

// BoundaryAreaSqFt computes the area in square feet of a GeoJSON boundary polygon.
func BoundaryAreaSqFt(boundaryGJSON string) (float64, error) {
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
	return AreaSqFtFromProjected(AreaInProjectedUnits(g), proj), nil
}
