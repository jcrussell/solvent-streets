package export

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/units"
)

// methodologyMarkers are prose markers unique to the methodology source of
// truth. If any disappear from the rendered output, the markdown embed or
// the render pipeline has broken.
var methodologyMarkers = []string{
	"planning-grade",
	"OpenStreetMap",
	"FHWA-RD-01-156",
	"FHWA-HIF-12-042",
	"compound annual growth",
}

// forbiddenHTML are substrings that must never appear in the rendered
// methodology. Goldmark's default renderer escapes raw HTML blocks, so a
// <script> tag in the markdown source surfaces as &lt;script&gt;. This is
// a regression test on top of that escaping, not the security boundary.
var forbiddenHTML = []string{
	"<script",
	"javascript:",
	" onerror=",
	" onclick=",
}

func TestMethodologyHasNoRawHTML(t *testing.T) {
	rendered := strings.ToLower(string(MethodologyHTML()))
	for _, s := range forbiddenHTML {
		if strings.Contains(rendered, s) {
			t.Errorf("rendered methodology contains forbidden substring %q", s)
		}
	}
}

func TestLandingMethodologyRenders(t *testing.T) {
	tmpl := parseLandingTemplates(t)

	var buf bytes.Buffer
	data := struct {
		Examples        []ExampleInfo
		MethodologyHTML template.HTML
	}{
		MethodologyHTML: MethodologyHTML(),
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute landing: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `class="methodology"`) {
		t.Errorf("landing output missing methodology wrapper div")
	}
	for _, s := range methodologyMarkers {
		if !strings.Contains(out, s) {
			t.Errorf("landing output missing %q", s)
		}
	}
}

func TestDashboardMethodologyRenders(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{MethodologyHTML: MethodologyHTML()}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	aboutIdx := strings.Index(out, `id="about-tab"`)
	if aboutIdx < 0 {
		t.Fatal("rendered dashboard missing #about-tab container")
	}
	tail := out[aboutIdx:]
	if !strings.Contains(tail, `class="methodology"`) {
		t.Error("rendered dashboard missing methodology wrapper inside #about-tab")
	}
	for _, s := range methodologyMarkers {
		if !strings.Contains(tail, s) {
			t.Errorf("rendered dashboard missing %q inside About tab", s)
		}
	}
}

func TestDashboardHasAboutTab(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	// Structural elements must appear exactly once — duplicates would
	// suggest an injection loop or a bad template partial.
	for _, s := range []string{`data-tab="about-tab"`, `id="about-tab"`} {
		if n := strings.Count(out, s); n != 1 {
			t.Errorf("dashboard About tab: %q appears %d times, want 1", s, n)
		}
	}
}

func TestRenderLandingPage(t *testing.T) {
	dir := t.TempDir()
	examples := []ExampleInfo{
		{Slug: "test-city", Title: "Test City", CityNames: "Test", CityCount: 1, HexEdgeM: 100, UnitSystem: "metric"},
	}
	if err := RenderLandingPage(dir, examples); err != nil {
		t.Fatalf("RenderLandingPage: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatalf("read generated index.html: %v", err)
	}
	out := string(body)

	if !strings.Contains(out, `href="./test-city/"`) {
		t.Error("generated landing missing example card link")
	}
	if !strings.Contains(out, `class="methodology"`) {
		t.Error("generated landing missing methodology wrapper")
	}
	for _, s := range methodologyMarkers {
		if !strings.Contains(out, s) {
			t.Errorf("generated landing missing %q", s)
		}
	}
}

func parseLandingTemplates(t *testing.T) *template.Template {
	t.Helper()
	landingData, err := templatesFS.ReadFile("templates/landing.html.tmpl")
	if err != nil {
		t.Fatalf("read landing template: %v", err)
	}
	methData, err := templatesFS.ReadFile("templates/methodology.html.tmpl")
	if err != nil {
		t.Fatalf("read methodology template: %v", err)
	}
	themeData, err := templatesFS.ReadFile("templates/theme.html.tmpl")
	if err != nil {
		t.Fatalf("read theme template: %v", err)
	}
	tmpl := template.New("landing")
	if _, err := tmpl.Parse(string(landingData)); err != nil {
		t.Fatalf("parse landing: %v", err)
	}
	if _, err := tmpl.Parse(string(methData)); err != nil {
		t.Fatalf("parse methodology: %v", err)
	}
	if _, err := tmpl.Parse(string(themeData)); err != nil {
		t.Fatalf("parse theme: %v", err)
	}
	return tmpl
}

func parseDashboardTemplates(t *testing.T) *template.Template {
	t.Helper()
	indexData, err := templatesFS.ReadFile("templates/index.html.tmpl")
	if err != nil {
		t.Fatalf("read index template: %v", err)
	}
	methData, err := templatesFS.ReadFile("templates/methodology.html.tmpl")
	if err != nil {
		t.Fatalf("read methodology template: %v", err)
	}
	themeData, err := templatesFS.ReadFile("templates/theme.html.tmpl")
	if err != nil {
		t.Fatalf("read theme template: %v", err)
	}
	tmpl, err := template.New("index").Funcs(indexFuncMap(units.Metric)).Parse(string(indexData))
	if err != nil {
		t.Fatalf("parse index: %v", err)
	}
	if _, err := tmpl.Parse(string(methData)); err != nil {
		t.Fatalf("parse methodology: %v", err)
	}
	if _, err := tmpl.Parse(string(themeData)); err != nil {
		t.Fatalf("parse theme: %v", err)
	}
	return tmpl
}
