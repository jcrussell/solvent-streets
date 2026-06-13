package geo

import (
	"strconv"
	"strings"
)

// feetPerMetre / inchesPerMetre convert imperial OSM width notations to metres.
const (
	metresPerFoot = 0.3048
	metresPerInch = 0.0254
)

// parseOSMWidth parses an OSM width tag value into metres. OSM width values
// are commonly bare metres ("5", "5.5"), explicitly metres ("5 m", "5m"), or
// imperial: feet/inches via the apostrophe/quote notation ("12'", "3'6\"") or
// a unit suffix ("12 ft", "12ft"). It returns (metres, true) on a successful
// parse of a positive width, or (0, false) when the value is empty, malformed,
// or non-positive so callers fall through to lower-priority estimates.
func parseOSMWidth(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}

	// Feet/inches apostrophe notation: 12', 3'6", 3'6 (the trailing inch
	// quote is optional). Detect by the foot apostrophe.
	if i := strings.IndexByte(s, '\''); i >= 0 {
		return parseFeetInches(s, i)
	}

	// Unit suffixes. Order matters: check "ft"/"feet"/"in" before the bare
	// "m" so "12 ft" doesn't accidentally match an "m" trim.
	lower := strings.ToLower(s)
	switch {
	case strings.HasSuffix(lower, "ft"):
		return parseScaled(s[:len(s)-len("ft")], metresPerFoot)
	case strings.HasSuffix(lower, "feet"):
		return parseScaled(s[:len(s)-len("feet")], metresPerFoot)
	case strings.HasSuffix(lower, "\""): // inches: 16"
		return parseScaled(s[:len(s)-1], metresPerInch)
	case strings.HasSuffix(lower, "in"):
		return parseScaled(s[:len(s)-len("in")], metresPerInch)
	case strings.HasSuffix(lower, "m"):
		return parseScaled(s[:len(s)-1], 1.0)
	default:
		return parseScaled(s, 1.0) // bare value is metres
	}
}

// parseScaled trims surrounding space, parses a float, and multiplies by
// scale. Returns false on parse failure or non-positive result.
func parseScaled(s string, scale float64) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v * scale, true
}

// parseFeetInches parses a feet/inches value where apostropheIdx is the index
// of the foot apostrophe, e.g. "3'6\"" -> 3 ft 6 in. The inches portion (and
// its closing quote) is optional.
func parseFeetInches(s string, apostropheIdx int) (float64, bool) {
	feet, err := strconv.ParseFloat(strings.TrimSpace(s[:apostropheIdx]), 64)
	if err != nil || feet < 0 {
		return 0, false
	}
	metres := feet * metresPerFoot

	rest := strings.TrimSpace(s[apostropheIdx+1:])
	rest = strings.TrimSuffix(rest, "\"")
	rest = strings.TrimSpace(rest)
	if rest != "" {
		inches, err := strconv.ParseFloat(rest, 64)
		if err != nil || inches < 0 {
			return 0, false
		}
		metres += inches * metresPerInch
	}
	if metres <= 0 {
		return 0, false
	}
	return metres, true
}

// Default road widths by highway classification (meters)
var defaultWidths = map[string]float64{
	"motorway":       14.0,
	"motorway_link":  7.0,
	"trunk":          12.0,
	"trunk_link":     6.0,
	"primary":        10.0,
	"primary_link":   5.0,
	"secondary":      8.0,
	"secondary_link": 4.5,
	"tertiary":       7.0,
	"tertiary_link":  4.0,
	"residential":    5.5,
	"unclassified":   5.0,
	"service":        3.5,
	"living_street":  4.0,
}

// laneWidth is the assumed width of a single travel lane.
// Source: AASHTO "A Policy on Geometric Design of Highways and Streets" (Green Book),
// Table 4-1 — standard lane width for urban arterials is 3.6 m (12 ft); 3.7 m is a
// common metric rounding used in FHWA models.
const laneWidth = 3.7 // meters per lane

// InferWidth returns the estimated road width in meters based on OSM tags.
// Priority: explicit width tag > lanes-based > highway classification fallback.
func InferWidth(tags map[string]string) float64 {
	// 1. Explicit width tag — use as-is (surveyed width includes or
	// intentionally excludes parking; adding parkingAddon would double-count).
	if w, ok := tags["width"]; ok {
		if v, ok := parseOSMWidth(w); ok {
			return v
		}
	}

	// 2. Lanes-based
	if l, ok := tags["lanes"]; ok {
		if v, err := strconv.ParseFloat(l, 64); err == nil && v > 0 {
			return v*laneWidth + parkingAddon(tags)
		}
	}

	// 3. Highway classification fallback
	if hw, ok := tags["highway"]; ok {
		if w, ok := defaultWidths[hw]; ok {
			return w + parkingAddon(tags)
		}
	}

	return 5.5 // ultimate fallback: residential width
}

// Default sidewalk widths by highway classification (meters)
var defaultSidewalkWidths = map[string]float64{
	"footway":    1.5,
	"pedestrian": 3.0,
	"corridor":   2.0,
}

// InferSidewalkWidth returns the estimated sidewalk width in meters based on OSM tags.
// Priority: explicit width tag > highway classification fallback.
func InferSidewalkWidth(tags map[string]string) float64 {
	if w, ok := tags["width"]; ok {
		if v, ok := parseOSMWidth(w); ok {
			return v
		}
	}
	if hw, ok := tags["highway"]; ok {
		if w, ok := defaultSidewalkWidths[hw]; ok {
			return w
		}
	}
	return 1.5 // fallback: standard sidewalk width
}

// parkingAddon adds width for on-street parallel parking lanes.
// 2.4 m per side is the standard parallel parking lane width from
// AASHTO Green Book Table 4-20 (2.4 m = 8 ft minimum stall width).
func parkingAddon(tags map[string]string) float64 {
	addon := 0.0
	if tags["parking:left"] == "lane" || tags["parking:left"] == "parallel" {
		addon += 2.4
	}
	if tags["parking:right"] == "lane" || tags["parking:right"] == "parallel" {
		addon += 2.4
	}
	return addon
}
