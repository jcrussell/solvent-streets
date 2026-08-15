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
		// NF (Forest Service) routes are federally administered, not state.
		// State is not the right answer -- it is the status quo, pinned only
		// so this batch does not move them. Fixing it needs a federal-ref
		// rule, which is out of scope here.
		{"national forest route unchanged", map[string]string{"ref": "NF 5"}, JurisdictionState},
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
// OH is in examples/top-50-cities), but isStateOperator's pre-existing generic
// "department of transportation" test classifies it State. That is a separate,
// pre-existing gap -- isCityOperator only knows the "city"/"municipal" tokens
// -- and pinning either verdict here would either bake in a known-wrong answer
// or silently claim a fix this batch does not make.
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
