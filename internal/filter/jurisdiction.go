package filter

import (
	"regexp"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// Jurisdiction classifies road ownership/maintenance responsibility.
type Jurisdiction int

const (
	JurisdictionCity Jurisdiction = iota
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
	// federalRefRe matches Interstate and US-highway refs in both the
	// spaced ("I 80", "US 101") and hyphenated ("I-80", "US-101") forms
	// that both appear in OSM. The trailing \d guards against matching
	// unrelated tokens that merely start with "I"/"US".
	federalRefRe = regexp.MustCompile(`^(I|US)[ -]\d`)

	// stateExplicitRefRe matches the unambiguous state-route conventions:
	// SR/SH (State Route / State Highway) and "Route N" / "State Route N".
	// These are never county or federal, so they can match directly.
	stateExplicitRefRe = regexp.MustCompile(`^(SR|SH|STATE ROUTE|ROUTE)[ -]?\d`)

	// statePostalRefRe matches a generic two-letter postal-code state route
	// ("CA 84", "CO 2", "MA 9", "OR 99E", "OR-99E"). It is deliberately
	// applied AFTER federalRefRe (so US/I never reach it) and after the
	// county-ref check, and excludes the deny-listed prefixes below so it
	// cannot reclassify county routes ("CR 12") or business/federal forms.
	statePostalRefRe = regexp.MustCompile(`^([A-Z]{2})[ -]\d`)

	// countyRefRe matches county-route refs ("CR 12", "CR-12"). "CR" is the
	// near-universal OSM county-route prefix; it must be checked before the
	// generic two-letter state match, which would otherwise swallow it.
	countyRefRe = regexp.MustCompile(`^CR[ -]\d`)
)

// statePostalDeny lists two-letter prefixes that look like postal codes to
// statePostalRefRe but are NOT state-route designations: county routes (CR),
// federal (US — already caught earlier, defensive), and business routes (BR).
var statePostalDeny = map[string]bool{
	"CR": true, // county route
	"US": true, // federal (handled by federalRefRe; defensive)
	"BR": true, // business route
}

// isStateRef reports whether an OSM ref denotes a state route. It assumes
// federal and county refs have already been ruled out by the caller.
func isStateRef(ref string) bool {
	if stateExplicitRefRe.MatchString(ref) {
		return true
	}
	if m := statePostalRefRe.FindStringSubmatch(ref); m != nil {
		return !statePostalDeny[m[1]]
	}
	return false
}

// isStateOperator reports whether an operator/network string denotes a state
// DOT. Covers Caltrans plus the generic "<State> Department of Transportation"
// / "DOT" / "State Highway" forms used outside California.
func isStateOperator(operator string) bool {
	if operator == "" {
		return false
	}
	// Caltrans is unambiguous regardless of other tokens.
	if strings.Contains(operator, "caltrans") {
		return true
	}
	// Counties and cities also run "Departments of Transportation" / "DOT"s
	// (e.g. "Los Angeles County DOT", "Anytown City DOT"). Those are NOT
	// state operators — exclude them so the generic DOT match below can't
	// steal county/city roads into the State bucket.
	if strings.Contains(operator, "county") || isCityOperator(operator) {
		return false
	}
	if strings.Contains(operator, "department of transportation") || strings.Contains(operator, "state highway") {
		return true
	}
	// Bare "dot" token (e.g. "CDOT", "MassDOT", "ODOT") — safe to match now
	// that county/city operators have been excluded above.
	if strings.Contains(operator, "dot") {
		return true
	}
	return false
}

// ClassifyJurisdiction determines road jurisdiction from OSM tags.
func ClassifyJurisdiction(tags map[string]string) Jurisdiction {
	// Uppercase the ref so case-insensitive route conventions ("State Route
	// 26", "Route 9") match the uppercase ref regexes; OSM refs are usually
	// uppercase already, but mixed case appears in the wild.
	ref := strings.ToUpper(tags["ref"])
	highway := tags["highway"]
	operator := strings.ToLower(tags["operator"])
	network := strings.ToLower(tags["network"])

	// Federal: interstates and US highways. Checked first so "US"/"I" refs
	// never fall through to the generic two-letter state-postal match.
	if federalRefRe.MatchString(ref) {
		return JurisdictionFederal
	}
	if highway == "motorway" || highway == "motorway_link" {
		return JurisdictionFederal
	}

	// County refs ("CR 12") are checked before the generic state-postal
	// match, which would otherwise misclassify them as a state route.
	if countyRefRe.MatchString(ref) {
		return JurisdictionCounty
	}

	// State: state DOT operator (Caltrans + generic DOTs) or state-route ref.
	if isStateOperator(operator) || isStateOperator(network) {
		return JurisdictionState
	}
	if isStateRef(ref) {
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
