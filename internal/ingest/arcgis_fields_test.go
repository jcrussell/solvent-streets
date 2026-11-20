package ingest

import (
	"testing"

	"github.com/jcrussell/solvent-streets/internal/filter"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// TestRoadFeaturesFromRows_AlamedaClasses pins class derivation on rows
// shaped like Alameda County's Street_Centerlines: each functional class
// becomes the OSM highway value that gives it the right cohort and
// jurisdiction, and the name comes from STREET.
func TestRoadFeaturesFromRows_AlamedaClasses(t *testing.T) {
	tests := []struct {
		class       string
		wantHighway string
		wantCohort  string
		wantJur     filter.Jurisdiction
	}{
		{"Interstate", "motorway", "motorway", filter.JurisdictionFederal},
		{"Ramp", "motorway_link", "motorway", filter.JurisdictionFederal},
		{"Freeway or Expressway", "trunk", "trunk", filter.JurisdictionState},
		{"Principal Arterial", "primary", "primary", filter.JurisdictionCity},
		{"Minor Arterial", "secondary", "secondary", filter.JurisdictionCity},
		{"Major Collector", "tertiary", "tertiary", filter.JurisdictionCity},
		{"Minor Collector", "tertiary", "tertiary", filter.JurisdictionCity},
		{"Local", "residential", "residential", filter.JurisdictionCity},
	}
	for _, tt := range tests {
		t.Run(tt.class, func(t *testing.T) {
			rows := []ArcGISRow{{ID: "1", Props: map[string]string{
				"CLASS": tt.class, "STREET": "N LIVERMORE AVE", "SFEANME": "LIVERMORE", "MUNL": "LI",
			}}}
			f := roadFeaturesFromRows(rows, resource.TypeRoads)[0]
			if got := f.Tags["highway"]; got != tt.wantHighway {
				t.Errorf("highway = %q, want %q", got, tt.wantHighway)
			}
			if got := forecast.NormalizeClass(f.Tags["highway"]); got != tt.wantCohort {
				t.Errorf("cohort = %q, want %q", got, tt.wantCohort)
			}
			if got := filter.ClassifyJurisdiction(f.Tags); got != tt.wantJur {
				t.Errorf("jurisdiction = %s, want %s", got, tt.wantJur)
			}
			if f.Name != "N LIVERMORE AVE" {
				t.Errorf("name = %q, want the STREET value", f.Name)
			}
			if rows[0].Props["highway"] != "" {
				t.Error("the row's own properties were modified")
			}
		})
	}
}

func TestArcGISHighway(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]string
		want  string
	}{
		{"numeric F_SYSTEM", map[string]string{"F_SYSTEM": "1"}, "motorway"},
		{"numeric local", map[string]string{"f_system": "7"}, "residential"},
		// HERE/NAVTEQ FUNC_CLASS 5 is a local street, not a major collector;
		// digits outside F_SYSTEM map to nothing.
		{"HERE-style FUNC_CLASS is not HPMS", map[string]string{"FUNC_CLASS": "5"}, ""},
		{"digits in CLASS fall through to F_SYSTEM", map[string]string{"CLASS": "2", "F_SYSTEM": "7"}, "residential"},
		{"Interstate ramp by street name", map[string]string{"CLASS": "Ramp", "STREET": "VASCO OFF I580 EB"}, "motorway_link"},
		{"ramp whose name names no route", map[string]string{"CLASS": "Ramp", "STREET": "VASCO ON OFF WB"}, "motorway_link"},
		{"state-route ramp by street name", map[string]string{"CLASS": "Ramp", "STREET": "SR 24 WB ON RAMP"}, "trunk_link"},
		{"state-route ramp by hyphenated name", map[string]string{"CLASS": "Ramp", "FULLNAME": "Hwy-92 Off Ramp"}, "trunk_link"},
		{"state-route ramp value", map[string]string{"CLASS": "State Route Ramp", "STREET": "A ST"}, "trunk_link"},
		{"Interstate ramp value wins over a name", map[string]string{"CLASS": "Interstate Ramp", "STREET": "SR 84 CONN"}, "motorway_link"},
		{"route-like word is not a route", map[string]string{"CLASS": "Ramp", "STREET": "HIGHWAY RD"}, "motorway_link"},
		{"lower-case field", map[string]string{"class": "Minor Arterial"}, "secondary"},
		{"other principal arterial", map[string]string{"FUNC_CLASS": "Other Principal Arterial"}, "primary"},
		{"interstate ramp is a ramp", map[string]string{"CLASS": "Interstate Ramp"}, "motorway_link"},
		{"unknown vocabulary", map[string]string{"CLASS": "Alley"}, ""},
		{"unknown code", map[string]string{"F_SYSTEM": "9"}, ""},
		{"no class field", map[string]string{"SFEATYP": "RAMP"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := arcgisHighway(tt.props); got != tt.want {
				t.Errorf("arcgisHighway(%v) = %q, want %q", tt.props, got, tt.want)
			}
		})
	}
}

// TestRoadFeaturesFromRows_KeepsOwnTags: a layer that already carries an OSM
// highway tag keeps it, and FULLNAME wins over STREET.
func TestRoadFeaturesFromRows_KeepsOwnTags(t *testing.T) {
	rows := []ArcGISRow{{ID: "1", Props: map[string]string{
		"highway": "service", "CLASS": "Local", "FullName": "Main St", "STREET": "MAIN ST",
	}}}
	f := roadFeaturesFromRows(rows, resource.TypeRoads)[0]
	if f.Tags["highway"] != "service" {
		t.Errorf("highway = %q, want the row's own service", f.Tags["highway"])
	}
	if f.Name != "Main St" {
		t.Errorf("name = %q, want FULLNAME", f.Name)
	}
}
