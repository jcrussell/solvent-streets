package filter

import (
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

func TestClassifyJurisdiction(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want Jurisdiction
	}{
		{"interstate ref", map[string]string{"ref": "I 580", "highway": "motorway"}, JurisdictionFederal},
		{"US highway ref", map[string]string{"ref": "US 101"}, JurisdictionFederal},
		{"motorway no ref", map[string]string{"highway": "motorway"}, JurisdictionFederal},
		{"motorway link", map[string]string{"highway": "motorway_link"}, JurisdictionFederal},
		{"caltrans operator", map[string]string{"operator": "Caltrans", "highway": "primary"}, JurisdictionState},
		{"caltrans mixed case", map[string]string{"operator": "CalTrans"}, JurisdictionState},
		{"state route CA ref", map[string]string{"ref": "CA 84", "highway": "primary"}, JurisdictionState},
		{"state route SR ref", map[string]string{"ref": "SR 84"}, JurisdictionState},

		// E1: generalized multi-state detection (CO/MA/OR refs + DOT operators).
		{"colorado state route", map[string]string{"ref": "CO 2", "highway": "primary"}, JurisdictionState},
		{"massachusetts state route", map[string]string{"ref": "MA 9", "highway": "primary"}, JurisdictionState},
		{"oregon state route", map[string]string{"ref": "OR 99E", "highway": "primary"}, JurisdictionState},
		{"oregon hyphenated ref", map[string]string{"ref": "OR-99E"}, JurisdictionState},
		{"colorado dot operator", map[string]string{"operator": "Colorado Department of Transportation", "highway": "primary"}, JurisdictionState},
		{"massdot operator", map[string]string{"operator": "MassDOT", "highway": "primary"}, JurisdictionState},
		{"odot operator", map[string]string{"operator": "ODOT", "highway": "primary"}, JurisdictionState},
		{"state highway operator", map[string]string{"operator": "Oregon State Highway Division"}, JurisdictionState},
		{"state route worded", map[string]string{"ref": "State Route 26"}, JurisdictionState},

		// E1 regression: county/city DOTs must NOT be classified as state
		// despite containing "dot"/"department of transportation".
		{"county dot stays county", map[string]string{"operator": "Los Angeles County DOT", "highway": "secondary"}, JurisdictionCounty},
		{"county dept of transportation stays county", map[string]string{"operator": "Miami-Dade County Department of Transportation", "highway": "secondary"}, JurisdictionCounty},
		{"city dot stays city", map[string]string{"operator": "Anytown City DOT", "highway": "residential"}, JurisdictionCity},

		// E1: hyphenated federal refs must stay federal, not state.
		{"interstate hyphenated", map[string]string{"ref": "I-80"}, JurisdictionFederal},
		{"us highway hyphenated", map[string]string{"ref": "US-101"}, JurisdictionFederal},

		// E1 hazard: CR/US collision — CR is county, US is federal, neither
		// may be swallowed by the generic two-letter state-postal match.
		{"county route CR ref", map[string]string{"ref": "CR 12", "highway": "primary"}, JurisdictionCounty},
		{"county route CR hyphenated", map[string]string{"ref": "CR-12"}, JurisdictionCounty},
		{"us highway not state", map[string]string{"ref": "US 50"}, JurisdictionFederal},

		{"trunk highway", map[string]string{"highway": "trunk"}, JurisdictionState},
		{"trunk link", map[string]string{"highway": "trunk_link"}, JurisdictionState},
		{"county operator", map[string]string{"operator": "Alameda County", "highway": "secondary"}, JurisdictionCounty},
		{"county network", map[string]string{"network": "Alameda county roads"}, JurisdictionCounty},
		{"secondary no operator", map[string]string{"highway": "secondary"}, JurisdictionCounty},
		{"secondary city operator", map[string]string{"highway": "secondary", "operator": "City of Livermore"}, JurisdictionCity},
		{"residential", map[string]string{"highway": "residential"}, JurisdictionCity},
		{"tertiary", map[string]string{"highway": "tertiary"}, JurisdictionCity},
		{"empty tags", map[string]string{}, JurisdictionCity},
		{"nil tags", nil, JurisdictionCity},

		// A4 fix 1: Texas-style "IH" interstates are federal, not state.
		// Before this batch federalRefRe missed "IH", the ref fell through to
		// statePostalRefRe, and these classified as State.
		{"texas interstate IH ref", map[string]string{"ref": "IH 35"}, JurisdictionFederal},
		{"texas interstate IH hyphenated", map[string]string{"ref": "IH-10"}, JurisdictionFederal},

		// A4 fix 2: the federal DOT is federal, not state. It satisfies the
		// generic "department of transportation"/"dot" tests in
		// isStateOperator, so it needs an explicit check that runs first --
		// and it must not instead fall through to City.
		{"us dot operator is federal", map[string]string{"operator": "US Department of Transportation", "highway": "primary"}, JurisdictionFederal},
		{"us dot operator dotted", map[string]string{"operator": "U.S. Department of Transportation"}, JurisdictionFederal},
		{"united states dot operator", map[string]string{"operator": "United States Department of Transportation"}, JurisdictionFederal},
		{"usdot abbreviation", map[string]string{"operator": "USDOT", "highway": "primary"}, JurisdictionFederal},
		{"fhwa operator", map[string]string{"operator": "Federal Highway Administration", "highway": "primary"}, JurisdictionFederal},
		{"federal dot network", map[string]string{"network": "US Department of Transportation", "highway": "primary"}, JurisdictionFederal},

		// A4 fix 2 hazard: "Columbus" and "Massachusetts" both contain the
		// literal substring "us department of transportation". The federal
		// word-boundary guard is asserted directly in
		// TestFederalOperatorWordBoundary; only the unambiguous city form is
		// pinned to a bucket here, because the state/city verdict for a bare
		// "Columbus Department of Transportation" is a separate pre-existing
		// question (see that test's comment).
		{"columbus city dot stays city", map[string]string{"operator": "City of Columbus Department of Transportation", "highway": "residential"}, JurisdictionCity},

		// A4 fix 3: "<State> Transportation Department" is a state DOT.
		{"state transportation department word order", map[string]string{"operator": "Nevada Transportation Department", "highway": "primary"}, JurisdictionState},
		{"state transportation department network", map[string]string{"network": "Utah Transportation Department"}, JurisdictionState},
		{"us transportation department is federal", map[string]string{"operator": "US Transportation Department", "highway": "primary"}, JurisdictionFederal},

		// A4 fix 3 hazard: "<City> Transportation Department" is a MUNICIPAL
		// agency and by far the more common use of that word order. None of
		// these contain the "city"/"municipal" tokens isCityOperator looks
		// for, so a bare substring test for "transportation department"
		// would pull all of them into the State cohort. All three cities are
		// in examples/top-50-cities.
		{"boston transportation department stays city", map[string]string{"operator": "Boston Transportation Department", "highway": "residential"}, JurisdictionCity},
		{"austin transportation department stays city", map[string]string{"operator": "Austin Transportation Department", "highway": "residential"}, JurisdictionCity},
		{"phoenix street transportation department stays city", map[string]string{"operator": "Phoenix Street Transportation Department", "highway": "residential"}, JurisdictionCity},
		// State-named city prefixes must not be mistaken for the state DOT.
		{"virginia beach transportation department stays city", map[string]string{"operator": "Virginia Beach Transportation Department", "highway": "residential"}, JurisdictionCity},
		{"kansas city transportation department stays city", map[string]string{"operator": "Kansas City Transportation Department", "highway": "residential"}, JurisdictionCity},
		{"county transportation department stays county", map[string]string{"operator": "Clark County Transportation Department", "highway": "secondary"}, JurisdictionCounty},
		{"city transportation department stays city", map[string]string{"operator": "City of Reno Transportation Department", "highway": "residential"}, JurisdictionCity},

		// A4 deny-list regression pins: these non-postal prefixes must keep
		// matching isStateRef. An allow list of real USPS codes would
		// silently move them into the City bucket.
		{"texas farm to market", map[string]string{"ref": "FM 1960", "highway": "primary"}, JurisdictionState},
		{"texas ranch to market", map[string]string{"ref": "RM 620", "highway": "primary"}, JurisdictionState},
		{"texas state loop", map[string]string{"ref": "SL 8"}, JurisdictionState},
		{"texas state spur", map[string]string{"ref": "SS 55"}, JurisdictionState},
		{"texas business us", map[string]string{"ref": "BU 287"}, JurisdictionState},
		{"texas business state", map[string]string{"ref": "BS 6"}, JurisdictionState},
		{"minnesota trunk highway", map[string]string{"ref": "TH 5"}, JurisdictionState},

		// solvent-streets-akvx. The three cases below were previously pinned to
		// State as the status quo, pending "a federal-ref rule" that
		// federalRefRe now carries. Refs are the real ones from the ingested
		// corpus: NF 61 is Angeles NF (Los Angeles / Glendale / La Cañada
		// Flintridge), FR 2N72 is San Bernardino NF, FS 370 is Colorado
		// Springs. Note FR's numbering is not plain digits -- "2N72", "3S04" --
		// so the regex must only require a leading digit.
		{"national forest route is federal", map[string]string{"ref": "NF 61"}, JurisdictionFederal},
		{"forest road is federal", map[string]string{"ref": "FR 2N72"}, JurisdictionFederal},
		{"forest road hyphenated", map[string]string{"ref": "FR-333"}, JurisdictionFederal},
		{"forest service road is federal", map[string]string{"ref": "FS 370"}, JurisdictionFederal},

		// County-highway refs. CH is the bead's case (Lakewood CO,
		// Minneapolis MN -- not the IL/WI the bead predicted); CC 215 is the
		// Clark County beltway in Las Vegas, found by measuring rather than
		// from the bead. TR (township road) has zero occurrences in the
		// corpus and rides on domain knowledge alone.
		{"county highway ref", map[string]string{"ref": "CH 93", "highway": "primary"}, JurisdictionCounty},
		{"county highway hyphenated", map[string]string{"ref": "CH-30"}, JurisdictionCounty},
		{"clark county beltway", map[string]string{"ref": "CC 215", "highway": "primary"}, JurisdictionCounty},
		// ...but on the real corpus every CC 215 way is tagged motorway, and
		// the motorway rule runs ahead of the ref checks, so the county rule
		// never fires for them. Pinned so the shadowing is visible rather than
		// surprising the next reader who greps for CC.
		{"clark county beltway as motorway stays federal", map[string]string{"ref": "CC 215", "highway": "motorway"}, JurisdictionFederal},
		{"township road", map[string]string{"ref": "TR 40"}, JurisdictionCounty},

		// solvent-streets-qwfo: every rule in this file used to be anchored on
		// a DIGIT right after the separator, so route designators that put a
		// LETTER there, or spell the route type as a word, matched nothing and
		// fell all the way through to City. 12851 road-like features across 55
		// cities, 15427 of the 17214 ref-bearing City features being
		// highway=primary -- arterial area, the expensive kind.
		//
		// Each case below is the lettered or spelled twin of a bare-digit case
		// already pinned above, and must reach the same bucket: that
		// equivalence is the whole claim of the fix.
		//
		// California county-route designators, 9025 features. San Diego
		// County's S-series and Los Angeles County's N-series are the bulk.
		{"CA county route S-series", map[string]string{"ref": "CR S21", "highway": "primary"}, JurisdictionCounty},
		{"CA county route N-series", map[string]string{"ref": "CR N8", "highway": "primary"}, JurisdictionCounty},
		{"CA county route G-series", map[string]string{"ref": "CR G8", "highway": "primary"}, JurisdictionCounty},
		// Semicolon-joined refs are governed by the first component, because
		// every ref pattern is ^-anchored. 225 features in Carlsbad alone.
		{"CA county route with joined historic US ref", map[string]string{"ref": "CR S21;US 101 Historic", "highway": "primary"}, JurisdictionCounty},
		// Wisconsin spells County Trunk Highway out, 455 features. "CTH E"
		// carries no digit at all, which is why spelledCountyRefRe cannot
		// reuse the numeric shape.
		{"wisconsin county trunk highway", map[string]string{"ref": "CTH PP", "highway": "primary"}, JurisdictionCounty},
		{"wisconsin county trunk letter only", map[string]string{"ref": "CTH E", "highway": "primary"}, JurisdictionCounty},
		{"county highway spelled out", map[string]string{"ref": "County Highway S21", "highway": "primary"}, JurisdictionCounty},
		{"county road spelled out", map[string]string{"ref": "County Road 12", "highway": "primary"}, JurisdictionCounty},
		// The ABBREVIATED county spelling, where "CO" collides with Colorado's
		// USPS code. Before spelledCountyRefRe covered these, the collision
		// split identical road types across two different buckets: "CO RD 45"
		// matched statePostalRefRe's [A-Z]{1,2} arm ("RD" followed by a
		// non-letter) and billed a county road to the STATE, while "CO HWY 12"
		// and "CO RTE 9" failed that arm on their three-letter designators and
		// fell through the whole ladder to CITY.
		{"abbreviated county road", map[string]string{"ref": "CO RD 45", "highway": "unclassified"}, JurisdictionCounty},
		{"abbreviated county road lettered", map[string]string{"ref": "CO RD A", "highway": "unclassified"}, JurisdictionCounty},
		{"abbreviated county highway", map[string]string{"ref": "CO HWY 12", "highway": "unclassified"}, JurisdictionCounty},
		{"abbreviated county route", map[string]string{"ref": "CO RTE 9", "highway": "unclassified"}, JurisdictionCounty},
		{"abbreviated county road with period", map[string]string{"ref": "CO. RD 45", "highway": "unclassified"}, JurisdictionCounty},
		{"abbreviated county road hyphenated", map[string]string{"ref": "CO-RD 45", "highway": "unclassified"}, JurisdictionCounty},
		// Mixed case appears in the wild; ClassifyJurisdiction uppercases the
		// ref before any pattern runs.
		{"abbreviated county road mixed case", map[string]string{"ref": "Co Rd 45", "highway": "unclassified"}, JurisdictionCounty},

		// The other side of that collision, and the reason the rule keys on a
		// WORD rather than adding CO to statePostalDeny: every real Colorado
		// route must keep its State classification. Histogramming ^CO over the
		// ingested corpus returns 33 distinct refs and 12118 features, all of
		// them this shape -- denying CO outright would move every one of them
		// into City.
		{"colorado state route stays state", map[string]string{"ref": "CO 121", "highway": "primary"}, JurisdictionState},
		{"colorado state route low number stays state", map[string]string{"ref": "CO 2", "highway": "primary"}, JurisdictionState},
		{"colorado state route with suffix stays state", map[string]string{"ref": "CO 470 EXPR", "highway": "primary"}, JurisdictionState},
		{"colorado route joined with a county ref stays state", map[string]string{"ref": "CO 88;CR 42", "highway": "primary"}, JurisdictionState},
		// Two further designator shapes, found by peer review after the first
		// attempt at this fix required [A-Z]?\d and still missed 75 features.
		// They are why countyRefRe matches on the prefix alone: enumerating
		// shapes loses to the next shape.
		{"CA county route hyphenated after the letter", map[string]string{"ref": "CR S-40", "highway": "primary"}, JurisdictionCounty},
		{"CA county route letter only, no number", map[string]string{"ref": "CR G", "highway": "primary"}, JurisdictionCounty},
		{"CA county route two-letter designator", map[string]string{"ref": "CR LL", "highway": "primary"}, JurisdictionCounty},
		// TxDOT loops and spurs written out. The abbreviated SL/SS forms of
		// the same roads were already State via the permissive default; these
		// were City. 2905 features.
		{"texas loop spelled out", map[string]string{"ref": "Loop 12", "highway": "primary"}, JurisdictionState},
		{"texas spur spelled out", map[string]string{"ref": "Spur 303", "highway": "primary"}, JurisdictionState},
		// Florida's lettered state road, 466 features in Jacksonville.
		{"florida lettered state road", map[string]string{"ref": "SR A1A", "highway": "primary"}, JurisdictionState},

		// The negative side of the same rules. A street whose NAME merely
		// starts with a route word is not a route: the trailing \d in
		// stateWordRefRe and the \b in spelledCountyRefRe are what hold this
		// line, and dropping either silently moves real city streets out of
		// the city's funding obligation.
		{"street named Loop Road is not a state loop", map[string]string{"ref": "Loop Road", "highway": "residential"}, JurisdictionCity},
		{"street named Spur Trail is not a state spur", map[string]string{"ref": "Spur Trail", "highway": "residential"}, JurisdictionCity},
		{"street named County Line Road is not a county route", map[string]string{"ref": "County Line Road", "highway": "residential"}, JurisdictionCity},
		// The \b guarding the CO RD family: without it these two would read as
		// county routes rather than the ordinary city streets they are.
		{"street named CO ROUTED is not a county route", map[string]string{"ref": "CO ROUTED", "highway": "residential"}, JurisdictionCity},
		{"street named CO RDS is not a county route", map[string]string{"ref": "CO RDS", "highway": "residential"}, JurisdictionCity},

		// Left as State deliberately -- see the statePostalDeny comment.
		// BW 8 is Beltway 8, genuinely TxDOT. PR is Texas "Private Road", and
		// State keeps a road the city does not maintain out of the city's
		// cohort area. OS is unidentified on 19 features.
		{"houston beltway stays state", map[string]string{"ref": "BW 8"}, JurisdictionState},
		{"texas private road stays state", map[string]string{"ref": "PR 1836"}, JurisdictionState},
		{"unidentified OS prefix stays state", map[string]string{"ref": "OS 15"}, JurisdictionState},

		// solvent-streets-m0qa. statePostalRefRe required TWO letters, so the
		// route conventions that number with a SINGLE one matched nothing and
		// fell all the way through to City -- 1648 features billing state
		// highway to the city. Every ref below is a real corpus ref, at the
		// highway class it actually carries there.
		//
		// The bucket is per-prefix and measured, not pattern-derived: C is
		// Columbus OH's Franklin COUNTY roads, which is why a bare
		// ^[A-Z][ -]\d would have been wrong even before it swallowed the
		// 65385 Interstate features.
		{"michigan state route", map[string]string{"ref": "M 1", "highway": "primary"}, JurisdictionState},
		{"nebraska state route", map[string]string{"ref": "N-64", "highway": "primary"}, JurisdictionState},
		{"kansas state route", map[string]string{"ref": "K-32", "highway": "primary"}, JurisdictionState},
		{"nebraska link route", map[string]string{"ref": "L-28K", "highway": "primary"}, JurisdictionState},
		{"north carolina secondary route", map[string]string{"ref": "S-29-411", "highway": "unclassified"}, JurisdictionState},
		{"ohio county road single letter", map[string]string{"ref": "C-138", "highway": "tertiary"}, JurisdictionCounty},
		// Michigan is spaced in the corpus and Nebraska hyphenated, but both
		// separators are accepted for both -- see singleLetterStateRefRe.
		{"michigan state route hyphenated", map[string]string{"ref": "M-5", "highway": "primary"}, JurisdictionState},
		{"nebraska state route spaced", map[string]string{"ref": "N 64", "highway": "primary"}, JurisdictionState},

		// Missouri's lettered supplementals, 81 features in Kansas City MO.
		// statePostalRefRe required a DIGIT after the separator; these put a
		// letter there, which is the same trap SR A1A and CR S21 sprang.
		{"missouri supplemental route", map[string]string{"ref": "MO W", "highway": "primary"}, JurisdictionState},
		{"missouri supplemental doubled letter", map[string]string{"ref": "MO AA", "highway": "tertiary"}, JurisdictionState},

		// The negative side of both rules above. The trailing \d in
		// singleLetterStateRefRe is what keeps a street whose ref is its own
		// name out of the State bucket, and the ([^A-Z]|$) cap in
		// statePostalRefRe is what keeps Washington DC's junk
		// ref="DC GOVERNMENT" (1 residential way, the ONLY thing besides the
		// Missouri routes that the letter-designator relaxation newly
		// matches) from being billed to the state -- DC the city is the
		// maintaining entity there, so City is right.
		{"street whose ref is its name is not a state route", map[string]string{"ref": "M Street", "highway": "residential"}, JurisdictionCity},
		{"junk DC GOVERNMENT ref stays city", map[string]string{"ref": "DC GOVERNMENT", "highway": "residential"}, JurisdictionCity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyJurisdiction(tt.tags)
			if got != tt.want {
				t.Errorf("ClassifyJurisdiction(%v) = %s, want %s", tt.tags, got, tt.want)
			}
		})
	}
}

