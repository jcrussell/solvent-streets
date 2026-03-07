package geo

import "math"

// UTMProjector implements the Projector interface using UTM Transverse Mercator.
// Zone is auto-detected from longitude. Works anywhere with ~1m accuracy.
type UTMProjector struct {
	Zone     int
	Northern bool // true for northern hemisphere
}

// NewUTMProjector creates a UTM projector for the given lon/lat center point.
func NewUTMProjector(lon, lat float64) *UTMProjector {
	zone := int(math.Floor((lon+180)/6)) + 1
	return &UTMProjector{
		Zone:     zone,
		Northern: lat >= 0,
	}
}

// WGS84 ellipsoid constants
const (
	a  = 6378137.0           // Semi-major axis (GRS80/WGS84)
	f  = 1 / 298.257222101   // Flattening
	e2 = 2*f - f*f           // Eccentricity squared
)

// UTM constants
const (
	utmK0 = 0.9996
	utmFE = 500000.0 // false easting in meters
	utmFN = 10000000.0 // false northing for southern hemisphere
)

func (u *UTMProjector) centralMeridian() float64 {
	return float64(u.Zone*6-183) * math.Pi / 180
}

// ToProjected converts WGS84 (lon, lat) degrees to UTM (easting, northing) in meters.
func (u *UTMProjector) ToProjected(lon, lat float64) (float64, float64, error) {
	latRad := lat * math.Pi / 180
	lonRad := lon * math.Pi / 180
	lon0 := u.centralMeridian()

	sinLat := math.Sin(latRad)
	cosLat := math.Cos(latRad)
	tanLat := math.Tan(latRad)

	ep2 := e2 / (1 - e2) // e'^2
	N := a / math.Sqrt(1-e2*sinLat*sinLat)
	T := tanLat * tanLat
	C := ep2 * cosLat * cosLat
	A := (lonRad - lon0) * cosLat

	// Meridional arc length
	M := meridionalArc(latRad)

	A2 := A * A
	A3 := A2 * A
	A4 := A3 * A
	A5 := A4 * A
	A6 := A5 * A

	x := utmFE + utmK0*N*(A+(1-T+C)*A3/6+(5-18*T+T*T+72*C-58*ep2)*A5/120)

	y := utmK0 * (M + N*tanLat*(A2/2+(5-T+9*C+4*C*C)*A4/24+(61-58*T+T*T+600*C-330*ep2)*A6/720))
	if !u.Northern {
		y += utmFN
	}

	return x, y, nil
}

// FromProjected converts UTM (easting, northing) in meters to WGS84 (lon, lat) degrees.
func (u *UTMProjector) FromProjected(x, y float64) (float64, float64, error) {
	lon0 := u.centralMeridian()

	x -= utmFE
	if !u.Northern {
		y -= utmFN
	}

	M := y / utmK0
	mu := M / (a * (1 - e2/4 - 3*e2*e2/64 - 5*e2*e2*e2/256))

	e1 := (1 - math.Sqrt(1-e2)) / (1 + math.Sqrt(1-e2))
	e12 := e1 * e1
	e13 := e12 * e1
	e14 := e13 * e1

	lat1 := mu + (3*e1/2-27*e13/32)*math.Sin(2*mu) +
		(21*e12/16-55*e14/32)*math.Sin(4*mu) +
		(151*e13/96)*math.Sin(6*mu) +
		(1097*e14/512)*math.Sin(8*mu)

	sinLat1 := math.Sin(lat1)
	cosLat1 := math.Cos(lat1)
	tanLat1 := math.Tan(lat1)

	ep2 := e2 / (1 - e2)
	N1 := a / math.Sqrt(1-e2*sinLat1*sinLat1)
	T1 := tanLat1 * tanLat1
	C1 := ep2 * cosLat1 * cosLat1
	R1 := a * (1 - e2) / math.Pow(1-e2*sinLat1*sinLat1, 1.5)
	D := x / (N1 * utmK0)

	D2 := D * D
	D3 := D2 * D
	D4 := D3 * D
	D5 := D4 * D
	D6 := D5 * D

	latRad := lat1 - (N1*tanLat1/R1)*(D2/2-(5+3*T1+10*C1-4*C1*C1-9*ep2)*D4/24+(61+90*T1+298*C1+45*T1*T1-252*ep2-3*C1*C1)*D6/720)

	lonRad := lon0 + (D-(1+2*T1+C1)*D3/6+(5-2*C1+28*T1-3*C1*C1+8*ep2+24*T1*T1)*D5/120)/cosLat1

	return lonRad * 180 / math.Pi, latRad * 180 / math.Pi, nil
}

// Unit returns the projection unit name.
func (u *UTMProjector) Unit() string { return "meters" }

// meridionalArc computes the meridional arc length from the equator to latitude phi.
func meridionalArc(phi float64) float64 {
	e4 := e2 * e2
	e6 := e4 * e2
	return a * ((1-e2/4-3*e4/64-5*e6/256)*phi -
		(3*e2/8+3*e4/32+45*e6/1024)*math.Sin(2*phi) +
		(15*e4/256+45*e6/1024)*math.Sin(4*phi) -
		(35*e6/3072)*math.Sin(6*phi))
}
