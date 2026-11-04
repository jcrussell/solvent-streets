package resource

import "testing"

func TestEstimateParkingSpaces(t *testing.T) {
	cases := []struct {
		name   string
		tags   map[string]string
		area   float64
		want   float64
		source SpaceSource
	}{
		{"capacity wins over area", map[string]string{"capacity": "40"}, 3000, 40, SpaceSourceCapacity},
		{"capacity with whitespace", map[string]string{"capacity": " 12 "}, 0, 12, SpaceSourceCapacity},
		{"area fallback floors", map[string]string{}, 95, 3, SpaceSourceArea},
		{"garbage capacity falls back", map[string]string{"capacity": "yes"}, 300, 10, SpaceSourceArea},
		{"range capacity falls back", map[string]string{"capacity": "50;60"}, 300, 10, SpaceSourceArea},
		{"approximate capacity falls back", map[string]string{"capacity": "~50"}, 300, 10, SpaceSourceArea},
		{"zero capacity falls back", map[string]string{"capacity": "0"}, 300, 10, SpaceSourceArea},
		{"multi-storey × parking:levels", map[string]string{"parking": "multi-storey", "parking:levels": "3"}, 3000, 300, SpaceSourceArea},
		{"underground × levels fallback key", map[string]string{"parking": "underground", "levels": "2"}, 3000, 200, SpaceSourceArea},
		{"rooftop × building:levels", map[string]string{"parking": "rooftop", "building:levels": "4"}, 300, 40, SpaceSourceArea},
		{"levels 1 is no multiplier", map[string]string{"parking": "multi-storey", "parking:levels": "1"}, 3000, 100, SpaceSourceArea},
		{"levels garbage is no multiplier", map[string]string{"parking": "multi-storey", "parking:levels": "abc"}, 3000, 100, SpaceSourceArea},
		{"surface lot ignores levels", map[string]string{"parking": "surface", "levels": "3"}, 3000, 100, SpaceSourceArea},
		{"capacity still wins for a garage", map[string]string{"parking": "multi-storey", "parking:levels": "3", "capacity": "250"}, 3000, 250, SpaceSourceCapacity},
		{"too small for one space", map[string]string{}, 20, 0, SpaceSourceNone},
		{"zero area", map[string]string{}, 0, 0, SpaceSourceNone},
		{"nil tags", nil, 60, 2, SpaceSourceArea},
	}
	for _, tc := range cases {
		got, src := EstimateParkingSpaces(tc.tags, tc.area)
		if got != tc.want || src != tc.source {
			t.Errorf("%s: EstimateParkingSpaces(%v, %v) = (%v, %q), want (%v, %q)", tc.name, tc.tags, tc.area, got, src, tc.want, tc.source)
		}
	}
}

func TestSpacesMetricKey(t *testing.T) {
	cases := map[Type]string{
		TypeParking:                  SpacesMetricAll,
		TypeParking.With(ScopeCity):  SpacesMetricCity,
		TypeRoads:                    "",
		TypeRoads.With(ScopeCity):    "",
		TypeSidewalks:                "",
		TypeCombined.With(ScopeCity): "",
	}
	for in, want := range cases {
		if got := SpacesMetricKey(in); got != want {
			t.Errorf("SpacesMetricKey(%q) = %q, want %q", in, got, want)
		}
	}
}
