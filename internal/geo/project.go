package geo

// Projector converts between WGS84 (lon/lat degrees) and a projected coordinate system.
type Projector interface {
	// ToProjected converts WGS84 (lon, lat) degrees to projected (x, y).
	ToProjected(lon, lat float64) (x, y float64, err error)
	// FromProjected converts projected (x, y) to WGS84 (lon, lat) degrees.
	FromProjected(x, y float64) (lon, lat float64, err error)
}
