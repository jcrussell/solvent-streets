package geo

import (
	"fmt"
	"math"

	"github.com/peterstace/simplefeatures/carto"
	"github.com/peterstace/simplefeatures/geom"
)

// UTMProjector converts between WGS84 (lon/lat degrees) and UTM Transverse
// Mercator (meters). Zone is auto-detected from longitude. Works anywhere
// with ~1m accuracy. The projection math is delegated to simplefeatures'
// carto package; this type adapts it to our (lon, lat) float64 ergonomics and
// exposes the resolved Zone/Northern for callers that need them.
//
// INVARIANT: always construct via NewUTMProjector. The unexported carto
// projection is required by ToProjected/FromProjected, so a zero-value or
// struct-literal UTMProjector (e.g. &UTMProjector{Zone: 10}) is invalid and
// will nil-panic on first use. Zone/Northern are exported for reading only;
// setting them by hand does not configure the projection.
type UTMProjector struct {
	Zone     int
	Northern bool // true for northern hemisphere

	utm *carto.UTM // the actual projection; nil unless built by NewUTMProjector
}

// NewUTMProjector creates a UTM projector for the given lon/lat center point.
func NewUTMProjector(lon, lat float64) *UTMProjector {
	// Resolve the zone ourselves rather than via carto.NewUTMFromLocation: that
	// constructor returns an error for lon/lat outside [-180,180]/[-80,84] and
	// applies the Norway/Svalbard zone exceptions, neither of which we want. We
	// clamp out-of-range longitudes into [1,60] (TestUTMZone_Antimeridian pins
	// this) and use plain 6° bands everywhere.
	zone := int(math.Floor((lon+180)/6)) + 1
	zone = max(zone, 1)
	zone = min(zone, 60)
	northern := lat >= 0

	hemi := "N"
	if !northern {
		hemi = "S"
	}
	// Feed the clamped zone to carto via its code-string constructor (carto.UTM
	// has unexported fields, so NewUTMFromCode is the only way to build one from
	// a zone we computed). The code is always well-formed here — zone is in
	// [1,60] and hemi is "N"/"S" — so the error is unreachable; we panic rather
	// than thread an error through this total constructor. Anyone who later
	// loosens the zone clamp must revisit this (and the constructor signature).
	u, err := carto.NewUTMFromCode(fmt.Sprintf("%02d%s", zone, hemi))
	if err != nil {
		panic(fmt.Sprintf("geo: invalid UTM code for zone %d hemi %s: %v", zone, hemi, err))
	}

	return &UTMProjector{
		Zone:     zone,
		Northern: northern,
		utm:      u,
	}
}

// ToProjected converts WGS84 (lon, lat) degrees to UTM (easting, northing) in meters.
func (u *UTMProjector) ToProjected(lon, lat float64) (float64, float64) {
	xy := u.utm.Forward(geom.XY{X: lon, Y: lat})
	return xy.X, xy.Y
}

// FromProjected converts UTM (easting, northing) in meters to WGS84 (lon, lat) degrees.
func (u *UTMProjector) FromProjected(x, y float64) (float64, float64) {
	ll := u.utm.Reverse(geom.XY{X: x, Y: y})
	return ll.X, ll.Y
}
