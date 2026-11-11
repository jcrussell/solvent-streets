package ingest

import (
	"strconv"
	"strings"
)

// Agency centerline layers carry no OSM tags, so a row with no highway tag of
// its own gets one derived from its functional class (solvent-streets-stue).
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

// arcgisNumericClass maps the HPMS F_SYSTEM codes (1-7), which several layers
// publish in their functional-class field instead of the text names.
var arcgisNumericClass = map[int]string{
	1: "motorway",    // Interstate
	2: "trunk",       // Principal Arterial - Other Freeways and Expressways
	3: "primary",     // Principal Arterial - Other
	4: "secondary",   // Minor Arterial
	5: "tertiary",    // Major Collector
	6: "tertiary",    // Minor Collector
	7: "residential", // Local
}

// arcgisHighway returns the OSM highway value for a row's functional class,
// or "" when the row names none this mapping knows.
func arcgisHighway(props map[string]string) string {
	for _, field := range arcgisClassFields {
		for k, v := range props {
			if strings.EqualFold(k, field) {
				if hw := highwayForClass(v); hw != "" {
					return hw
				}
			}
		}
	}
	return ""
}

// highwayForClass maps one functional-class value. Ramps are tested before
// the road classes because a ramp's value can name its parent ("Interstate
// Ramp").
func highwayForClass(v string) string {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return arcgisNumericClass[n]
	}
	c := strings.ToLower(v)
	switch {
	case strings.Contains(c, "ramp"):
		return "motorway_link"
	case strings.Contains(c, "interstate"):
		return "motorway"
	case strings.Contains(c, "freeway"), strings.Contains(c, "expressway"):
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
