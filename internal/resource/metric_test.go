package resource

import "testing"

func TestSubject(t *testing.T) {
	cases := []struct {
		in   Type
		want string
	}{
		{TypeRoads, "roads"},
		{TypeRoads.With(ScopeCity), "roads:city"},
		{TypeParking.With(ScopeCity), "parking:city"},
		{TypeSidewalks, "sidewalks"},
		{CombinedAll, "combined"},
		{CombinedCity, "combined:city"},
		{Type("fire_stations"), ""},
		{Type(""), ""},
	}
	for _, c := range cases {
		if got := Subject(c.in); got != c.want {
			t.Errorf("Subject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
