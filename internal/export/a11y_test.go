package export

import (
	"bytes"
	"strings"
	"testing"
)

// TestDashboardTablistAria asserts the WAI-ARIA tablist pattern is wired
// into the rendered dashboard: the bar carries role="tablist", each tab
// button carries role="tab"/aria-selected/aria-controls and a unique id,
// and each tab panel carries role="tabpanel"/aria-labelledby pointing at
// the matching button.
func TestDashboardTablistAria(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	// Cities populated so the optional Compare tab also renders and is
	// covered by the assertions below.
	if err := tmpl.Execute(&buf, TemplateData{
		Cities: []CityInfo{{Slug: "x", Name: "X"}},
	}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `class="tab-bar" role="tablist" aria-label="Sections"`) {
		t.Error("tab-bar missing role=tablist + aria-label")
	}

	tabs := []struct {
		id       string
		panelID  string
		selected string
		tabindex string
	}{
		{"map-tab-btn", "map-tab", "true", "0"},
		{"financials-tab-btn", "financials-tab", "false", "-1"},
		{"compare-tab-btn", "compare-tab", "false", "-1"},
		{"aggregate-tab-btn", "aggregate-tab", "false", "-1"},
		{"config-tab-btn", "config-tab", "false", "-1"},
		{"about-tab-btn", "about-tab", "false", "-1"},
	}
	for _, tc := range tabs {
		btn := `id="` + tc.id + `" role="tab" aria-selected="` + tc.selected + `" aria-controls="` + tc.panelID + `" tabindex="` + tc.tabindex + `"`
		if !strings.Contains(out, btn) {
			t.Errorf("tab button %q missing expected ARIA attributes (looking for %q)", tc.id, btn)
		}
		panel := `id="` + tc.panelID + `" class="tab-content`
		if !strings.Contains(out, panel) {
			t.Errorf("panel %q missing", tc.panelID)
			continue
		}
		labelledBy := `role="tabpanel" aria-labelledby="` + tc.id + `"`
		if !strings.Contains(out, labelledBy) {
			t.Errorf("panel %q missing role=tabpanel aria-labelledby=%q", tc.panelID, tc.id)
		}
	}
}

// TestDashboardReducedMotionCSS asserts the prefers-reduced-motion media
// query is present so animations are disabled for users who opted out at
// the OS level.
func TestDashboardReducedMotionCSS(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "@media (prefers-reduced-motion: reduce)") {
		t.Error("missing prefers-reduced-motion media query")
	}
	// Inside the block, at minimum, transition-duration should be capped.
	if !strings.Contains(out, "transition-duration: 0.001ms !important") {
		t.Error("prefers-reduced-motion block does not cap transition-duration")
	}
}

// TestDashboardNarrowViewportCSS asserts the narrow-viewport breakpoint
// is wired into the rendered dashboard so the legend, tabs, financial
// cards, and chart grids reflow on a 375px phone viewport rather than
// overflowing or covering the map (solvent-streets-1vk).
func TestDashboardNarrowViewportCSS(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{
		Cities: []CityInfo{{Slug: "x", Name: "X"}},
	}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "@media (max-width: 640px)") {
		t.Fatal("missing narrow-viewport media query")
	}
	// Spot-check that the grids the bead calls out (financials cards and
	// chart grids) actually collapse to a single column inside the block.
	for _, s := range []string{
		".headlines { grid-template-columns: 1fr;",
		".chart-grid { grid-template-columns: 1fr;",
		".compare-charts { grid-template-columns: 1fr;",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("narrow-viewport block does not collapse expected grid: %q", s)
		}
	}
}

// TestDashboardErrorBannerAlertRole asserts the error banner announces
// itself to assistive tech the moment it appears.
func TestDashboardErrorBannerAlertRole(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `id="error-banner"`) {
		t.Fatal("error-banner missing")
	}
	if !strings.Contains(out, `role="alert" aria-live="assertive"`) {
		t.Error("error-banner missing role=alert + aria-live=assertive")
	}
}

