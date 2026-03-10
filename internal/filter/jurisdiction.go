package filter

import (
	"regexp"
	"strings"

	"pvmt/internal/resource"
)

// Jurisdiction classifies road ownership/maintenance responsibility.
type Jurisdiction int

const (
	JurisdictionCity    Jurisdiction = iota
	JurisdictionCounty
	JurisdictionState
	JurisdictionFederal
)

func (j Jurisdiction) String() string {
	switch j {
	case JurisdictionCity:
		return "city"
	case JurisdictionCounty:
		return "county"
	case JurisdictionState:
		return "state"
	case JurisdictionFederal:
		return "federal"
	default:
		return "unknown"
	}
}

var (
	federalRefRe = regexp.MustCompile(`^(I |US )`)
	stateRefRe   = regexp.MustCompile(`^(CA |SR )`)
)

// ClassifyJurisdiction determines road jurisdiction from OSM tags.
func ClassifyJurisdiction(tags map[string]string) Jurisdiction {
	ref := tags["ref"]
	highway := tags["highway"]
	operator := strings.ToLower(tags["operator"])
	network := strings.ToLower(tags["network"])

	// Federal: interstates and US highways
	if federalRefRe.MatchString(ref) {
		return JurisdictionFederal
	}
	if highway == "motorway" || highway == "motorway_link" {
		return JurisdictionFederal
	}

	// State: Caltrans-operated or state route refs
	if strings.Contains(operator, "caltrans") {
		return JurisdictionState
	}
	if stateRefRe.MatchString(ref) {
		return JurisdictionState
	}
	if highway == "trunk" || highway == "trunk_link" {
		return JurisdictionState
	}

	// County
	if strings.Contains(operator, "county") || strings.Contains(network, "county") {
		return JurisdictionCounty
	}
	if highway == "secondary" && !isCityOperator(operator) {
		return JurisdictionCounty
	}

	return JurisdictionCity
}

func isCityOperator(operator string) bool {
	return strings.Contains(operator, "city") || strings.Contains(operator, "municipal")
}

// Partition classifies features into jurisdiction buckets.
func Partition(features []resource.Feature) map[Jurisdiction][]resource.Feature {
	result := make(map[Jurisdiction][]resource.Feature)
	for _, f := range features {
		j := ClassifyJurisdiction(f.Tags)
		result[j] = append(result[j], f)
	}
	return result
}

// Summary returns feature counts per jurisdiction.
func Summary(features []resource.Feature) map[Jurisdiction]int {
	counts := make(map[Jurisdiction]int)
	for _, f := range features {
		counts[ClassifyJurisdiction(f.Tags)]++
	}
	return counts
}
