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
// the territories), shared by every rule that has to tell a state agency from
// a municipal one. It is a single source of truth so the two spellings of a
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
	`wisconsin|wyoming|district of columbia|puerto rico|guam|virgin islands|` +
	`american samoa|northern mariana islands`

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
	//
	// The [A-Z]{1,2} alternative covers the designators that are LETTERS
	// rather than digits: MoDOT's supplemental routes -- "MO W" (53
	// features), "MO V" (18), "MO C" (6), "MO D", "MO N", plus the doubled
	// "MO AA"/"MO FF" -- 81 of which land in City in Kansas City, MO.
	// Requiring a digit was the last thing keeping the lettered spellings of
	// a state route out, exactly as it was for CR/SR before f24871f.
	//
	// The ([^A-Z]|$) tail caps the designator at two letters, and it is
	// load-bearing. Histogramming every two-letter-then-letter ref not
	// already claimed by an earlier rule turns up exactly two things across
	// the corpus: the 81 Missouri supplementals, and Washington DC's
	// ref="DC GOVERNMENT" on one residential way. The latter is junk, and
	// routing a DC residential road out of City would be the wrong direction
	// besides -- DC the city IS the maintaining entity there.
	statePostalRefRe = regexp.MustCompile(`^([A-Z]{2})[ -](\d|[A-Z]{1,2}([^A-Z]|$))`)

	// singleLetterStateRefRe and singleLetterCountyRefRe carry the route
	// conventions that number with a SINGLE letter. statePostalRefRe requires
	// two, so before these rules every one of them matched nothing, fell
	// through the whole ladder and landed in City -- 1648 features billing
	// state highway to the city, tracked as solvent-streets-m0qa.
	//
	// This is an explicit ALLOW LIST rather than a `^[A-Z][ -]\d` pattern, and
	// the distinction is the whole point. The complete set of single-letter
	// prefixes in the ingested corpus is I, M, N, K, L, S, C -- and "I" is
	// 65385 features of Interstate. A generic rule would swallow all of them,
	// business routes included ("I 70 BUS;US 40"); they would survive only
	// because federalRefRe runs earlier, which puts a large silent move one
	// reordering away. Enumerating the six leaves nothing to reorder.
	//
	// Measured over 8741198 road features, settling the per-prefix
	// determination the m0qa bead left open:
	//
	//	M  2355  Michigan (Detroit). "M 1" is Woodward Ave, MDOT;
	//	         also M 5, M 3, M 53, M 85, M 153.                  -> State
	//	N   456  Nebraska (Omaha). N-64, N-133, N-50.               -> State
	//	K    63  Kansas. "K-32" reaches the Kansas City, MO bbox.   -> State
	//	L    58  Nebraska link routes (Omaha). L-28K, L-28B, L-28H. -> State
	//	S    41  NCDOT secondary routes (Charlotte, "S-29-64") plus
	//	         Omaha's S-28J.                                     -> State
	//	C    22  Columbus, OH: C-138, C-288, C-36, C-28, C-501.
	//	         Franklin COUNTY roads, not state routes -- which is
	//	         why C is split out below.                          -> County
	//
	// Both separators are accepted for every prefix even though the corpus is
	// cleanly split (M spaced, the other five hyphenated). Pinning the
	// measured separator looks tighter and is worse: Michigan routes are
	// commonly tagged "M-5" in OSM and Detroit simply happens not to, so a
	// space-only M rule would silently miss MDOT the first time another
	// Michigan city is ingested. The trailing \d holds the line instead --
	// it is what keeps a street whose ref is its own name ("M Street") out of
	// the State bucket.
	//
	// The mandatory separator shadows nothing: SR, SS, SL, SH, MO, NF, LOOP,
	// CR, CH, CC, CTH and COUNTY all carry a letter in position 2.
	//
	// KNOWN LIMIT, measured and left alone. The trailing \d excludes a plain
	// street name but not an ORDINAL one: a ref of "N 1ST ST" would classify
	// State. It is not tightened, for two reasons. There is no such ref in the
	// corpus, and the control for that is E and W -- the two direction letters
	// with no route convention at all, which would carry any leaked street
	// names ("E 12TH ST") and have ZERO refs of this shape across all
	// 8,741,198 features. And the obvious tightening, anchoring the end of the
	// designator, breaks a real one: "M 1 N" is an actual Michigan ref that a
	// $-anchored pattern rejects. A trailing designator is genuinely open,
	// so leaving the rule permissive costs less than closing it wrongly.
	singleLetterStateRefRe = regexp.MustCompile(`^[MNKLS][ -]\d`)

	// singleLetterCountyRefRe is the County half of the allow list above; see
	// that comment for the measurement. It is kept out of countyRefRe because
	// that rule matches on the PREFIX ALONE with no designator shape
	// (bf0c601), and this one does require a digit: "C" is a single letter,
	// with far more ways to turn up on a road than "CR" has.
	singleLetterCountyRefRe = regexp.MustCompile(`^C[ -]\d`)

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
	//
	// The `CO RD` family is the ABBREVIATED spelling of the same thing, and it
	// is here because "CO" is both Colorado's USPS code and the near-universal
	// abbreviation for "County". Without it, statePostalRefRe's [A-Z]{1,2}
	// arm reads "CO RD 45" as a Colorado state route (its two-letter
	// designator "RD" is followed by a non-letter), while "CO HWY 12" and
	// "CO RTE 9" match nothing at all -- their three-letter designators fail
	// that arm -- and fall through the whole ladder into City. One county road
	// billed to the state, its neighbour billed to the city, for the same
	// road type.
	//
	// Like TR above, this is recorded on domain knowledge and has ZERO
	// occurrences in the ingested corpus: histogramming every ^CO ref over
	// ~400 cities returns 33 distinct refs, 12118 features, and every one is a
	// real Colorado route in the "CO <digits>" shape ("CO 121", 2377; "CO 30",
	// 1398). `CO RD`/`CO HWY`/`CO RTE` appear zero times -- unsurprising for a
	// city-focused corpus, since the abbreviation belongs to rural county
	// addressing. So this rule is INERT today and cannot regress those 12118:
	// a digit can never match (RD|ROAD|HWY|HIGHWAY|RTE|ROUTE), so Colorado
	// keeps every ref it already had.
	//
	// The optional period covers the "CO. RD 45" spelling. \b after each
	// alternative is load-bearing the same way stateWordRefRe's trailing \d
	// is: without it "CO ROUTED" or a street named "CO RDS" would match.
	spelledCountyRefRe = regexp.MustCompile(
		`^(CTH[ -]|CO\.?[ -](RD|ROAD|HWY|HIGHWAY|RTE|ROUTE)\b|COUNTY (ROAD|HIGHWAY|ROUTE|TRUNK)\b)`)

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
	//
	// This closed the LOOP/SPUR gap and nothing wider. The fully spelled-out
	// State and US forms still reach no rule and fall through to City:
	// "State Highway 26", "State Hwy 26", "State Road 37" (while "SH 130" and
	// "SR-37" are correctly State), and in federalRefRe's territory
	// "US Highway 12", "US Route 66", "Interstate 90", "U.S. 40". The federal
	// misses are the expensive ones -- those are the highest-area roads in the
	// corpus. spelledCountyRefRe has a matching asymmetry: its two arms use
	// different designator vocabularies, so "CO RD 45" is County but
	// "County Rd 12" is City. solvent-streets-fu74.
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

	// notDOTRe matches an operator that explicitly DISCLAIMS a DOT rather than
	// naming one. Measured: operator="not fdot", 9 features -- Tampa primary
	// 4, Jacksonville secondary 5. It is the only standalone "not" in any
	// operator or network value across the corpus.
	//
	// Without this they reach isStateOperator's bare-"dot" fallback, because
	// spacedDOTRe needs a word boundary before "dot" and "fdot" has none, and
	// classify State -- the exact opposite of what the tag says. The guard
	// only makes isStateOperator decline them; where they land afterwards is
	// the ordinary ladder's call. Net movement is 5 features, not 9: the
	// Tampa primaries fall to City and one Jacksonville secondary to County,
	// while the other four Jacksonville ways carry ref="CR 228" and were
	// already County via countyRefRe, which runs ahead of every operator rule.
	//
	// Consulted by BOTH isFederalOperator and isStateOperator, because
	// ClassifyJurisdiction asks the federal question first: guarding only the
	// state rule would leave "not USDOT" classifying Federal, which is the
	// same inversion one level over.
	//
	// Deliberately narrow: it requires the negated token to END in "dot", so
	// a spelled-out disclaimer ("not the ohio department of transportation")
	// still reaches stateNameSuffixRe and still classifies State. Zero such
	// features exist; widening it would be guessing at a shape rather than
	// measuring one.
	notDOTRe = regexp.MustCompile(`\bnot\s+\S*dot\b`)

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
// That class is closed for the ABBREVIATED and LETTERED shapes. It is NOT closed
// for the fully spelled-out ones -- "State Highway 26", "US Highway 12",
// "Interstate 90" and "County Rd 12" all still land in City. See the note on
// stateWordRefRe above and solvent-streets-fu74; do not read the paragraph below
// as covering them.
// solvent-streets-m0qa was the last of it. statePostalRefRe required TWO
// letters, so the single-letter conventions landed in City -- Michigan's
// "M 5"/"M 1", Nebraska's "N-64"/"L-28K", Kansas's "K-32", 1648 features. It
// also required a DIGIT, so Missouri's lettered supplementals ("MO W") landed
// there too, 81 more. Both are handled above -- by singleLetterStateRefRe and
// singleLetterCountyRefRe, and by the [A-Z]{1,2} alternative in
// statePostalRefRe. Note that the answer to the single-letter case was an
// allow list of the six measured prefixes and NOT a bare ^[A-Z][ -]\d, which
// would have swallowed 65385 Interstate features and put Ohio's county
// "C-138" in the State bucket.
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

