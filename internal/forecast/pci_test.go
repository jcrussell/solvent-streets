package forecast

import (
	"maps"
	"testing"
)

// TestClassTable pins, for every class the tables name and the shapes of
// input around them, the cohort class NormalizeClass files a highway tag under
// and the decay rate DecayRateForClass gives it. The expected values are
// written out by hand rather than read from the tables: NormalizeClass keys
// the stored cohort stats (a change to its output needs a
// config.ComputeEpoch bump), and the rates set every forecast.
func TestClassTable(t *testing.T) {
	cases := []struct {
		in    string
		class string
		rate  float64
	}{
		// The highway classes that are their own cohort.
		{"motorway", "motorway", 0.0105},
		{"trunk", "trunk", 0.014},
		{"primary", "primary", 0.0175},
		{"secondary", "secondary", 0.021},
		{"tertiary", "tertiary", 0.0245},
		{"residential", "residential", 0.028},
		{"service", "service", 0.0315},

		// Link ramps take their parent's class and rate.
		{"motorway_link", "motorway", 0.0105},
		{"trunk_link", "trunk", 0.014},
		{"primary_link", "primary", 0.0175},
		{"secondary_link", "secondary", 0.021},
		{"tertiary_link", "tertiary", 0.0245},
		{"residential_link", "residential", 0.028},
		{"service_link", "service", 0.0315},

		// Every other highway value is a residential cohort; its rate, asked
		// of the raw tag, is the default.
		{"living_street", "residential", 0.0245},
		{"unclassified", "residential", 0.0245},
		{"track", "residential", 0.0245},
		{"footway", "residential", 0.0245},
		{"unknown", "residential", 0.0245},
		{"", "residential", 0.0245},
		{"_link", "residential", 0.0245},
		{"Motorway", "residential", 0.0245},

		// Only one _link suffix is stripped.
		{"motorway_link_link", "residential", 0.0245},

		// The non-highway keys of the rate table are not cohort classes:
		// they keep their rate and normalize to residential.
		{"roads", "residential", 0.0245},
		{"roads_link", "residential", 0.0245},
		{"sidewalks", "residential", 0.0175},
		{"sidewalks_link", "residential", 0.0175},
		{"default", "residential", 0.0245},
		{"default_link", "residential", 0.0245},
		{"parking", "residential", 0.0245},
	}
	for _, tc := range cases {
		if got := NormalizeClass(tc.in); got != tc.class {
			t.Errorf("NormalizeClass(%q) = %q, want %q", tc.in, got, tc.class)
		}
		if got := DecayRateForClass(tc.in); got != tc.rate {
			t.Errorf("DecayRateForClass(%q) = %v, want %v", tc.in, got, tc.rate)
		}
	}
}

// TestDecayTables pins the exported rate tables key by key, and the facts the
// rest of the package reads off them.
func TestDecayTables(t *testing.T) {
	wantRoad := map[string]float64{
		"motorway":    0.0105,
		"trunk":       0.014,
		"primary":     0.0175,
		"secondary":   0.021,
		"tertiary":    0.0245,
		"residential": 0.028,
		"service":     0.0315,
		"roads":       0.0245,
	}
	wantAll := map[string]float64{"sidewalks": 0.0175, "default": 0.0245}
	maps.Copy(wantAll, wantRoad)
	for name, tbl := range map[string]struct{ got, want map[string]float64 }{
		"RoadDecayRates":    {RoadDecayRates, wantRoad},
		"DefaultDecayRates": {DefaultDecayRates, wantAll},
	} {
		if len(tbl.got) != len(tbl.want) {
			t.Errorf("%s has %d keys, want %d: %v", name, len(tbl.got), len(tbl.want), tbl.got)
		}
		for k, want := range tbl.want {
			if got, ok := tbl.got[k]; !ok || got != want {
				t.Errorf("%s[%q] = %v (present %v), want %v", name, k, got, ok, want)
			}
		}
	}

	if got := ResolveDecayRate(0); got != 0.0245 {
		t.Errorf("ResolveDecayRate(0) = %v, want 0.0245", got)
	}
	if roadDecayAnchor != 0.0245 {
		t.Errorf("roadDecayAnchor = %v, want 0.0245", roadDecayAnchor)
	}
	// A cohort class is a road class, and a road class is one NormalizeClass
	// keeps or the aggregate "roads".
	for class := range RoadDecayRates {
		if !IsRoadClass(class) {
			t.Errorf("IsRoadClass(%q) = false", class)
		}
		if kept := NormalizeClass(class) == class; kept == (class == "roads") {
			t.Errorf("NormalizeClass(%q) kept = %v", class, kept)
		}
	}
	for _, class := range []string{"sidewalks", "default", "parking", ""} {
		if IsRoadClass(class) {
			t.Errorf("IsRoadClass(%q) = true", class)
		}
	}
}
