package export

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCityInfoTagsJSONRoundTrip asserts the tags field omits from JSON when
// empty and round-trips when set.
func TestCityInfoTagsJSONRoundTrip(t *testing.T) {
	// Empty tags must be omitted (omitempty).
	empty := CityInfo{Slug: "denver", Name: "Denver"}
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal untagged city: %v", err)
	}
	if strings.Contains(string(b), "tags") {
		t.Errorf("empty tags should be omitted from JSON, got %s", b)
	}

	// Set tags must serialize and round-trip.
	set := CityInfo{Slug: "san-jose", Name: "San Jose", Tags: []string{"Bay Area", "Top 50"}}
	b, err = json.Marshal(set)
	if err != nil {
		t.Fatalf("marshal tagged city: %v", err)
	}
	if !strings.Contains(string(b), `"tags":["Bay Area","Top 50"]`) {
		t.Errorf("expected tags in JSON, got %s", b)
	}
	var back CityInfo
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !equalSlice(back.Tags, []string{"Bay Area", "Top 50"}) {
		t.Errorf("tags did not round-trip: got %v", back.Tags)
	}
}

// TestIndexConfigJSON_EmitsTags asserts the PVMT_CONFIG.cities payload — the
// only input to app.js's client-side tag filter — carries each city's tags, and
// omits the field for untagged cities (solvent-streets-8je6).
func TestIndexConfigJSON_EmitsTags(t *testing.T) {
	td := TemplateData{
		Cities: []CityInfo{
			{Slug: "san-jose", Name: "San Jose", Tags: []string{"Bay Area", "Top 50"}},
			{Slug: "reno", Name: "Reno"}, // untagged
		},
		UnitSystem: "metric",
	}
	var cfg struct {
		Cities []struct {
			Slug string   `json:"slug"`
			Tags []string `json:"tags"`
		} `json:"cities"`
	}
	if err := json.Unmarshal([]byte(td.IndexConfigJSON()), &cfg); err != nil {
		t.Fatalf("unmarshal IndexConfigJSON: %v", err)
	}
	if len(cfg.Cities) != 2 {
		t.Fatalf("expected 2 cities in payload, got %d", len(cfg.Cities))
	}
	if !equalSlice(cfg.Cities[0].Tags, []string{"Bay Area", "Top 50"}) {
		t.Errorf("San Jose tags = %v; want [Bay Area Top 50]", cfg.Cities[0].Tags)
	}
	// Untagged city must omit the field entirely (omitempty), so app.js's
	// (city.tags || []) guard sees an absent key, not [].
	if cfg.Cities[1].Tags != nil {
		t.Errorf("untagged Reno should have no tags key, got %v", cfg.Cities[1].Tags)
	}
}