// stateAbbrev lists the qualifier forms that name a state or territory by
// ABBREVIATION rather than by its spelled-out name. It is consulted by
// isStateQualifier, alongside stateNameSuffixRe.
//
// solvent-streets-q48z.2: anchoring the DOT qualifier on the 52 spelled-out
// names alone meant "NYS Department of Transportation", "NC Department of
// Transportation" and "Guam Department of Transportation" failed
// isStateTransportationAgency, PASSED isLocalTransportationAgency, and landed
// in City -- a v0.3.0 regression, since the bare
// strings.Contains(operator, "department of transportation") it replaced
// caught all of them.
//
// This is an ALLOW list, and unlike the ref-side rules it is NOT restricted to
// prefixes measured in the corpus. That is a deliberate departure and the
// reason is the difference in ambiguity between the two signals. Refs collide
// with local conventions -- solvent-streets-vp43 measured "CT" as Wisconsin
// County Trunk and "ME" as a California county road, neither of them the state
// the postal code names. An agency NAME does not collide that way: "<code>
// Department of Transportation" is a state DOT essentially always. Enumerating
// only the handful of forms someone happened to write down would leave the
// next one ("PA ...", "WA ...") silently broken, which is the same "the next
// shape springs the same trap" failure bf0c601 was fixed for.
//
// TWO DELIBERATE OMISSIONS, both two-letter codes that are far more often a
// CITY:
//
//	la — Los Angeles. "LA Department of Transportation" (LADOT) already
//	     classifies City via isLocalTransportationAgency; omitting "la" here
//	     PRESERVES that rather than changing it. Louisiana's own agency is
//	     "Department of Transportation and Development", which reaches State
//	     on the no-qualifier path regardless.
//	dc — accepting it would flip Washington DC road area from City to State,
//	     which is the wrong direction: DC the city IS the maintaining entity
//	     there, and City is where its funding obligation belongs. DC's actual
//	     agency, "District Department of Transportation", stays City on the
//	     "district" qualifier either way.
//
// Worth knowing about one entry that stays: "co" is also how "County" is
// abbreviated, so a bare qualifier of "co" reaches State rather than the
// County that strings.Contains(operator, "county") above would have given it.
// It survives only because isStateQualifier demands the abbreviation be the
// WHOLE qualifier -- "fairfax co" is not "co", so "Fairfax Co DOT" is
// untouched by this map. Colorado's own agency reaches State by other routes
// (glued "CDOT" on the bare-dot fallback, or the spelled-out name), so drop
// "co" if a real county-abbreviated operator ever turns up.
//
// Expect ZERO corpus movement from this map. The operator tag covers 0.042% of
// road features (3,631 of 8,741,198, 160 distinct values, dominated by transit
// agencies and businesses), and every state DOT actually present already
// classified correctly -- spelled-out forms via stateNameSuffixRe, glued forms
// (FDOT, MassDOT) via the bare-"dot" fallback. The regression is real; its
// measured impact is not. Fixed on correctness, not on volume.
var stateAbbrev = map[string]bool{
	"al": true, "ak": true, "az": true, "ar": true, "ca": true, "co": true,
	"ct": true, "de": true, "fl": true, "ga": true, "hi": true, "id": true,
	"il": true, "in": true, "ia": true, "ks": true, "ky": true, "me": true,
	"md": true, "ma": true, "mi": true, "mn": true, "ms": true, "mo": true,
	"mt": true, "ne": true, "nv": true, "nh": true, "nj": true, "nm": true,
	"ny": true, "nc": true, "nd": true, "oh": true, "ok": true, "or": true,
	"pa": true, "ri": true, "sc": true, "sd": true, "tn": true, "tx": true,
	"ut": true, "vt": true, "va": true, "wa": true, "wv": true, "wi": true,
	"wy": true,
	// Territories. Puerto Rico, Guam, the US Virgin Islands, American Samoa
	// and the Northern Mariana Islands all run their own DOTs; their spelled
	// names live in stateNameAlt.
	"pr": true, "vi": true, "gu": true, "as": true, "mp": true,
	// Not postal codes, but the abbreviations these two states actually use.
	"nys":  true, // New York State
	"mass": true, // Massachusetts (the spelled-out "MassDOT" is glued and
	// reaches the bare-"dot" fallback instead)
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
	if singleLetterStateRefRe.MatchString(ref) {
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
	// "not USDOT" names no federal agency; see notDOTRe. Checked here as well
	// as in isStateOperator because ClassifyJurisdiction consults this
	// predicate FIRST, so a disclaimer that only guarded the state rule would
	// still come back Federal.
	if notDOTRe.MatchString(operator) {
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
	// An operator that explicitly disclaims a DOT ("not FDOT") is not naming
	// one. Checked here rather than next to the bare-"dot" fallback it exists
	// to guard, so that a disclaimer beats every positive match below it
	// rather than only the last one. isFederalOperator carries the same guard
	// for the same reason.
	if notDOTRe.MatchString(operator) {
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

// isStateQualifier reports whether the words standing in front of a
// transportation-agency name denote a STATE or territory rather than a city.
//
// It MUST be the single predicate both isStateTransportationAgency and
// isLocalTransportationAgency consult, and that is not a stylistic preference.
// isStateOperator bails on isCityOperator BEFORE it reaches the state-agency
// test, and isCityOperator -> isLocalTransportationAgency answers yes to
// "nc department of transportation" -- a qualifier that is present and is not a
// state NAME. Teaching only isStateTransportationAgency about abbreviations
// would compile, pass every existing test, and move zero features, because the
// operator would still be rejected as a city one first. That is
// solvent-streets-niak's failure shape, one level down.
//
// An abbreviation must be the WHOLE qualifier, with dots stripped so "n.c."
// and "nc" agree, and with a leading "the" dropped -- deptOfTransportationRe
// only absorbs "the" when it sits directly before "department", so "the nc
// department of transportation" yields the qualifier "the nc".
//
// Whole-qualifier and not a suffix, even though stateNameSuffixRe next door IS
// a suffix test, and the asymmetry is the point. A state NAME ending the
// qualifier identifies the state, because the cities that begin with one are
// separated by what FOLLOWS it ("virginia beach", "kansas city"). A state CODE
// ending the qualifier identifies nothing of the sort: "Charlotte NC
// Department of Transportation" and "Boston MA Department of Transportation"
// are the ordinary way to disambiguate a CITY, and a suffix test reads both as
// state agencies and moves municipal lane area out of the City cohort. That is
// solvent-streets-niak's direction of failure, reintroduced one abbreviation
// at a time; it was caught in review of the commit that added this function.
func isStateQualifier(qualifier string) bool {
	if stateNameSuffixRe.MatchString(qualifier) {
		return true
	}
	return stateAbbrev[strings.ReplaceAll(strings.TrimPrefix(qualifier, "the "), ".", "")]
}

// isStateTransportationAgency reports whether operator names a transportation
// department belonging to a state: either state-qualified, or carrying no
// qualifier at all.
func isStateTransportationAgency(operator string) bool {
	qualifier, ok := transportationAgencyQualifier(operator)
	return ok && (qualifier == "" || isStateQualifier(qualifier))
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
	if !ok || qualifier == "" || isStateQualifier(qualifier) {
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
	if countyRefRe.MatchString(ref) || spelledCountyRefRe.MatchString(ref) ||
		singleLetterCountyRefRe.MatchString(ref) {
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
