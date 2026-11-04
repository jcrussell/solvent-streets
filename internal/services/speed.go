package services

import (
	"strconv"
	"strings"
)

// SpeedTable maps an OSM highway class to a travel speed in km/h, with a
// Default for classes it does not list.
type SpeedTable struct {
	ByClass map[string]float64
	Default float64
}

// DefaultRoadSpeeds are free-flow approximations by highway class, in km/h.
// They are deliberately close to typical posted limits rather than the higher
// speeds an emergency vehicle may legally drive: intersections, turns and
// traffic roughly cancel the exemption, and fire-service planning commonly
// assumes ~35 mph (56 km/h) averages on arterials. A `maxspeed` tag, when it
// parses, overrides the class value (see SpeedKmh).
var DefaultRoadSpeeds = SpeedTable{
	ByClass: map[string]float64{
		"motorway":       100,
		"motorway_link":  60,
		"trunk":          80,
		"trunk_link":     50,
		"primary":        65,
		"primary_link":   45,
		"secondary":      55,
		"secondary_link": 40,
		"tertiary":       45,
		"tertiary_link":  35,
		"unclassified":   40,
		"residential":    40,
		"road":           40,
		"living_street":  20,
		"service":        20,
	},
	Default: 30,
}

// SpeedKmh returns the travel speed for a road with the given OSM tags: the
// parsed `maxspeed` when present and sensible, else the class speed, else
// Default. Roads from a non-OSM source (ArcGIS centerlines carry agency
// fields, not OSM tags) therefore get Default.
func (t SpeedTable) SpeedKmh(tags map[string]string) float64 {
	if v, ok := ParseMaxspeed(tags["maxspeed"]); ok {
		return v
	}
	if v, ok := t.ByClass[tags["highway"]]; ok {
		return v
	}
	return t.Default
}

// mphToKmh converts statute miles per hour to km/h.
const mphToKmh = 1.609344

// ParseMaxspeed parses an OSM maxspeed value into km/h: a bare number is
// km/h ("50"), "NN mph" is converted, "NN km/h" is accepted. Anything else —
// "signals", "none", "walk", a range or list ("30;50"), a zero or absurd value
// — is not a speed and returns ok=false so the caller falls back to the class
// table.
func ParseMaxspeed(s string) (float64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	factor := 1.0
	switch {
	case strings.HasSuffix(s, "mph"):
		factor = mphToKmh
		s = strings.TrimSpace(strings.TrimSuffix(s, "mph"))
	case strings.HasSuffix(s, "km/h"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "km/h"))
	case strings.HasSuffix(s, "kmh"):
		s = strings.TrimSpace(strings.TrimSuffix(s, "kmh"))
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	v *= factor
	// Below 5 km/h is not a drivable limit and above 200 is a typo.
	if v < 5 || v > 200 {
		return 0, false
	}
	return v, true
}
