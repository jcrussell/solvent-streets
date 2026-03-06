package geo

import "math"

// EPSG:2227 - California State Plane Zone 3, NAD83, US Survey Feet
// Lambert Conformal Conic projection parameters
const (
	// Semi-major axis (GRS80)
	a = 6378137.0
	// Flattening
	f = 1 / 298.257222101
	// Eccentricity squared
	e2 = 2*f - f*f

	// EPSG:2227 parameters
	lat1 = 37.06666666666667 * math.Pi / 180 // Standard parallel 1
	lat2 = 38.43333333333333 * math.Pi / 180 // Standard parallel 2
	lat0 = 36.5 * math.Pi / 180              // Latitude of origin
	lon0 = -120.5 * math.Pi / 180            // Central meridian

	fe = 6561666.667 * 0.3048006096012192 // False easting (converted from US survey feet to meters, then back)
	fn = 1640416.667 * 0.3048006096012192 // False northing

	// US survey foot
	usSurveyFoot = 1200.0 / 3937.0
)

var (
	e      = math.Sqrt(e2)
	projN  float64
	projF  float64
	projR0 float64
)

func init() {
	m1 := msfn(lat1)
	m2 := msfn(lat2)
	t0 := tsfn(lat0)
	t1 := tsfn(lat1)
	t2 := tsfn(lat2)

	projN = (math.Log(m1) - math.Log(m2)) / (math.Log(t1) - math.Log(t2))
	projF = m1 / (projN * math.Pow(t1, projN))
	projR0 = a * projF * math.Pow(t0, projN)
}

func msfn(lat float64) float64 {
	sinlat := math.Sin(lat)
	return math.Cos(lat) / math.Sqrt(1-e2*sinlat*sinlat)
}

func tsfn(lat float64) float64 {
	sinlat := math.Sin(lat)
	return math.Tan(math.Pi/4-lat/2) / math.Pow((1-e*sinlat)/(1+e*sinlat), e/2)
}

// ToStatePlane converts WGS84 (lon, lat) in degrees to EPSG:2227 (x, y) in US survey feet.
func ToStatePlane(lon, lat float64) (x, y float64) {
	latRad := lat * math.Pi / 180
	lonRad := lon * math.Pi / 180

	t := tsfn(latRad)
	r := a * projF * math.Pow(t, projN)
	theta := projN * (lonRad - lon0)

	easting := r*math.Sin(theta) + 6561666.667*usSurveyFoot
	northing := projR0 - r*math.Cos(theta) + 1640416.667*usSurveyFoot

	// Convert from meters to US survey feet
	x = easting / usSurveyFoot
	y = northing / usSurveyFoot
	return x, y
}

// ToWGS84 converts EPSG:2227 (x, y) in US survey feet to WGS84 (lon, lat) in degrees.
func ToWGS84(x, y float64) (lon, lat float64) {
	// Convert from US survey feet to meters
	xm := x * usSurveyFoot
	ym := y * usSurveyFoot

	xm -= 6561666.667 * usSurveyFoot
	ym -= 1640416.667 * usSurveyFoot

	rp := projR0 - ym
	thetap := math.Atan2(xm, rp)

	sign := 1.0
	if projN < 0 {
		sign = -1.0
	}
	r := sign * math.Sqrt(xm*xm+rp*rp)
	t := math.Pow(r/(a*projF), 1.0/projN)

	// Iterative latitude computation
	latRad := math.Pi/2 - 2*math.Atan(t)
	for i := 0; i < 15; i++ {
		sinlat := math.Sin(latRad)
		newLat := math.Pi/2 - 2*math.Atan(t*math.Pow((1-e*sinlat)/(1+e*sinlat), e/2))
		if math.Abs(newLat-latRad) < 1e-15 {
			break
		}
		latRad = newLat
	}

	lonRad := thetap/projN + lon0

	return lonRad * 180 / math.Pi, latRad * 180 / math.Pi
}
