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
		`id="compare-cost-chart" role="img" aria-label="20-Year Cost Trajectory chart"`,
		`id="compare-need-chart" role="img" aria-label="Year 1 vs Year 20 Maintenance Need chart"`,
	}
	for _, s := range wantChartLabels {
		if !strings.Contains(out, s) {
			t.Errorf("missing compare-tab canvas aria-label: %q", s)
		}
	}
}

// TestCitySelectorOptgroups asserts the city selector renders <optgroup>s for
// regioned cities and bare <option>s for un-regioned ("Other") cities when
// TemplateData.CitiesByRegion is populated.
func TestCitySelectorOptgroups(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	cities := []CityInfo{
		{Slug: "oakland", Name: "Oakland", Region: "Bay Area"},
		{Slug: "denver", Name: "Denver"},
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{
		Cities:         cities,
		CitiesByRegion: GroupCitiesByRegion(cities),
	}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `<optgroup label="Bay Area">`) {
		t.Error("missing <optgroup label=\"Bay Area\"> for regioned city")
	}
	if !strings.Contains(out, `<option value="oakland"`) {
		t.Error("regioned city option missing")
	}
	// The un-regioned city must render as a bare <option> with no optgroup
	// wrapper. Assert its option exists and that no optgroup mentions it.
	if !strings.Contains(out, `<option value="denver"`) {
		t.Error("un-regioned city option missing")
	}
	// There is exactly one optgroup (Bay Area); the empty-region group is bare.
	if n := strings.Count(out, "<optgroup"); n != 1 {
		t.Errorf("expected exactly 1 optgroup, got %d", n)
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
