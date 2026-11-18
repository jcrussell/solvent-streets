package export

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"
	"testing"
)

// methodologyMarkers are prose markers unique to the methodology source of
// truth. If any disappear from the rendered output, the markdown embed or
// the render pipeline has broken.
var methodologyMarkers = []string{
	"planning-grade",
	"OpenStreetMap",
	"FHWA-RD-01-156",
	"FHWA-HIF-12-042",
	"linear annual growth",
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
	html, err := MethodologyHTML()
	if err != nil {
		t.Fatalf("MethodologyHTML: %v", err)
	}
	rendered := strings.ToLower(string(html))
	for _, s := range forbiddenHTML {
		if strings.Contains(rendered, s) {
			t.Errorf("rendered methodology contains forbidden substring %q", s)
		}
	}
}

func TestDashboardMethodologyRenders(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	methodology, err := MethodologyHTML()
	if err != nil {
		t.Fatalf("MethodologyHTML: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{MethodologyHTML: methodology}); err != nil {
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

// TestMethodologyAnchorsResolve checks every in-document link in the
// methodology lands on a heading id, so renaming a heading cannot silently
// break a link to it (solvent-streets-ern4.34). The ids must also be unique
// on the dashboard, which carries its own ids beside the prose.
func TestMethodologyAnchorsResolve(t *testing.T) {
	html, err := MethodologyHTML()
	if err != nil {
		t.Fatalf("MethodologyHTML: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`<h[1-6] id="([^"]+)"`).FindAllStringSubmatch(string(html), -1) {
		ids[m[1]] = true
	}
	if !ids["how-spending-moves-condition"] {
		t.Errorf(`rendered methodology has no id="how-spending-moves-condition"; heading ids: %v`, ids)
	}
	links := regexp.MustCompile(`\]\(#([^)\s]+)\)`).FindAllSubmatch(methodologyMarkdown, -1)
	if len(links) == 0 {
		t.Fatal("methodology.md has no in-document links; the check below would pass vacuously")
	}
	for _, m := range links {
		if !ids[string(m[1])] {
			t.Errorf("methodology.md links #%s, which no heading generates", m[1])
		}
	}

	var buf bytes.Buffer
	if err := parseDashboardTemplates(t).Execute(&buf, TemplateData{MethodologyHTML: html}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	for id := range ids {
		if n := strings.Count(buf.String(), `id="`+id+`"`); n != 1 {
			t.Errorf(`dashboard has id="%s" %d times, want 1`, id, n)
		}
	}
}

func parseDashboardTemplates(t *testing.T) *template.Template {
	t.Helper()
	// The production parser, so a partial added to ParseIndexTemplateFS is
	// exercised here too rather than silently missing from the test tree.
	tmpl, err := ParseIndexTemplate()
	if err != nil {
		t.Fatalf("parse index templates: %v", err)
	}
	return tmpl
}
