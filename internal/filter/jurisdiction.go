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
	//
	// "IH" is the Texas-style interstate prefix ("IH 35", "IH-10"). It is
	// listed before the bare "I" alternative for readability; RE2 tries all
	// alternatives, so "I" cannot shadow it either way. Without "IH" here
	// the ref falls through to statePostalRefRe and is classified State.
	federalRefRe = regexp.MustCompile(`^(IH|I|US)[ -]\d`)

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

	// federalOperatorRe matches the federal DOT and the federal highway
	// agencies. It exists because these strings also satisfy the generic
	// "department of transportation" / bare-"dot" tests in isStateOperator,
	// which would otherwise bucket federally operated roads as State.
	//
	// The leading \b is load-bearing: "Columbus Department of Transportation"
	// (Columbus, OH is in examples/top-50-cities) contains the substring
	// "us department of transportation", and a plain strings.Contains check
	// would misclassify that city operator as federal.
	federalOperatorRe = regexp.MustCompile(
		`\b(usdot|fhwa|(u\.?s\.?|united states|federal)[ .]+` +
			`(department of transportation|transportation department|dot\b|highway administration))`)

	// stateTransportationDeptRe matches the "<State> Transportation
	// Department" word order that isStateOperator's "department of
	// transportation" check misses.
	//
	// It is anchored on an actual state name rather than testing for the
	// bare "transportation department" substring, because that substring is
	// overwhelmingly a CITY agency: "Boston Transportation Department",
	// "Austin Transportation Department", "Phoenix Street Transportation
	// Department" are all municipal, and none of them contain the
	// "city"/"municipal" tokens isCityOperator looks for. A substring test
	// would move that city lane area into the State cohort.
	//
	// The mandatory \s+ between the state name and "transportation" is what
	// keeps "Virginia Beach Transportation Department" and "Kansas City
	// Transportation Department" out: their state-named prefix is followed
	// by another word, not by "transportation".
	stateTransportationDeptRe = regexp.MustCompile(
		`\b(alabama|alaska|arizona|arkansas|california|colorado|connecticut|delaware|` +
			`florida|georgia|hawaii|idaho|illinois|indiana|iowa|kansas|kentucky|louisiana|` +
			`maine|maryland|massachusetts|michigan|minnesota|mississippi|missouri|montana|` +
			`nebraska|nevada|new hampshire|new jersey|new mexico|new york|north carolina|` +
			`north dakota|ohio|oklahoma|oregon|pennsylvania|rhode island|south carolina|` +
			`south dakota|tennessee|texas|utah|vermont|west virginia|virginia|washington|` +
			`wisconsin|wyoming|district of columbia|puerto rico)\s+(state\s+)?` +
			`transportation department\b`)
)

// statePostalDeny lists two-letter prefixes that look like postal codes to
// statePostalRefRe but are NOT state-route designations: county routes (CR),
// federal (US/IH — already caught earlier, defensive), and business routes
// (BR).
//
// This list is deliberately a DENY list, not an allow list of real USPS
// codes. Several non-postal two-letter prefixes are genuinely state
// maintained — Texas "FM"/"RM" (Farm-to-Market / Ranch-to-Market, TxDOT
// maintained) and Texas "SL"/"SS"/"BU"/"BS" (state loops, spurs, business
// routes). Under an allow list those would stop matching isStateRef, fall
// past the county checks, and land in the City bucket, silently inflating
// city cohort area, AnnualNeed and FundingGap.
//
// "NF" (National Forest / Forest Service routes) also matches here and is
// therefore classified State today. That is NOT because those routes are
// state maintained — they are federally administered — but because no
// federal-ref rule covers them and deny-listing them would drop them to
// City, which is further from the truth. Left alone deliberately in this
// batch; correcting it needs a federal-ref rule, not a deny entry.
//
// Only add a prefix here when its roads are demonstrably not state
// maintained AND another rule already routes them somewhere better (or City
// is genuinely the correct bucket, not merely the fallthrough one); the
// permissive default stands for everything else.
var statePostalDeny = map[string]bool{
	"CR": true, // county route
	"US": true, // federal (handled by federalRefRe; defensive)
	"IH": true, // federal interstate, Texas style (handled by federalRefRe; defensive)
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

// isFederalOperator reports whether an operator/network string denotes the
// federal DOT or a federal highway agency ("US Department of Transportation",
// "USDOT", "Federal Highway Administration"). The operator is expected to be
// lowercased by the caller.
func isFederalOperator(operator string) bool {
	if operator == "" {
		return false
	}
	return federalOperatorRe.MatchString(operator)
}

// isStateOperator reports whether an operator/network string denotes a state
// DOT. Covers Caltrans plus the generic "<State> Department of Transportation"
// / "<State> Transportation Department" / "DOT" / "State Highway" forms used
// outside California.
func isStateOperator(operator string) bool {
	if operator == "" {
		return false
	}
	// The federal DOT satisfies the generic "department of transportation"
	// and bare-"dot" tests below, so rule it out up front. Callers must send
	// it to JurisdictionFederal themselves — returning false here only keeps
	// it out of the State bucket.
	if isFederalOperator(operator) {
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
	// Both word orders appear in OSM: "Nevada Department of Transportation"
	// and "Nevada Transportation Department". The second order must be
	// state-name anchored -- see stateTransportationDeptRe -- because the
	// bare substring is far more often a city public-works agency.
	if strings.Contains(operator, "department of transportation") ||
		stateTransportationDeptRe.MatchString(operator) ||
		strings.Contains(operator, "state highway") {
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

	// Federal operator ("US Department of Transportation", FHWA). This must
	// run ahead of the state check: those strings also satisfy the generic
	// DOT matching in isStateOperator. Merely excluding them there is not
	// enough — with no rule of their own they would fall all the way through
	// to JurisdictionCity at the bottom, which is worse than State.
	if isFederalOperator(operator) || isFederalOperator(network) {
		return JurisdictionFederal
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
