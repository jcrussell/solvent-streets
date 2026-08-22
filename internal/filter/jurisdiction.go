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

// stateNameAlt is the regex alternation of every US state name (plus DC and
// Puerto Rico), shared by every rule that has to tell a state agency from a
// municipal one. It is a single source of truth so the two spellings of a
// transportation department cannot drift apart in which names they recognise —
// which is exactly how solvent-streets-niak happened: "<State> Transportation
// Department" was anchored on this list while "<City> Department of
// Transportation" was left as a bare substring test.
const stateNameAlt = `alabama|alaska|arizona|arkansas|california|colorado|connecticut|delaware|` +
	`florida|georgia|hawaii|idaho|illinois|indiana|iowa|kansas|kentucky|louisiana|` +
	`maine|maryland|massachusetts|michigan|minnesota|mississippi|missouri|montana|` +
	`nebraska|nevada|new hampshire|new jersey|new mexico|new york|north carolina|` +
	`north dakota|ohio|oklahoma|oregon|pennsylvania|rhode island|south carolina|` +
	`south dakota|tennessee|texas|utah|vermont|west virginia|virginia|washington|` +
	`wisconsin|wyoming|district of columbia|puerto rico`

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
	//
	// NF / FR / FS are the National Forest and Forest Service road prefixes —
	// federally administered, not state maintained. They are matched here
	// rather than deny-listed in statePostalDeny because a deny entry routes
	// to City, the fallthrough bucket, which is further from the truth than
	// the State they used to get. None of the three is a USPS state code, so
	// none can shadow a real state route. Measured against the ingested
	// corpus: NF 61 (Angeles NF, in the Los Angeles/Glendale/La Cañada
	// Flintridge bboxes), FR 2N72 / 3S04 / 333 (San Bernardino NF plus
	// El Paso, Albuquerque, Colorado Springs), FS 368 / 370 (Colorado
	// Springs — the same roads FR also tags).
	federalRefRe = regexp.MustCompile(`^(IH|I|US|NF|FR|FS)[ -]\d`)

	// stateExplicitRefRe matches the unambiguous state-route conventions:
	// SR/SH (State Route / State Highway) and "Route N" / "State Route N".
	// These are never county or federal, so they can match directly.
	//
	// The optional [A-Z] before the number covers Florida's lettered state
	// roads -- "SR A1A" is 466 features in Jacksonville. Anchoring on a bare
	// \d missed every one of them, and a state route that falls through this
	// ladder lands in City, billing FDOT arterial to the city.
	stateExplicitRefRe = regexp.MustCompile(`^(SR|SH|STATE ROUTE|ROUTE)[ -]?[A-Z]?\d`)

	// statePostalRefRe matches a generic two-letter postal-code state route
	// ("CA 84", "CO 2", "MA 9", "OR 99E", "OR-99E"). It is deliberately
	// applied AFTER federalRefRe (so US/I never reach it) and after the
	// county-ref check, and excludes the deny-listed prefixes below so it
	// cannot reclassify county routes ("CR 12") or business/federal forms.
	statePostalRefRe = regexp.MustCompile(`^([A-Z]{2})[ -]\d`)

	// countyRefRe matches county-route refs. Each alternative must be checked
	// before the generic two-letter state match, which would otherwise swallow
	// it; none of them is a USPS state code, so none can shadow a real state
	// route.
	//
	//   CR — the near-universal OSM county-route prefix.
	//   CH — county highway. Documented as an IL/WI convention, but the
	//        ingested corpus finds it only in Lakewood, CO and Minneapolis, MN
	//        (CH 19/23/30/34/35/42/44/77/93/94, 172 features).
	//   CC — Clark County, NV. "CC 215" is the Bruce Woodbury Beltway
	//        (133 features, Las Vegas). Not in the bead; found by measuring.
	//        Currently INERT on the corpus: every CC 215 way is tagged
	//        highway=motorway, and ClassifyJurisdiction's motorway rule runs
	//        ahead of the ref checks, so those 133 resolve Federal -- arguably
	//        wrong for a county beltway, but that is the generic
	//        motorways-are-federal heuristic's call to revisit, not this
	//        rule's. The entry still earns its place for CC ways tagged
	//        anything else.
	//   TR — township road (OH/IN/MI/PA). A township is a sub-county civil
	//        division, so County is the nearest correct bucket of the four
	//        this type offers — NOT a claim that townships are county
	//        maintained. Unlike the other three this has ZERO occurrences in
	//        the ingested corpus (~400 cities); it is recorded here on domain
	//        knowledge alone. Harmless if wrong: see the note below on why
	//        County and State are indistinguishable to every published number.
	// Anything carrying one of these prefixes and a separator is a county
	// route; no designator shape is required after it. The bare-\d anchor
	// this replaces missed California's letter designators entirely --
	// San Diego County's S-routes (CR S21, 1064 features; CR S17, 670;
	// CR S11, 604), Los Angeles County's N-routes (CR N8, 624), the G/J/E
	// series (CR G8, 600) -- 9025 features, overwhelmingly highway=primary,
	// every one landing in City and billing a county arterial to the city's
	// area, AnnualNeed and FundingGap.
	//
	// Matching on the prefix alone rather than enumerating designator shapes
	// is deliberate, and mirrors spelledCountyRefRe's `CTH[ -]`. A first
	// attempt requiring [A-Z]?\d still missed 75 further features in three
	// more shapes -- hyphenated "CR S-40" (16), and letter-only "CR G" (15),
	// "CR LL" (6), "CR H" -- which is the same trap a fourth shape would
	// spring later. None of CR/CH/CC/TR is a USPS state code (see
	// statePostalDeny), so widening cannot shadow a real state route, and
	// federalRefRe runs earlier in the ladder regardless.
	countyRefRe = regexp.MustCompile(`^(CR|CH|CC|TR)[ -]`)

	// spelledCountyRefRe matches the county-route forms written out in words,
	// which no numeric pattern can reach. Wisconsin spells County Trunk
	// Highway out: "CTH PP" (171 features, Milwaukee), "CTH E", "CTH ZZ" --
	// note the letter-only designator carries no digit at all, so this cannot
	// reuse the shape above -- plus "County Highway S21" (178). 455 features.
	spelledCountyRefRe = regexp.MustCompile(`^(CTH[ -]|COUNTY (ROAD|HIGHWAY|ROUTE|TRUNK)\b)`)

	// stateWordRefRe matches the state-route types spelled as words rather than
	// abbreviated. statePostalDeny's note below already records that TxDOT's
	// abbreviated SL/SS (state loop / state spur) forms reach isStateRef and
	// are correctly State; the spelled forms of the very same roads matched
	// nothing and fell through to City -- "Loop 12" is 1172 features in
	// Dallas, "Spur 303" 327, 2905 in total across Dallas, Austin, San Antonio
	// and Fort Worth.
	//
	// The trailing \d is load-bearing: it is what keeps a street literally
	// named "Loop Road" or "Spur Trail" out of the State bucket.
	stateWordRefRe = regexp.MustCompile(`^(LOOP|SPUR)[ -]\d`)

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
		`\b(` + stateNameAlt + `)\s+(state\s+)?transportation department\b`)

	// stateNameSuffixRe matches a state name occupying the END of a string.
	// It is used on the qualifier that precedes a transportation-agency name,
	// where anchoring at the end is what separates a state from a city that
	// merely starts with one: "virginia beach" ends in "beach", "kansas city"
	// in "city", while "washington" and "new york" end in themselves.
	stateNameSuffixRe = regexp.MustCompile(`(^|\s)(` + stateNameAlt + `)$`)

	// deptOfTransportationRe and spacedDOTRe capture everything in front of a
	// transportation-agency name, in the two spellings that need a qualifier
	// test. The optional "the"/"state" words are absorbed by the pattern
	// rather than left in the capture, so "the state department of
	// transportation" yields an EMPTY qualifier and not a bogus local one.
	//
	// spacedDOTRe requires a word boundary before "dot", which is exactly what
	// keeps the glued state abbreviations ("massdot", "caltrans"-adjacent
	// forms like "cdot", "odot") out of it: they have no boundary there and
	// stay on isStateOperator's bare-"dot" fallback.
	deptOfTransportationRe = regexp.MustCompile(
		`^(.*?)\b(?:the\s+)?(?:state\s+)?department of transportation\b`)
	spacedDOTRe = regexp.MustCompile(`^(.*?)\b(?:the\s+)?(?:state\s+)?dot\b`)
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
// The deny list is NOT the whole story, and reading it as such is how
// solvent-streets-qwfo happened. Every rule here is a two-letter-then-DIGIT
// shape, so it can only ever see the ABBREVIATED spelling of a route. The
// same TxDOT loops and spurs this comment credits the permissive default with
// catching as "SL 12"/"SS 55" were, written out as "Loop 12" and "Spur 303",
// matching nothing at all and landing in City — 2905 features. Lettered
// designators (CR S21, SR A1A) failed the same way, for 9491 more. Those are
// now handled by stateWordRefRe, spelledCountyRefRe and the [A-Z]? in
// countyRefRe/stateExplicitRefRe. Before adding a prefix here, check whether
// the ref convention you are reasoning about also has a spelled or lettered
// form that no pattern in this file can reach.
//
// That class is NOT closed. statePostalRefRe still requires TWO letters, so
// the single-letter state-route conventions land in City today: Michigan's
// "M 5"/"M 1" (1207 features -- M 1 is Woodward Ave, MDOT), Nebraska's
// "N-64"/"L-28K" (356), Kansas's "K-32" (63). ~1729 features, tracked in
// solvent-streets-m0qa. They are left alone here rather than guessed at,
// because a bare ^[A-Z][ -]\d would also need to reckon with Ohio's "C-138"
// (county, not state) and with Missouri's lettered supplementals ("MO W"),
// and neither has been measured per-prefix yet.
//
// Three more non-postal prefixes reach this rule and are left as State
// deliberately, each for its own reason. All three were found by histogramming
// distinct ^[A-Z]{2}[ -]\d prefixes over the ingested corpus rather than
// reasoned about in the abstract:
//
//	BW (1015 features, Houston) — "BW 8" is Beltway 8 / the Sam Houston
//	   Tollway, a TxDOT-designated state highway. State is simply correct
//	   here; the permissive default got it right. Noted because the volume
//	   makes it the largest prefix with no explicit rule.
//	PR (17 features, Houston + San Antonio) — this settles the long-standing
//	   ambiguity between Texas rural "Private Road" addressing and PRDOT
//	   Puerto Rico highways: every occurrence is Texas, so these are private
//	   roads, and no Puerto Rico city is in the set. None of the four buckets
//	   means "private". State is the least-wrong: it keeps a road the city
//	   does not maintain out of the city's cohort area and funding gap, which
//	   City would not.
//	OS (19 features, Jacksonville) — unidentified. Possibly Osceola National
//	   Forest, which is ~50 mi west of the bbox, but not confidently enough to
//	   route it federal on 19 features. Left as State until there is evidence.
//
// Only add a prefix here when its roads are demonstrably not state
// maintained AND another rule already routes them somewhere better (or City
// is genuinely the correct bucket, not merely the fallthrough one); the
// permissive default stands for everything else.
//
// Worth knowing before agonizing over County vs State vs Federal for a new
// prefix: ClassifyJurisdiction is consumed as a binary City / not-City gate
// everywhere it feeds an output (compute.go's filterBufferedByJurisdiction,
// combined.go, export/playhex.go). The three non-City buckets are told apart
// only in compute's progress line. So a choice among them is a labeling
// choice with no effect on any published number — and a choice to route
// something to City is the one that moves area, AnnualNeed and FundingGap.
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
	if stateWordRefRe.MatchString(ref) {
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
	// and "Nevada Transportation Department". BOTH must be state-name
	// anchored -- see stateTransportationDeptRe -- because either bare
	// substring is far more often a city public-works agency: "Columbus
	// Department of Transportation" and "Boston Transportation Department"
	// are both municipal.
	if isStateTransportationAgency(operator) ||
		stateTransportationDeptRe.MatchString(operator) ||
		strings.Contains(operator, "state highway") {
		return true
	}
	// Bare "dot" token (e.g. "CDOT", "MassDOT", "ODOT") -- safe to match now
	// that county and city operators have been excluded above. The city
	// exclusion is what carries "Columbus DOT" and "Corpus Christi DOT": a
	// SPACE-separated "dot" with a non-state qualifier is a local agency, and
	// isCityOperator knows that. A glued abbreviation has no qualifier to
	// read, so it stays here and stays State.
	if strings.Contains(operator, "dot") {
		return true
	}
	return false
}

