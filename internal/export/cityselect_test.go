package export

import (
	"strings"
	"testing"
)

// citySelectFixture has a city in two groups (San Jose), a single-group city
// and an untagged one, so the selector has optgroups, a repeated option and
// bare options.
func citySelectFixture(activeSlug string) TemplateData {
	cities := []CityInfo{
		{Slug: "san-jose", Name: "San Jose, CA", BBox: [4]float64{-122.05, 37.1, -121.6, 37.5}, Tags: []string{"Bay Area", "Top 50"}},
		{Slug: "oakland", Name: "Oakland, CA", BBox: [4]float64{-122.4, 37.7, -122.1, 37.9}, Tags: []string{"Bay Area"}},
		{Slug: "denver", Name: "Denver, CO", BBox: [4]float64{-105.1, 39.6, -104.6, 39.9}},
	}
	return TemplateData{Cities: cities, TagDimensions: GroupCitiesByDimension(cities), ActiveSlug: activeSlug}
}

// selectedOptions lists "group label/slug" for every option CitySelect marks
// selected ("" label for a bare option).
func selectedOptions(groups []CityOptionGroup) []string {
	var out []string
	for _, g := range groups {
		for _, o := range g.Options {
			if o.Selected {
				out = append(out, g.Label+"/"+o.Slug)
			}
		}
	}
	return out
}

func TestCitySelect_SelectsExactlyOneOption(t *testing.T) {
	tests := []struct {
		name, active, want string
	}{
		{"no active city: the first option", "", "Bay Area/oakland"},
		{"an unknown active city: the first option", "nowhere", "Bay Area/oakland"},
		{"an untagged active city", "denver", "/denver"},
		{"a city in two groups: its first listing only", "san-jose", "Bay Area/san-jose"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectedOptions(citySelectFixture(tc.active).CitySelect())
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("selected = %v; want [%s]", got, tc.want)
			}
		})
	}
}

func TestCitySelect_LayoutAndBBox(t *testing.T) {
	groups := citySelectFixture("").CitySelect()
	if len(groups) != 3 {
		t.Fatalf("groups = %+v; want Bay Area, Top 50 and the untagged run", groups)
	}
	if !groups[0].Grouped || groups[0].Label != "Bay Area" || !groups[1].Grouped || groups[1].Label != "Top 50" {
		t.Errorf("tagged groups = %+v, %+v", groups[0], groups[1])
	}
	if groups[2].Grouped || len(groups[2].Options) != 1 || groups[2].Options[0].Slug != "denver" {
		t.Errorf("untagged run = %+v; want Denver as a bare option", groups[2])
	}
	if got := groups[2].Options[0].BBox; got != "-105.1,39.6,-104.6,39.9" {
		t.Errorf("bbox attribute = %q", got)
	}
	// Plain decimals at any magnitude, as JavaScript's Array.join writes them.
	if got := bboxAttr([4]float64{0.00005, -180, 12, 1e-6}); got != "0.00005,-180,12,0.000001" {
		t.Errorf("bboxAttr = %q", got)
	}
	if got := (TemplateData{}).CitySelect(); len(got) != 0 {
		t.Errorf("a page with no cities has options: %+v", got)
	}
}

// TestCitySelect_OnePartialForBothPages renders both pages over the same data
// and checks the selector markup is byte-identical.
func TestCitySelect_OnePartialForBothPages(t *testing.T) {
	td := citySelectFixture("")
	cut := func(html []byte) string {
		s := string(html)
		a := strings.Index(s, `<select id="city-select"`)
		if a < 0 {
			t.Fatalf("no city selector rendered")
		}
		return s[a : a+strings.Index(s[a:], "</select>")]
	}
	index, err := RenderPage(ParseIndexTemplate, td)
	if err != nil {
		t.Fatal(err)
	}
	play, err := RenderPage(ParseGameTemplate, td)
	if err != nil {
		t.Fatal(err)
	}
	if cut(index) != cut(play) {
		t.Errorf("city selector differs:\nindex: %s\nplay:  %s", cut(index), cut(play))
	}
	if !strings.Contains(cut(index), `<option value="oakland" data-bbox="-122.4,37.7,-122.1,37.9" selected>Oakland, CA</option>`) {
		t.Errorf("unexpected option markup: %s", cut(index))
	}
}