// TestIsStateRefDenyList pins the deny-list contract directly. statePostalDeny
// is a deny list on purpose: every ref below that is expected true would be
// lost to the City bucket if it were replaced by an allow list of real USPS
// two-letter codes. TestFederalRefBeatsStatePostalDeny covers the classify-time
// outcome for the deny-listed federal prefixes; this test covers isStateRef.
func TestIsStateRefDenyList(t *testing.T) {
	tests := []struct {
		ref  string
		want bool
	}{
		// Real postal-code state routes.
		{"CA 84", true},
		{"CO 2", true},
		{"OR-99E", true},
		// Non-postal but state maintained -- must stay true.
		{"FM 1960", true}, // TxDOT Farm-to-Market
		{"RM 620", true},  // TxDOT Ranch-to-Market
		{"SL 8", true},    // Texas state loop
		{"SS 55", true},   // Texas state spur
		{"BU 287", true},  // Texas business US route
		{"BS 6", true},    // Texas business state route
		{"TH 5", true},    // Minnesota trunk highway (MnDOT)
		{"NF 5", true},    // Forest Service: federally administered, but not
		// deny-listed -- see statePostalDeny's comment.
		// Deny-listed.
		{"CR 12", false}, // county route
		{"US 50", false}, // federal (also caught earlier by federalRefRe)
		{"IH 35", false}, // federal interstate (also caught by federalRefRe)
		{"BR 1", false},  // business route
		// solvent-streets-m0qa: single-letter and lettered designators.
		{"M 1", true},   // Michigan, MDOT
		{"K-32", true},  // Kansas
		{"MO W", true},  // MoDOT supplemental route
		{"MO AA", true}, // ...including the doubled letter
		// C is the County half of the single-letter allow list, so
		// ClassifyJurisdiction -- not isStateRef -- is what claims it.
		{"C-138", false},
		// The trailing \d and the two-letter designator cap.
		{"M Street", false},
		{"DC GOVERNMENT", false},
		// Not a two-letter+digit ref at all.
		{"Main Street", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			if got := isStateRef(tt.ref); got != tt.want {
				t.Errorf("isStateRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

// TestFederalRefBeatsStatePostalDeny pins which rule wins for "IH": the ref is
// on statePostalDeny (so isStateRef says no) AND matched by federalRefRe. The
// federal rule must be what produces the classification -- if only the deny
// entry existed, "IH 35" would fall through to JurisdictionCity.
func TestFederalRefBeatsStatePostalDeny(t *testing.T) {
	for _, ref := range []string{"IH 35", "IH-10", "US 50", "US-101"} {
		t.Run(ref, func(t *testing.T) {
			if !federalRefRe.MatchString(ref) {
				t.Fatalf("federalRefRe does not match %q; it would be classified City", ref)
			}
			if isStateRef(ref) {
				t.Errorf("isStateRef(%q) = true, want false (deny-listed)", ref)
			}
			if got := ClassifyJurisdiction(map[string]string{"ref": ref}); got != JurisdictionFederal {
				t.Errorf("ClassifyJurisdiction(ref=%q) = %s, want federal", ref, got)
			}
		})
	}
}

// TestFederalOperatorWordBoundary pins ONLY the \b guard in federalOperatorRe:
// these operators contain the literal substring "us department of
// transportation" / "us dot" and a strings.Contains implementation would call
// them federal. It deliberately does not assert their state-vs-city verdict.
//
// "Columbus Department of Transportation" is a city street department (Columbus
// OH is in examples/top-50-cities). Its state-vs-city verdict is pinned by
// TestMunicipalTransportationDepartments, not here; this test stays narrowly
// about the federal \b guard so a regression in either rule names itself.
func TestFederalOperatorWordBoundary(t *testing.T) {
	for _, operator := range []string{
		"columbus department of transportation",
		"columbus dot",
		"massachusetts department of transportation",
		"corpus christi dot",
	} {
		t.Run(operator, func(t *testing.T) {
			if isFederalOperator(operator) {
				t.Errorf("isFederalOperator(%q) = true, want false; the \\b guard in federalOperatorRe regressed", operator)
			}
		})
	}
}

// TestFederalAndStateOperator pins that the federal DOT is recognised as
// federal and simultaneously rejected by isStateOperator, while state DOTs in
// either word order stay state and city/county DOTs stay excluded.
func TestFederalAndStateOperator(t *testing.T) {
	tests := []struct {
		operator    string // already lowercased, as ClassifyJurisdiction does
		wantFederal bool
		wantState   bool
	}{
		{"us department of transportation", true, false},
		{"u.s. department of transportation", true, false},
		{"united states department of transportation", true, false},
		{"us transportation department", true, false},
		{"usdot", true, false},
		{"us dot", true, false},
		{"federal highway administration", true, false},
		// State DOTs, both word orders.
		{"nevada department of transportation", false, true},
		{"nevada transportation department", false, true},
		{"new york state transportation department", false, true},
		{"caltrans", false, true},
		{"massdot", false, true},
		{"oregon state highway division", false, true},
		// "<State> Department of Transportation" is state-name anchored the
		// same way the reversed order is, so these must survive the anchoring.
		{"washington state department of transportation", false, true},
		{"new york state department of transportation", false, true},
		{"colorado dot", false, true},
		// Louisiana's agency is "Department of Transportation and
		// Development". The pattern ends on a word boundary, so the trailing
		// words neither break the match nor land in the qualifier.
		{"louisiana department of transportation and development", false, true},
		// No qualifier at all: a bare "Department of Transportation" on a road
		// is the state's own agency as OSM uses the tag. Articles and a
		// leading "state" are absorbed by the pattern, not read as a city.
		{"department of transportation", false, true},
		{"department of transportation and development", false, true},
		{"the state department of transportation", false, true},
		// City agencies using the "<X> Department of Transportation" word
		// order -- solvent-streets-niak. None contain "city"/"municipal", so
		// only the state-name anchoring keeps them out of the State bucket.
		{"columbus department of transportation", false, false},
		{"district department of transportation", false, false},
		{"columbus dot", false, false},
		{"corpus christi dot", false, false},
		// State-named city prefixes in this word order too: the state name has
		// to END the qualifier, and here it is followed by another word.
		{"virginia beach department of transportation", false, false},
		{"kansas city department of transportation", false, false},
		// City agencies using the "<X> Transportation Department" word order.
		// None contain "city"/"municipal", so only the state-name anchoring
		// in stateTransportationDeptRe keeps them out of the State bucket.
		{"boston transportation department", false, false},
		{"austin transportation department", false, false},
		{"phoenix street transportation department", false, false},
		// State-named city prefixes: the required whitespace+"transportation"
		// after the state name is what rejects these.
		{"virginia beach transportation department", false, false},
		{"kansas city transportation department", false, false},
		{"washington township transportation department", false, false},
		// Not state, not federal.
		{"los angeles county transportation department", false, false},
		{"city of reno transportation department", false, false},
		{"", false, false},
		// solvent-streets-q48z.2: state DOTs named by ABBREVIATION or by a
		// territory. Anchoring the qualifier on the 52 spelled-out state
		// names alone dropped all of these into City.
		{"nys department of transportation", false, true},
		{"nc department of transportation", false, true},
		{"n.c. department of transportation", false, true},
		{"mass department of transportation", false, true},
		{"pa dot", false, true},
		{"guam department of transportation", false, true},
		{"us virgin islands department of transportation", false, true},
		// The two codes deliberately left OUT of stateAbbrev, both far more
		// often a city than a state. Omitting them preserves today's answer
		// rather than changing it.
		{"la department of transportation", false, false},
		{"dc department of transportation", false, false},
		// An operator that explicitly disclaims a DOT is not naming one.
		// Without notDOTRe this reaches the bare-"dot" fallback and is State.
		{"not fdot", false, false},
		// ...and the same disclaimer aimed at a FEDERAL agency must not come
		// back Federal. ClassifyJurisdiction asks isFederalOperator first, so
		// guarding only isStateOperator left these inverted.
		{"not usdot", false, false},
		// ...but only where the disclaimed token ends in "dot", which is all
		// notDOTRe claims to match. "not fhwa" stays Federal. Pinned so the
		// boundary is visible rather than surprising the next reader: zero
		// disclaimers of any shape but "not fdot" exist in the corpus, and
		// widening the regex would be guessing at a shape rather than
		// measuring one.
		{"not fhwa", true, false},
		// The leading "the" deptOfTransportationRe leaves in the qualifier is
		// dropped, so this stays State.
		{"the nc department of transportation", false, true},
		// A state CODE at the end of a qualifier is how a CITY is
		// disambiguated, not how a state agency is named. A suffix test would
		// read all three as state agencies and move municipal lane area out of
		// the City cohort -- solvent-streets-niak's direction, one
		// abbreviation at a time. isStateQualifier requires the abbreviation
		// to be the WHOLE qualifier for exactly this reason.
		{"charlotte nc department of transportation", false, false},
		{"boston ma department of transportation", false, false},
		{"columbus oh dot", false, false},
		// "co" is in stateAbbrev for Colorado and is also how "County" is
		// abbreviated. Whole-qualifier matching is what keeps this out of the
		// State bucket.
		{"fairfax co dot", false, false},
		{"marin co department of transportation", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.operator, func(t *testing.T) {
			if got := isFederalOperator(tt.operator); got != tt.wantFederal {
				t.Errorf("isFederalOperator(%q) = %v, want %v", tt.operator, got, tt.wantFederal)
			}
			if got := isStateOperator(tt.operator); got != tt.wantState {
				t.Errorf("isStateOperator(%q) = %v, want %v", tt.operator, got, tt.wantState)
			}
		})
	}
}

func TestJurisdictionString(t *testing.T) {
	tests := []struct {
		j    Jurisdiction
		want string
	}{
		{JurisdictionCity, "city"},
		{JurisdictionCounty, "county"},
		{JurisdictionState, "state"},
		{JurisdictionFederal, "federal"},
		{Jurisdiction(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.j.String(); got != tt.want {
			t.Errorf("%d.String() = %s, want %s", tt.j, got, tt.want)
		}
	}
}

func TestPartition(t *testing.T) {
	features := []resource.Feature{
		{ID: "1", Tags: map[string]string{"highway": "residential"}},
		{ID: "2", Tags: map[string]string{"highway": "motorway"}},
		{ID: "3", Tags: map[string]string{"highway": "trunk"}},
		{ID: "4", Tags: map[string]string{"highway": "residential"}},
		{ID: "5", Tags: map[string]string{"highway": "secondary"}},
	}

	parts := Partition(features)
	if len(parts[JurisdictionCity]) != 2 {
		t.Errorf("expected 2 city, got %d", len(parts[JurisdictionCity]))
	}
	if len(parts[JurisdictionFederal]) != 1 {
		t.Errorf("expected 1 federal, got %d", len(parts[JurisdictionFederal]))
	}
	if len(parts[JurisdictionState]) != 1 {
		t.Errorf("expected 1 state, got %d", len(parts[JurisdictionState]))
	}
	if len(parts[JurisdictionCounty]) != 1 {
		t.Errorf("expected 1 county, got %d", len(parts[JurisdictionCounty]))
	}
}

func TestSummary(t *testing.T) {
	features := []resource.Feature{
		{ID: "1", Tags: map[string]string{"highway": "residential"}},
		{ID: "2", Tags: map[string]string{"highway": "motorway"}},
		{ID: "3", Tags: map[string]string{"highway": "residential"}},
	}

	counts := Summary(features)
	if counts[JurisdictionCity] != 2 {
		t.Errorf("expected 2 city, got %d", counts[JurisdictionCity])
	}
	if counts[JurisdictionFederal] != 1 {
		t.Errorf("expected 1 federal, got %d", counts[JurisdictionFederal])
	}
}

// TestRefPrefixesNeverFallToCity is the load-bearing guard for
// solvent-streets-akvx. ClassifyJurisdiction is consumed as a binary
// City / not-City gate everywhere it feeds an output, so reclassifying a ref
// among County/State/Federal cannot move a published number — but routing one
// to City moves lane area into the city cohort and inflates AnnualNeed and
// FundingGap. That makes "not City" the invariant actually worth pinning, and
// it is what makes the akvx reclassification provably area-neutral.
//
// Every prefix here denotes a route some government above the municipality is
// responsible for (or, for PR, a private owner). None should ever reach the
// City fallthrough — including via a well-meant statePostalDeny entry, which
// routes there.
func TestRefPrefixesNeverFallToCity(t *testing.T) {
	refs := []string{
		// Federal
		"I 80", "I-80", "IH 35", "US 101", "NF 61", "FR 2N72", "FS 370",
		// County
		"CR 12", "CH 93", "CC 215", "TR 40",
		// County, lettered and spelled forms (solvent-streets-qwfo). These
		// are the 9480 features that used to fall through to City.
		"CR S21", "CR N8", "CR G8", "CR-S6", "CTH PP", "CTH E",
		"County Highway S21", "County Road 12",
		"CR S-40", "CR G", "CR LL", "CR H",
		// State: postal-code routes, the explicit SR/SH forms, and the
		// non-postal prefixes deliberately left as State.
		"CA 84", "SR 84", "OR-99E", "BW 8", "PR 1836", "OS 15",
		// State, spelled and lettered forms (solvent-streets-qwfo). "Loop 12"
		// and "Spur 303" are the written-out twins of the SL/SS entries below,
		// which were already State; "SR A1A" is the lettered twin of "SR 84".
		"Loop 12", "Spur 303", "SR A1A",
		// Texas state-maintained non-postal prefixes. These sit next to the
		// new federal alternatives in the same regex and are easy to break:
		// under an allow list of real USPS codes they would stop matching
		// isStateRef and land in City, which is exactly what statePostalDeny's
		// comment warns against.
		"FM 1960", "RM 620", "SL 8", "SS 6", "BU 59", "BS 6", "TH 5",
		// Single-letter and lettered-designator conventions
		// (solvent-streets-m0qa). These are the 1729 features that used to
		// fall through to City; C-138 is County and the rest State, but the
		// invariant this test pins is only that none of them is City.
		"M 1", "N-64", "K-32", "L-28K", "S-29-64", "C-138", "MO W",
	}
	for _, ref := range refs {
		if got := ClassifyJurisdiction(map[string]string{"ref": ref}); got == JurisdictionCity {
			t.Errorf("ref %q classified City; it must land in a non-city bucket "+
				"or its lane area joins the city's funding obligation", ref)
		}
	}
}

// TestMunicipalTransportationDepartments pins solvent-streets-niak end to end,
// through ClassifyJurisdiction rather than the operator predicates alone.
//
// The bug: isStateOperator tested for the bare substring "department of
// transportation", so "Columbus Department of Transportation" (Columbus OH
// ships in examples/top-50-cities) and DC's "District Department of
// Transportation" put municipally-maintained lane area in the State cohort,
// understating both cities' AnnualNeed and FundingGap. The reversed word order
// had been anchored on a state name for exactly this reason; the anchoring was
// applied to one order only.
//
// highway=secondary is covered deliberately: ClassifyJurisdiction routes a
// secondary way to County unless isCityOperator claims it, so fixing only
// isStateOperator would have moved this lane area from State to County rather
// than to City -- a different wrong answer, and one the published numbers could
// not tell apart from the first.
func TestMunicipalTransportationDepartments(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want Jurisdiction
	}{
		{
			name: "columbus dot residential",
			tags: map[string]string{"highway": "residential", "operator": "Columbus Department of Transportation"},
			want: JurisdictionCity,
		},
		{
			name: "columbus dot secondary",
			tags: map[string]string{"highway": "secondary", "operator": "Columbus Department of Transportation"},
			want: JurisdictionCity,
		},
		{
			name: "ddot secondary",
			tags: map[string]string{"highway": "secondary", "operator": "District Department of Transportation"},
			want: JurisdictionCity,
		},
		{
			name: "columbus dot abbreviated",
			tags: map[string]string{"highway": "residential", "operator": "Columbus DOT"},
			want: JurisdictionCity,
		},
		{
			name: "state dot stays state",
			tags: map[string]string{"highway": "residential", "operator": "Ohio Department of Transportation"},
			want: JurisdictionState,
		},
		{
			name: "louisiana dotd stays state",
			tags: map[string]string{"highway": "residential", "operator": "Louisiana Department of Transportation and Development"},
			want: JurisdictionState,
		},
		{
			name: "unqualified dot stays state",
			tags: map[string]string{"highway": "residential", "operator": "Department of Transportation"},
			want: JurisdictionState,
		},
		{
			name: "federal dot stays federal",
			tags: map[string]string{"highway": "residential", "operator": "US Department of Transportation"},
			want: JurisdictionFederal,
		},
		{
			name: "county dot stays county",
			tags: map[string]string{"highway": "residential", "operator": "Fairfax County Department of Transportation"},
			want: JurisdictionCounty,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyJurisdiction(tt.tags); got != tt.want {
				t.Errorf("ClassifyJurisdiction(%v) = %s, want %s", tt.tags, got, tt.want)
			}
		})
	}
}

// TestAbbreviatedStateTransportationAgencies pins solvent-streets-q48z.2 end to
// end, through ClassifyJurisdiction rather than the operator predicates alone.
//
// The regression: anchoring the DOT qualifier on stateNameSuffixRe -- the 52
// spelled-out state names -- meant a state DOT named by abbreviation or by a
// territory failed isStateTransportationAgency, PASSED
// isLocalTransportationAgency, and fell through to City. The bare
// strings.Contains(operator, "department of transportation") it replaced had
// caught all of them.
//
// Covering highway=secondary as well as residential is deliberate, because the
// two halves of the fix fail DIFFERENTLY and a residential-only table cannot
// tell the second failure from success:
//
//   - Fix only isStateTransportationAgency and these stay City -- isStateOperator
//     bails on isCityOperator first, and isLocalTransportationAgency still
//     claims them.
//   - Fix only isLocalTransportationAgency and the secondary ways land in
//     County, via ClassifyJurisdiction's "secondary unless a city operator"
//     rule, while the residential ones correctly reach... City, which is wrong.
//
// Only the shared isStateQualifier predicate makes every row below pass.
func TestAbbreviatedStateTransportationAgencies(t *testing.T) {
	states := []string{
		"NYS Department of Transportation",
		"NC Department of Transportation",
		"N.C. Department of Transportation",
		"Mass Department of Transportation",
		"Guam Department of Transportation",
		"US Virgin Islands Department of Transportation",
		"Ohio Department of Transportation", // spelled-out control
	}
	// Not abbreviations of a state, whatever they look like: LA is Los
	// Angeles and DC is the city that maintains its own roads. Both must stay
	// City, and in DC's case moving it would be the wrong direction outright.
	cities := []string{
		"LA Department of Transportation",
		"DC Department of Transportation",
		"District Department of Transportation",
		// "<City> <ST>" -- the ordinary way to disambiguate a city, and the
		// reason isStateQualifier matches the whole qualifier rather than its
		// last token. Caught in review of the commit that added the function:
		// a suffix test made all three State, taking municipal lane area out
		// of the city's area, AnnualNeed and FundingGap.
		"Charlotte NC Department of Transportation",
		"Boston MA Department of Transportation",
		"Columbus OH DOT",
		// "co" is Colorado in stateAbbrev and "County" in the wild. County
		// would be the better bucket for these two, but City is the answer
		// this file gave before stateAbbrev existed and the point here is that
		// stateAbbrev did not change it; the County question is its own.
		"Fairfax Co DOT",
		"Marin Co Department of Transportation",
	}
	// highway is varied because the two half-fixes land in different buckets.
	for _, highway := range []string{"residential", "secondary"} {
		for _, operator := range states {
			t.Run(highway+"/"+operator, func(t *testing.T) {
				tags := map[string]string{"highway": highway, "operator": operator}
				if got := ClassifyJurisdiction(tags); got != JurisdictionState {
					t.Errorf("ClassifyJurisdiction(%v) = %s, want state", tags, got)
				}
			})
		}
		for _, operator := range cities {
			t.Run(highway+"/"+operator, func(t *testing.T) {
				tags := map[string]string{"highway": highway, "operator": operator}
				if got := ClassifyJurisdiction(tags); got != JurisdictionCity {
					t.Errorf("ClassifyJurisdiction(%v) = %s, want city", tags, got)
				}
			})
		}
	}
}

// TestOperatorDisclaimingADOTIsNotState pins the notDOTRe guard end to end.
// operator="not fdot" is 9 real features -- Tampa primary 4, Jacksonville
// secondary 5 -- and it is the only standalone "not" in any operator or network
// value across the corpus. Before the guard it reached isStateOperator's
// bare-"dot" fallback (spacedDOTRe needs a word boundary before "dot" and
// "fdot" has none) and classified State, the exact opposite of what the tag
// says.
//
// The guard only makes isStateOperator decline it; the buckets below are the
// ordinary ladder's call afterwards, and the primary one is the only change in
// this slice that moves features INTO City.
//
// Only 5 of the 9 actually move: 4 of the Jacksonville secondaries carry
// ref="CR 228", so countyRefRe routes them County before the operator is ever
// consulted. The ref rules running ahead of the operator rules is why the
// bucket here is worth pinning through ClassifyJurisdiction rather than
// through isStateOperator alone.
func TestOperatorDisclaimingADOTIsNotState(t *testing.T) {
	tests := []struct {
		highway string
		want    Jurisdiction
	}{
		{"primary", JurisdictionCity},     // Tampa, 4 features
		{"secondary", JurisdictionCounty}, // Jacksonville, 5 features
		{"residential", JurisdictionCity},
	}
	for _, tt := range tests {
		t.Run(tt.highway, func(t *testing.T) {
			tags := map[string]string{"highway": tt.highway, "operator": "not FDOT"}
			if got := ClassifyJurisdiction(tags); got != tt.want {
				t.Errorf("ClassifyJurisdiction(%v) = %s, want %s", tags, got, tt.want)
			}
		})
	}
}