// transportationAgencyQualifier returns the words standing in front of a
// "department of transportation" or space-separated "DOT" agency name, and
// whether operator names such an agency at all.
//
// The qualifier is what decides the bucket. "Nevada Department of
// Transportation" is a state agency; "Columbus Department of Transportation"
// and DC's "District Department of Transportation" are municipal ones; a bare
// "Department of Transportation" carries no qualifier at all and is the state's
// own agency as OSM uses the tag. Louisiana's "Department of Transportation and
// Development" reaches the same answer either way -- the pattern ends on a word
// boundary, so the trailing "and development" neither breaks the match nor
// lands in the qualifier.
func transportationAgencyQualifier(operator string) (qualifier string, ok bool) {
	for _, re := range []*regexp.Regexp{deptOfTransportationRe, spacedDOTRe} {
		if m := re.FindStringSubmatch(operator); m != nil {
			return strings.TrimSpace(m[1]), true
		}
	}
	return "", false
}

// isStateTransportationAgency reports whether operator names a transportation
// department belonging to a state: either state-name qualified, or carrying no
// qualifier at all.
func isStateTransportationAgency(operator string) bool {
	qualifier, ok := transportationAgencyQualifier(operator)
	return ok && (qualifier == "" || stateNameSuffixRe.MatchString(qualifier))
}

