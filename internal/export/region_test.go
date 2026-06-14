package export

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCityInfoRegionJSONRoundTrip asserts the region field omits from JSON
// when empty and round-trips when set.
func TestCityInfoRegionJSONRoundTrip(t *testing.T) {
	// Empty region must be omitted (omitempty).
	empty := CityInfo{Slug: "denver", Name: "Denver"}
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty-region city: %v", err)
	}
	if strings.Contains(string(b), "region") {
		t.Errorf("empty region should be omitted from JSON, got %s", b)
	}

	// Set region must serialize and round-trip.
	set := CityInfo{Slug: "oakland", Name: "Oakland", Region: "Bay Area"}
	b, err = json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal regioned city: %v", err)
	}
	if !strings.Contains(string(b), `"region":"Bay Area"`) {
		t.Errorf("expected region in JSON, got %s", b)
	}
	var back CityInfo
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Region != "Bay Area" {
		t.Errorf("region did not round-trip: got %q", back.Region)
	}
}

// TestGroupCitiesByRegion asserts grouping order: non-empty regions first
// (ascending by label), cities ascending by name within each, and the
// empty-region group last.
func TestGroupCitiesByRegion(t *testing.T) {
	cities := []CityInfo{
		{Name: "Zephyr"},                      // empty region
		{Name: "Oakland", Region: "Bay Area"}, //
		{Name: "Boulder", Region: "Colorado"}, //
		{Name: "Alameda", Region: "Bay Area"}, //
		{Name: "Aspen"},                       // empty region
		{Name: "Denver", Region: "Colorado"},  //
	}

	got := GroupCitiesByRegion(cities)
	if len(got) != 3 {
		t.Fatalf("expected 3 groups (Bay Area, Colorado, empty), got %d: %+v", len(got), got)
	}

	// Group order: Bay Area, Colorado, then "" last.
	wantRegions := []string{"Bay Area", "Colorado", ""}
	for i, w := range wantRegions {
		if got[i].Region != w {
			t.Errorf("group[%d].Region = %q; want %q", i, got[i].Region, w)
		}
	}

	// Cities within Bay Area sorted by name: Alameda, Oakland.
	if names := cityNames(got[0].Cities); !equalSlice(names, []string{"Alameda", "Oakland"}) {
		t.Errorf("Bay Area cities = %v; want [Alameda Oakland]", names)
	}
	// Colorado: Boulder, Denver.
	if names := cityNames(got[1].Cities); !equalSlice(names, []string{"Boulder", "Denver"}) {
		t.Errorf("Colorado cities = %v; want [Boulder Denver]", names)
	}
	// Empty-region group sorted by name: Aspen, Zephyr.
	if names := cityNames(got[2].Cities); !equalSlice(names, []string{"Aspen", "Zephyr"}) {
		t.Errorf("empty-region cities = %v; want [Aspen Zephyr]", names)
	}
}

// TestGroupCitiesByRegion_AllEmpty asserts that when no city has a region, a
// single empty-region group is returned (rendered as bare options).
func TestGroupCitiesByRegion_AllEmpty(t *testing.T) {
	cities := []CityInfo{{Name: "B"}, {Name: "A"}}
	got := GroupCitiesByRegion(cities)
	if len(got) != 1 {
		t.Fatalf("expected 1 group, got %d", len(got))
	}
	if got[0].Region != "" {
		t.Errorf("group region = %q; want empty", got[0].Region)
	}
	if names := cityNames(got[0].Cities); !equalSlice(names, []string{"A", "B"}) {
		t.Errorf("cities = %v; want [A B]", names)
	}
}

// TestGroupCitiesByRegion_Empty asserts nil in, nil out.
func TestGroupCitiesByRegion_Empty(t *testing.T) {
	if got := GroupCitiesByRegion(nil); got != nil {
		t.Errorf("expected nil for empty input, got %+v", got)
	}
}

func cityNames(cs []CityInfo) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
