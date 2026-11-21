package export

import (
	"bufio"
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/jcrussell/solvent-streets/internal/build"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// methodologyRepoPath is the full methodology's path in the repository,
// read on GitHub. The dashboard does not embed it; the About tab links into
// it. methodologyFile is the same file relative to this package.
const (
	methodologyRepoPath = "docs/methodology.md"
	methodologyFile     = "../../" + methodologyRepoPath
)

// aboutMarkers are prose markers unique to the About source of truth. If
// any disappear from the rendered output, the markdown embed or the render
// pipeline has broken.
var aboutMarkers = []string{
	"OpenStreetMap",
	"2020 U.S. Census",
	"hold-steady budget",
	"Solvency Score",
	"insolvency year",
	"Full methodology",
}

// forbiddenHTML are substrings that must never appear in the rendered
// About prose. Goldmark's default renderer escapes raw HTML blocks, so a
// <script> tag in the markdown source surfaces as &lt;script&gt;. This is
// a regression test on top of that escaping, not the security boundary.
var forbiddenHTML = []string{
	"<script",
	"javascript:",
	" onerror=",
	" onclick=",
}

func TestAboutHasNoRawHTML(t *testing.T) {
	html, err := AboutHTML()
	if err != nil {
		t.Fatalf("AboutHTML: %v", err)
	}
	rendered := strings.ToLower(string(html))
	for _, s := range forbiddenHTML {
		if strings.Contains(rendered, s) {
			t.Errorf("rendered About contains forbidden substring %q", s)
		}
	}
}

// TestAboutQuotesTheModelDefaults: the About prose states the default
// starting PCI, horizon and cost tiers in words, so it must follow the
// values the forecast uses.
func TestAboutQuotesTheModelDefaults(t *testing.T) {
	want := []string{
		fmt.Sprintf("PCI %g", config.DefaultInitialPCI),
		fmt.Sprintf("next %d years", config.DefaultHorizonYears),
	}
	for _, tier := range forecast.DefaultCostTiers {
		want = append(want, fmt.Sprintf("$%g ", tier.CostPerSqM))
	}
	for _, s := range want {
		if !bytes.Contains(aboutMarkdown, []byte(s)) {
			t.Errorf("about.md does not say %q", s)
		}
	}
}

// TestMethodologyNamesTheFundingLevels: the comparison runs carry one name
// each, on the Financials charts, the Materials headline and in the
// methodology that explains them.
func TestMethodologyNamesTheFundingLevels(t *testing.T) {
	md := readMethodology(t)
	for _, s := range forecast.DefaultComparisons(1) {
		if want := "**" + s.Label + "**"; !strings.Contains(md, want) {
			t.Errorf("methodology does not name the %q run as its label does (looking for %q)", s.Name, want)
		}
	}
}

func TestDashboardAboutRenders(t *testing.T) {
	tmpl := parseDashboardTemplates(t)

	about, err := AboutHTML()
	if err != nil {
		t.Fatalf("AboutHTML: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, TemplateData{AboutHTML: about}); err != nil {
		t.Fatalf("execute index: %v", err)
	}
	out := buf.String()

	aboutIdx := strings.Index(out, `id="about-tab"`)
	if aboutIdx < 0 {
		t.Fatal("rendered dashboard missing #about-tab container")
	}
	tail := out[aboutIdx:]
	if !strings.Contains(tail, `class="about-prose"`) {
		t.Error("rendered dashboard missing about-prose wrapper inside #about-tab")
	}
	for _, s := range aboutMarkers {
		if !strings.Contains(tail, s) {
			t.Errorf("rendered dashboard missing %q inside About tab", s)
		}
	}
	// The About tab's heading ids must be unique on the dashboard, which
	// carries its own ids beside the prose.
	for _, m := range regexp.MustCompile(`<h[1-6] id="([^"]+)"`).FindAllStringSubmatch(string(about), -1) {
		if n := strings.Count(out, `id="`+m[1]+`"`); n != 1 {
			t.Errorf(`dashboard has id="%s" %d times, want 1`, m[1], n)
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

func TestRepoRef(t *testing.T) {
	for version, want := range map[string]string{
		"v0.4.1":               "v0.4.1",
		"v1.10.0":              "v1.10.0",
		"v0.4.1-532-gb5bbc142": "main",
		"v0.4.1-dirty":         "main",
		"v0.5.0-SNAPSHOT-abc":  "main",
		"0.4.1":                "main",
		"dev":                  "main",
		"":                     "main",
	} {
		if got := repoRef(version); got != want {
			t.Errorf("repoRef(%q) = %q, want %q", version, got, want)
		}
	}
}

// TestRenderAboutLinks: relative links resolve against about.md's directory
// in the repository at the given ref, and every link that leaves the page
// opens in a new tab.
func TestRenderAboutLinks(t *testing.T) {
	src := []byte("[a](../../../docs/methodology.md#color-by) [b](../../../README.md) [c](#local) [d](https://example.com/x)")
	html, err := renderAbout(src, "v1.2.3")
	if err != nil {
		t.Fatalf("renderAbout: %v", err)
	}
	for _, want := range []string{
		`<a href="https://github.com/jcrussell/solvent-streets/blob/v1.2.3/docs/methodology.md#color-by" target="_blank" rel="noopener">a</a>`,
		`<a href="https://github.com/jcrussell/solvent-streets/blob/v1.2.3/README.md" target="_blank" rel="noopener">b</a>`,
		`<a href="#local">c</a>`,
		`<a href="https://example.com/x" target="_blank" rel="noopener">d</a>`,
	} {
		if !strings.Contains(string(html), want) {
			t.Errorf("rendered links missing %s\ngot: %s", want, html)
		}
	}
}

// TestMethodologyAnchorsResolve checks every link into a methodology
// section lands on a heading GitHub generates an anchor for, so renaming a
// heading cannot silently break a link to it. It covers the About tab's
// rendered links (the dashboard's only way into the methodology) and the
// methodology's own in-document links, and checks that every relative link
// in about.md names a file in the repository.
func TestMethodologyAnchorsResolve(t *testing.T) {
	anchors := githubAnchors(t, readMethodology(t))
	if !anchors["how-spending-moves-condition"] {
		t.Errorf("methodology has no #how-spending-moves-condition anchor; anchors: %v", anchors)
	}

	about, err := AboutHTML()
	if err != nil {
		t.Fatalf("AboutHTML: %v", err)
	}
	methodologyURL := repoURL + "/blob/" + repoRef(build.Current().Version) + "/" + methodologyRepoPath
	sectionLinks := 0
	for _, m := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(string(about), -1) {
		u, err := url.Parse(m[1])
		if err != nil {
			t.Errorf("About links %q, which does not parse: %v", m[1], err)
			continue
		}
		frag := u.Fragment
		u.Fragment = ""
		if u.String() != methodologyURL || frag == "" {
			continue
		}
		sectionLinks++
		if !anchors[frag] {
			t.Errorf("About links methodology #%s, which no heading generates", frag)
		}
	}
	if sectionLinks == 0 {
		t.Fatalf("About has no links to a methodology section at %s; the check above would pass vacuously", methodologyURL)
	}

	links := regexp.MustCompile(`\]\(#([^)\s]+)\)`).FindAllStringSubmatch(readMethodology(t), -1)
	if len(links) == 0 {
		t.Fatal("methodology.md has no in-document links; the check below would pass vacuously")
	}
	for _, m := range links {
		if !anchors[m[1]] {
			t.Errorf("methodology.md links #%s, which no heading generates", m[1])
		}
	}

	for _, m := range regexp.MustCompile(`\]\(([^)\s]+)\)`).FindAllSubmatch(aboutMarkdown, -1) {
		u, err := url.Parse(string(m[1]))
		if err != nil || u.Scheme != "" || u.Path == "" {
			continue
		}
		// about.md sits in docs/ inside this test's package directory.
		if _, err := os.Stat(path.Join("docs", u.Path)); err != nil {
			t.Errorf("about.md links %s, which is not in the repository: %v", m[1], err)
		}
	}
}

func readMethodology(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(methodologyFile)
	if err != nil {
		t.Fatalf("read methodology: %v", err)
	}
	return string(data)
}

// githubAnchors returns the anchors GitHub generates for the ATX headings
// of a markdown document outside fenced code: the heading text lowercased,
// with each space turned into a hyphen and every character other than a
// letter, digit, hyphen or underscore dropped ("### Color by" -> color-by).
// A repeated heading takes -1, -2, ... after the first.
func githubAnchors(t *testing.T, md string) map[string]bool {
	t.Helper()
	anchors := map[string]bool{}
	seen := map[string]int{}
	heading := regexp.MustCompile(`^#{1,6}\s+(.+?)\s*#*\s*$`)
	fenced := false
	sc := bufio.NewScanner(strings.NewReader(md))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		m := heading.FindStringSubmatch(line)
		if fenced || m == nil {
			continue
		}
		var b strings.Builder
		for _, r := range strings.ToLower(m[1]) {
			switch {
			case r == ' ':
				b.WriteRune('-')
			case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
				b.WriteRune(r)
			}
		}
		slug := b.String()
		if n := seen[slug]; n > 0 {
			seen[slug]++
			slug = fmt.Sprintf("%s-%d", slug, n)
		} else {
			seen[slug] = 1
		}
		anchors[slug] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan methodology: %v", err)
	}
	return anchors
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