// isLocalTransportationAgency reports whether operator names a transportation
// department run by a CITY: a qualifier that is present and is neither a state
// name nor the two other levels of government that also run DOTs.
//
// The federal and county exclusions are not reachable through
// ClassifyJurisdiction — both are decided before anything consults
// isCityOperator — but this predicate is also read on its own, and "US
// Department of Transportation" or "Fairfax County Department of
// Transportation" answering yes to "is this a city agency?" is wrong on its own
// terms and would become a live bug the first time the rule order moved.
func isLocalTransportationAgency(operator string) bool {
	qualifier, ok := transportationAgencyQualifier(operator)
	if !ok || qualifier == "" || stateNameSuffixRe.MatchString(qualifier) {
		return false
	}
	return !isFederalOperator(operator) && !strings.Contains(operator, "county")
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

	// County refs ("CR 12", "CR S21", "CTH PP") are checked before the generic
	// state-postal match, which would otherwise misclassify them as a state
	// route.
	if countyRefRe.MatchString(ref) || spelledCountyRefRe.MatchString(ref) {
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

// isCityOperator reports whether operator names a municipal agency.
//
// Beyond the "city"/"municipal" tokens, it recognises a transportation
// department whose qualifier is not a state name -- "Columbus Department of
// Transportation", DC's "District Department of Transportation". Those carry
// neither token, so without this they would fail the City test twice: once as
// isStateOperator's exclusion, leaving them in the State bucket, and once at
// ClassifyJurisdiction's highway=secondary rule, which would route them to
// County instead.
func isCityOperator(operator string) bool {
	return strings.Contains(operator, "city") ||
		strings.Contains(operator, "municipal") ||
		isLocalTransportationAgency(operator)
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
