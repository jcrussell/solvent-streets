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