// TestGroupCitiesByTag asserts grouping order: non-empty tags first (ascending
// by label), cities ascending by name within each, and the untagged group last.
func TestGroupCitiesByTag(t *testing.T) {
	cities := []CityInfo{
		{Name: "Zephyr"}, // untagged
		{Name: "Oakland", Tags: []string{"Bay Area"}},
		{Name: "Boulder", Tags: []string{"Colorado"}},
		{Name: "Alameda", Tags: []string{"Bay Area"}},
		{Name: "Aspen"}, // untagged
		{Name: "Denver", Tags: []string{"Colorado"}},
	}

	got := GroupCitiesByTag(cities)
	if len(got) != 3 {
		t.Fatalf("expected 3 groups (Bay Area, Colorado, untagged), got %d: %+v", len(got), got)
	}

	// Group order: Bay Area, Colorado, then "" last.
	wantTags := []string{"Bay Area", "Colorado", ""}
	for i, w := range wantTags {
		if got[i].Tag != w {
			t.Errorf("group[%d].Tag = %q; want %q", i, got[i].Tag, w)
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
	// Untagged group sorted by name: Aspen, Zephyr.
	if names := cityNames(got[2].Cities); !equalSlice(names, []string{"Aspen", "Zephyr"}) {
		t.Errorf("untagged cities = %v; want [Aspen Zephyr]", names)
	}

	// Why the index DATA_PREFIX alignment matters (bead yvlv.24): the selector's
	// first DOM <option> — the first city of the first tag group here, "Alameda"
	// — differs from the flat alphabetical first city ("Aspen") that seeds
	// CITIES[0]/DATA_PREFIX. Without the boot-time realignment the page would
	// load Aspen's data while the dropdown shows Alameda.
	flatFirst := cityNames(got[2].Cities)[0] // "Aspen"
	selectorFirst := got[0].Cities[0].Name   // "Alameda"
	if selectorFirst == flatFirst {
		t.Fatalf("expected selector-first %q to differ from flat-first %q", selectorFirst, flatFirst)
	}
}

// TestGroupCitiesByTag_MultiMembership asserts a city carrying multiple tags
// appears under each matching group (once per tag), and untagged cities do not
// leak into a tagged group.
func TestGroupCitiesByTag_MultiMembership(t *testing.T) {
	cities := []CityInfo{
		{Name: "San Jose", Tags: []string{"Bay Area", "Top 50"}},
		{Name: "Oakland", Tags: []string{"Bay Area"}},
		{Name: "Reno"}, // untagged
	}
	got := GroupCitiesByTag(cities)

	// Groups: Bay Area, Top 50, "" (untagged) — sorted with empty last.
	wantTags := []string{"Bay Area", "Top 50", ""}
	if len(got) != len(wantTags) {
		t.Fatalf("expected %d groups, got %d: %+v", len(wantTags), len(got), got)
	}
	for i, w := range wantTags {
		if got[i].Tag != w {
			t.Errorf("group[%d].Tag = %q; want %q", i, got[i].Tag, w)
		}
	}
	// San Jose appears under both Bay Area and Top 50.
	if names := cityNames(got[0].Cities); !equalSlice(names, []string{"Oakland", "San Jose"}) {
		t.Errorf("Bay Area cities = %v; want [Oakland San Jose]", names)
	}
	if names := cityNames(got[1].Cities); !equalSlice(names, []string{"San Jose"}) {
		t.Errorf("Top 50 cities = %v; want [San Jose]", names)
	}
	if names := cityNames(got[2].Cities); !equalSlice(names, []string{"Reno"}) {
		t.Errorf("untagged cities = %v; want [Reno]", names)
	}
}

// TestGroupCitiesByTag_AllUntagged asserts that when no city has a tag, a single
// untagged group is returned (rendered as bare options).
func TestGroupCitiesByTag_AllUntagged(t *testing.T) {
	cities := []CityInfo{{Name: "B"}, {Name: "A"}}
	got := GroupCitiesByTag(cities)
	if len(got) != 1 {
		t.Fatalf("expected 1 group, got %d", len(got))
	}
	if got[0].Tag != "" {
		t.Errorf("group tag = %q; want empty", got[0].Tag)
	}
	if names := cityNames(got[0].Cities); !equalSlice(names, []string{"A", "B"}) {
		t.Errorf("cities = %v; want [A B]", names)
	}
}

// TestGroupCitiesByTag_EmptyTagString pins the untagged group being keyed off
// the map rather than a flag set only in the len(Tags)==0 branch. A city
// carrying an explicit "" tag lands in the same bucket; under the flag the
// group went unrendered and the city vanished from the selector while still
// appearing in cities.json. config.validateCities rejects empty tags, so this
// guards CityInfo built outside that path.
func TestGroupCitiesByTag_EmptyTagString(t *testing.T) {
	cities := []CityInfo{
		{Name: "Oakland", Tags: []string{"Bay Area"}},
		{Name: "Ghost", Tags: []string{""}},
	}
	got := GroupCitiesByTag(cities)

	wantTags := []string{"Bay Area", ""}
	if len(got) != len(wantTags) {
		t.Fatalf("expected %d groups, got %d: %+v", len(wantTags), len(got), got)
	}
	for i, w := range wantTags {
		if got[i].Tag != w {
			t.Errorf("group[%d].Tag = %q; want %q", i, got[i].Tag, w)
		}
	}
	if names := cityNames(got[1].Cities); !equalSlice(names, []string{"Ghost"}) {
		t.Errorf("untagged cities = %v; want [Ghost]", names)
	}
}

// TestGroupCitiesByTag_Empty asserts nil in, nil out.
func TestGroupCitiesByTag_Empty(t *testing.T) {
	if got := GroupCitiesByTag(nil); got != nil {
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

// TestGroupCitiesByTag_CaseDifferingTagsDeterministic pins the byte-identical
// regen property the export pipeline preserves elsewhere (hex.go, playhex.go,
// export.go). tags is built by ranging a map, so a comparator that keys only on
// strings.ToLower leaves case-differing tags as ties, and sort.Slice is not
// stable -- group order then followed map iteration and index.html changed
// between regens of unchanged data. Case-differing tags do coexist: unionTags
// dedupes on exact equality and validateTags only rejects blanks.
//
// Run enough iterations that Go's map randomization actually shuffles the tie.
func TestGroupCitiesByTag_CaseDifferingTagsDeterministic(t *testing.T) {
	cities := []CityInfo{
		{Name: "Oakland", Tags: []string{"Bay Area"}},
		{Name: "Alameda", Tags: []string{"bay area"}},
		{Name: "Zed", Tags: []string{"Zeta"}},
	}
	want := []string{"Bay Area", "bay area", "Zeta"}
	for i := range 200 {
		got := GroupCitiesByTag(cities)
		tags := make([]string, len(got))
		for j, g := range got {
			tags[j] = g.Tag
		}
		if !equalSlice(tags, want) {
			t.Fatalf("iteration %d: group order = %v; want %v", i, tags, want)
		}
	}
}
