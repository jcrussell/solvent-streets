package ingest

import (
	"regexp"
	"strconv"
	"strings"
)

// Agency centerline layers carry no OSM tags, so a row with no highway tag of
// its own gets one derived from its functional class.
// Without it every ArcGIS segment was a residential, city-owned street: the
// cohort default of forecast.NormalizeClass and the bottom of
// filter.ClassifyJurisdiction. In Livermore that counted the I-580 mainline
// and its ramps as city residential streets.
//
// The class vocabulary is the FHWA functional classification, which state
// DOTs and counties publish under a handful of field names. The mapping
// follows OSM's US tagging: interstates and their ramps are motorway(_link)
// (Federal), other freeways and expressways trunk (State), and the arterial,
// collector and local classes primary / secondary / tertiary / residential.
// A value outside the vocabulary leaves the row untagged, as before.

// arcgisClassFields are the functional-class fields consulted, in order,
// compared case-insensitively.
var arcgisClassFields = []string{"CLASS", "FUNC_CLASS", "FUNCCLASS", "FUNCTIONAL_CLASS", "FCLASS", "F_SYSTEM"}

// arcgisHPMSField is the one field whose digits are read as HPMS F_SYSTEM
// codes. A bare number under any other class field is not: HERE/NAVTEQ layers
// publish FUNC_CLASS 1-5 with 5 = local, and FHWA's older two-digit codes
// read 02 as a rural principal arterial, so treating those digits as F_SYSTEM
// would turn local streets into collectors and arterials into state trunks.
// Such a row stays untagged.
const arcgisHPMSField = "F_SYSTEM"

// arcgisNumericClass maps the HPMS F_SYSTEM codes (1-7).
var arcgisNumericClass = map[int]string{
	1: "motorway",    // Interstate
	2: "trunk",       // Principal Arterial - Other Freeways and Expressways
	3: "primary",     // Principal Arterial - Other
	4: "secondary",   // Minor Arterial
	5: "tertiary",    // Major Collector
	6: "tertiary",    // Minor Collector
	7: "residential", // Local
}

// stateRouteRe finds a state route in a ramp's value or street name ("SR 84",
// "SR-24 ON RAMP", "STATE ROUTE 92", "HWY 13", "STATE HIGHWAY 1").
var stateRouteRe = regexp.MustCompile(`\b(SR|STATE ROUTE|STATE HWY|STATE HIGHWAY|HWY|HIGHWAY)[- ]?\d{1,3}\b`)

// arcgisHighway returns the OSM highway value for a row's functional class,
// or "" when the row names none this mapping knows.
func arcgisHighway(props map[string]string) string {
	for _, field := range arcgisClassFields {
		for k, v := range props {
			if !strings.EqualFold(k, field) {
				continue
			}
			if hw := highwayForClass(v, strings.EqualFold(k, arcgisHPMSField), arcgisName(props)); hw != "" {
				return hw
			}
		}
	}
	return ""
}

// highwayForClass maps one functional-class value. hpms says whether the
// field holds HPMS F_SYSTEM codes; a number in any other field maps to
// nothing. Ramps are tested before the road classes because a ramp's value
// can name its parent ("Interstate Ramp"). A ramp is a motorway_link
// (Federal) unless its value says state ("State Route Ramp") or its value or
// street name names a state route ("SR 24 WB ON RAMP"), which makes it a
// trunk_link (State). The default stays Interstate because a bare
// "Ramp" usually is one and its name does not always say so: in Alameda
// County's layer 49 of Livermore's 50 ramps serve I-580, and one of those
// is named only "VASCO ON OFF WB".
func highwayForClass(v string, hpms bool, name string) string {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		if !hpms {
			return ""
		}
		return arcgisNumericClass[n]
	}
	c := strings.ToLower(v)
	switch {
	case strings.Contains(c, "ramp"):
		if !strings.Contains(c, "interstate") && (strings.Contains(c, "state") ||
			stateRouteRe.MatchString(strings.ToUpper(v)) || stateRouteRe.MatchString(strings.ToUpper(name))) {
			return "trunk_link"
		}
		return "motorway_link"
	case strings.Contains(c, "interstate"):
		return "motorway"
	case strings.Contains(c, "freeway"), strings.Contains(c, "expressway"):
		// Functional class 2, Other Freeways and Expressways. The class
		// says how a road works, not who owns it; mapping it to trunk
		// (State) is right for the configured layer, whose freeways and
		// expressways are state routes (Alameda County's SR 84), but a
		// county expressway system would need an ownership signal.
		return "trunk"
	case strings.Contains(c, "principal arterial"):
		return "primary"
	case strings.Contains(c, "minor arterial"):
		return "secondary"
	case strings.Contains(c, "collector"):
		return "tertiary"
	case strings.Contains(c, "local"):
		return "residential"
	}
	return ""
}

// arcgisNameFields are the street-name fields consulted, in order, compared
// case-insensitively. FULLNAME is the common one; Alameda County's
// Street_Centerlines names streets in STREET (with direction) and SFEANME.
var arcgisNameFields = []string{"FULLNAME", "STREET", "SFEANME"}

// arcgisName returns the row's street name, or "" when it carries none.
func arcgisName(props map[string]string) string {
	for _, field := range arcgisNameFields {
		for k, v := range props {
			if strings.EqualFold(k, field) && strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	return ""
}