// TestDashboardCompareChartLabels asserts the four static <canvas>
// elements on the Compare tab carry role=img + aria-label so screen
// readers describe rather than skip them.
func TestDashboardCompareChartLabels(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{
		Cities: []CityInfo{{Slug: "x", Name: "X"}},
	}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	wantChartLabels := []string{
		`id="compare-area-chart" role="img" aria-label="Infrastructure Breakdown chart"`,
		`id="compare-mix-chart" role="img" aria-label="Road Network Composition chart"`,
		// Canvas aria-labels are static HTML (not updated by JS), so they are
		// kept horizon-agnostic; the visible <h3> titles carry the dynamic
		// "N-Year" label set in renderCompare().
		`id="compare-cost-chart" role="img" aria-label="Cost Trajectory chart"`,
		`id="compare-need-chart" role="img" aria-label="Year 1 vs final-year Maintenance Need chart"`,
	}
	for _, s := range wantChartLabels {
		if !strings.Contains(out, s) {
			t.Errorf("missing compare-tab canvas aria-label: %q", s)
		}
	}
}

// TestCitySelectorOptgroups asserts the city selector renders one <optgroup>
// per default-dimension tag (a multi-tag city appears under each) and bare
// <option>s for untagged ("Other") cities, and that the gear menu carries one
// tag filter per dimension (values listed once each, labels without the
// dimension prefix) in place of the old Compare/Aggregate-only #tag-scope.
func TestCitySelectorOptgroups(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	cities := []CityInfo{
		{Slug: "san-jose", Name: "San Jose, CA", Tags: []string{"Bay Area", "Top 50", "state:CA"}},
		{Slug: "oakland", Name: "Oakland, CA", Tags: []string{"Bay Area", "state:CA"}},
		{Slug: "denver", Name: "Denver, CO", Tags: []string{"state:CO"}},
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{
		Cities:        cities,
		TagDimensions: GroupCitiesByDimension(cities),
	}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `<optgroup label="Bay Area">`) {
		t.Error("missing <optgroup label=\"Bay Area\"> for tagged city")
	}
	if !strings.Contains(out, `<optgroup label="Top 50">`) {
		t.Error("missing <optgroup label=\"Top 50\"> for tagged city")
	}
	if !strings.Contains(out, `<option value="oakland"`) {
		t.Error("tagged city option missing")
	}
	// The city with no group tag must render as a bare <option> with no
	// optgroup wrapper; its state tag does not create a selector group.
	if !strings.Contains(out, `<option value="denver"`) {
		t.Error("untagged city option missing")
	}
	// Two optgroups (Bay Area, Top 50); state tags never become optgroups.
	if n := strings.Count(out, "<optgroup"); n != 2 {
		t.Errorf("expected exactly 2 optgroups, got %d", n)
	}
	// A multi-tag city appears once per group it belongs to: San Jose is in
	// both Bay Area and Top 50, so its option renders twice.
	if n := strings.Count(out, `<option value="san-jose"`); n != 2 {
		t.Errorf("expected San Jose option to render twice (Bay Area + Top 50), got %d", n)
	}

	// The gear menu's Cities row holds one filter per dimension: the group
	// filter lists each value once (not once per city), the state filter lists
	// its values by bare label. The legacy #tag-scope is gone.
	if strings.Contains(out, `id="tag-scope"`) {
		t.Error("legacy #tag-scope selector must not render")
	}
	if !strings.Contains(out, `id="gear-cities-row"`) {
		t.Error("missing gear-menu Cities row")
	}
	if !strings.Contains(out, `class="tag-filter" data-dim="group"`) || !strings.Contains(out, `class="tag-filter" data-dim="state"`) {
		t.Error("missing a tag-filter select for the group or state dimension")
	}
	if !strings.Contains(out, `aria-label="Filter cities by Group"`) {
		t.Error("group filter missing its aria-label")
	}
	if !strings.Contains(out, `<option value="">Any Group</option>`) {
		t.Error("group filter missing its 'Any' default option")
	}
	if n := strings.Count(out, `<option value="Bay Area">Bay Area</option>`); n != 1 {
		t.Errorf("expected one Bay Area filter option, got %d", n)
	}
	if n := strings.Count(out, `<option value="state:CA">CA</option>`); n != 1 {
		t.Errorf("expected one state:CA filter option labelled CA, got %d", n)
	}
	if strings.Contains(out, `data-dim="group" aria-label="Filter cities by Group" title="Show only cities in this Group" autocomplete="off" hidden`) {
		t.Error("the two-value group filter must not be hidden")
	}

	// The play page carries the same filters next to its own city selector.
	game, err := ParseGameTemplate()
	if err != nil {
		t.Fatalf("parse game: %v", err)
	}
	buf.Reset()
	if err := game.Execute(&buf, TemplateData{Cities: cities, TagDimensions: GroupCitiesByDimension(cities)}); err != nil {
		t.Fatalf("execute game: %v", err)
	}
	if !strings.Contains(buf.String(), `class="tag-filter" data-dim="state"`) {
		t.Error("play page missing the tag filters")
	}
}

