package config

import (
	"errors"
	"testing"
)

func TestParseTag(t *testing.T) {
	cases := []struct{ tag, dim, value string }{
		{"Bay Area", DefaultTagDimension, "Bay Area"},
		{"state:CA", "state", "CA"},
		{"metro:Bay Area", "metro", "Bay Area"},
		{"x:a:b", "x", "a:b"}, // split on the FIRST colon only
	}
	for _, tc := range cases {
		dim, value := ParseTag(tc.tag)
		if dim != tc.dim || value != tc.value {
			t.Errorf("ParseTag(%q) = (%q, %q), want (%q, %q)", tc.tag, dim, value, tc.dim, tc.value)
		}
		if got := TagLabel(tc.tag); got != tc.value {
			t.Errorf("TagLabel(%q) = %q, want %q", tc.tag, got, tc.value)
		}
	}
}

// TestConfig_Validate_Tags_Dimensions pins the dimension:value shape at the
// config boundary: a lowercase identifier dimension with a non-blank value is
// accepted; an empty dimension, an empty value, or a dimension that is not a
// lowercase identifier (a legacy label containing a colon, or a case variant
// that would silently fork a dimension) is rejected with ErrInvalidConfig.
func TestConfig_Validate_Tags_Dimensions(t *testing.T) {
	accept := []string{"Bay Area", "state:CA", "metro:Bay Area", "a-b_c9:x", "x:a:b"}
	for _, tag := range accept {
		cfg := Config{Cities: []CityConfig{{Name: "Oakland, CA", Overpass: true, Tags: []string{tag}}}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() rejected valid tag %q: %v", tag, err)
		}
	}
	reject := []string{":CA", "state:", "state: ", "State:CA", "Metro: Boston", "9x:y", "a b:c"}
	for _, tag := range reject {
		cfg := Config{Cities: []CityConfig{{Name: "Oakland, CA", Overpass: true, Tags: []string{tag}}}}
		err := cfg.Validate()
		if err == nil {
			t.Errorf("Validate() accepted malformed tag %q", tag)
			continue
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("tag %q: error %v does not chain to ErrInvalidConfig", tag, err)
		}
	}
}

func TestStateFromName(t *testing.T) {
	cases := []struct {
		name  string
		state string
		ok    bool
	}{
		{"Livermore, CA", "CA", true},
		{"Washington, DC", "DC", true},
		{"St. Louis, MO ", "MO", true},
		{"Livermore", "", false},
		{"Livermore, California", "", false},
		{"Livermore,CA", "", false},
		{"Oakland, ca", "", false},
	}
	for _, tc := range cases {
		st, ok := StateFromName(tc.name)
		if st != tc.state || ok != tc.ok {
			t.Errorf("StateFromName(%q) = (%q, %v), want (%q, %v)", tc.name, st, ok, tc.state, tc.ok)
		}
	}
}

// TestCityConfig_EffectiveTags pins the derivation contract: the state tag is
// appended only when the name has a ", XX" suffix and no explicit state tag
// exists; explicit tags are never reordered or dropped; and the receiver's own
// slice is not mutated when a tag is appended.
func TestCityConfig_EffectiveTags(t *testing.T) {
	cases := []struct {
		name string
		city CityConfig
		want []string
	}{
		{"derived", CityConfig{Name: "San Jose, CA", Tags: []string{"Bay Area", "Top 50"}}, []string{"Bay Area", "Top 50", "state:CA"}},
		{"derived untagged", CityConfig{Name: "Washington, DC"}, []string{"state:DC"}},
		{"explicit wins", CityConfig{Name: "Kansas City, MO", Tags: []string{"state:KS"}}, []string{"state:KS"}},
		{"no suffix", CityConfig{Name: "Livermore", Tags: []string{"Bay Area"}}, []string{"Bay Area"}},
		{"no suffix untagged", CityConfig{Name: "Livermore"}, nil},
	}
	for _, tc := range cases {
		got := tc.city.EffectiveTags()
		if !equalStrings(got, tc.want) {
			t.Errorf("%s: EffectiveTags() = %v, want %v", tc.name, got, tc.want)
		}
	}

	explicit := []string{"Bay Area"}
	c := CityConfig{Name: "Oakland, CA", Tags: explicit[:1:1]}
	_ = c.EffectiveTags()
	if !equalStrings(explicit, []string{"Bay Area"}) || len(c.Tags) != 1 {
		t.Errorf("EffectiveTags mutated the receiver's tags: %v / %v", explicit, c.Tags)
	}
}
