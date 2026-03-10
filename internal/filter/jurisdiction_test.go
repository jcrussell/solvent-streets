package filter

import (
	"testing"

	"pvmt/internal/resource"
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