// TestCitySelector_NoFilterRowWithoutChoice: a dimension with a single value
// cannot narrow anything, so when no dimension offers two or more values the
// Cities row is omitted entirely (HasTagFilters), and a single-value dimension
// alongside a real one renders hidden.
func TestCitySelector_NoFilterRowWithoutChoice(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	oneValue := []CityInfo{
		{Slug: "a", Name: "Alameda, CA", Tags: []string{"state:CA"}},
		{Slug: "b", Name: "Berkeley, CA", Tags: []string{"state:CA"}},
	}
	var buf bytes.Buffer
	td := TemplateData{Cities: oneValue, TagDimensions: GroupCitiesByDimension(oneValue)}
	if td.HasTagFilters() {
		t.Error("HasTagFilters() = true for a single-value dimension")
	}
	if err := tmpl.Execute(&buf, td); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(buf.String(), `id="gear-cities-row"`) {
		t.Error("Cities row rendered although no filter offers a choice")
	}

	mixed := []CityInfo{
		{Slug: "a", Name: "Alameda, CA", Tags: []string{"Bay Area", "state:CA"}},
		{Slug: "b", Name: "Boulder, CA", Tags: []string{"Front Range", "state:CA"}},
	}
	buf.Reset()
	td = TemplateData{Cities: mixed, TagDimensions: GroupCitiesByDimension(mixed)}
	if !td.HasTagFilters() {
		t.Error("HasTagFilters() = false although the group dimension has two values")
	}
	if err := tmpl.Execute(&buf, td); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `id="gear-cities-row"`) {
		t.Error("Cities row missing although the group dimension offers a choice")
	}
	if !strings.Contains(out, `data-dim="state" aria-label="Filter cities by State" title="Show only cities in this State" autocomplete="off" hidden>`) {
		t.Error("single-value state filter must render hidden")
	}
}

// TestLayerTogglesInGearMenu asserts the #toggles container now lives inside
// the #gear-menu popover (under a "Layers" row) and is no longer inside the
// stats panel. The change-handler selector "#toggles input" still resolves
// because #toggles exists; this test pins the relocation.
func TestLayerTogglesInGearMenu(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	gearStart := strings.Index(out, `id="gear-menu"`)
	gearEnd := strings.Index(out, `id="map-tab"`)
	if gearStart < 0 || gearEnd < 0 || gearEnd <= gearStart {
		t.Fatalf("could not locate gear-menu/map-tab boundaries (start=%d end=%d)", gearStart, gearEnd)
	}
	gearMarkup := out[gearStart:gearEnd]

	if !strings.Contains(gearMarkup, `id="toggles"`) {
		t.Error("#toggles is not inside the gear-menu markup")
	}
	if !strings.Contains(gearMarkup, `id="gear-toggles-row"`) {
		t.Error("gear-toggles-row missing from gear-menu")
	}

	// The stats panel (after the gear menu) must not contain #toggles anymore.
	statsMarkup := out[gearEnd:]
	if strings.Contains(statsMarkup, `id="toggles"`) {
		t.Error("#toggles still present outside the gear menu (stats panel)")
	}

	// Exactly one #toggles in the whole document (the moved one).
	if n := strings.Count(out, `id="toggles"`); n != 1 {
		t.Errorf("expected exactly one #toggles, got %d", n)
	}
}

// TestRegionSelectControl asserts the drag-to-select-region control and its
// supporting markup render and the template parses: the "Select Region"
// button, the rubber-band overlay div, and the region-result card class the
// JS injects (.region-detail) all exist.
func TestRegionSelectControl(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `id="region-select-btn"`) {
		t.Error("missing region-select button")
	}
	if !strings.Contains(out, `>Select Region<`) {
		t.Error("region-select button label missing")
	}
	if !strings.Contains(out, `id="region-rubberband"`) {
		t.Error("missing rubber-band overlay div")
	}
	// The result-card container class the JS injects must be styled/present
	// in the rendered output (CSS rule) so the panel renders correctly.
	if !strings.Contains(out, ".region-detail") {
		t.Error("missing .region-detail styling/markup for the result card")
	}
}

// TestDashboardFormLabelAssociations asserts the financials tab range
// inputs have <label for=...> associations rather than relying on visual
// proximity alone.
func TestDashboardFormLabelAssociations(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	for _, s := range []string{
		`<label for="budget-slider">`,
		`<label for="pci-slider">`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("missing form label association: %q", s)
		}
	}
}
