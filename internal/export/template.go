package export

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"sync"

	"github.com/yuin/goldmark"
)

//go:embed templates
var templatesFS embed.FS

// methodologyMarkdown is the prose source of truth for the methodology
// section. It is embedded at compile time from an in-repo file. Goldmark's
// default HTML renderer escapes raw HTML blocks (no html.WithUnsafe), so a
// stray <script> in this file becomes escaped text rather than live markup
// — but the template.HTML wrapper still bypasses Go's escaper on the rendered
// output, so do NOT reuse this pattern for markdown sourced from user input,
// config, or the network without also adding a sanitizer.
//
//go:embed docs/methodology.md
var methodologyMarkdown []byte

// methodologyHTMLOnce lazily renders the embedded methodology markdown.
// Lazy so that programs which import internal/export but never render
// methodology (pvmt serve, pvmt forecast, most tests) don't pay the parse
// cost. Returns an error instead of panicking so a malformed asset
// surfaces through the caller's render pipeline rather than killing the
// binary.
var methodologyHTMLOnce = sync.OnceValues(func() (template.HTML, error) {
	var buf bytes.Buffer
	if err := goldmark.New().Convert(methodologyMarkdown, &buf); err != nil {
		return "", fmt.Errorf("render methodology markdown: %w", err)
	}
	return template.HTML(buf.String()), nil
})

// MethodologyHTML returns the rendered methodology prose. The source lives
// in internal/export/docs/methodology.md; numeric model parameters (decay
// rates, cost tiers) deliberately do not live there — they remain in the
// forecast package and surface wherever they are actually used.
func MethodologyHTML() (template.HTML, error) { return methodologyHTMLOnce() }

// TemplateFS returns the embedded template filesystem for use by the server.
func TemplateFS() fs.ReadFileFS {
	return templatesFS
}

// AppJS returns the embedded index-page application JavaScript (templates/app.js),
// referenced by index.html as an external <script src>. Site output goes
// through Assets; this is the source for tests that read the script itself.
func AppJS() []byte { return appJS }

// GameJS returns the embedded play-page application JavaScript (templates/game.js),
// referenced by play.html as an external <script src>.
func GameJS() []byte { return gameJS }

// ParseIndexTemplate returns the parsed template tree for the index page,
// including the methodology, theme and selectors partials that index.html.tmpl
// references via {{template ...}}. Shared between the static exporter and the
// live server so they can't drift.
func ParseIndexTemplate() (*template.Template, error) {
	return ParseIndexTemplateFS(templatesFS)
}

// ParseIndexTemplateFS is the fs.FS-parametrized form of ParseIndexTemplate
// (byob-interfaces.3). Production callers use ParseIndexTemplate, which feeds
// the embedded templatesFS; tests can pass an fstest.MapFS with synthetic
// template content to exercise the parse wiring without touching disk or the
// embed.
func ParseIndexTemplateFS(source fs.FS) (*template.Template, error) {
	return parseTemplateFiles(source, "index",
		"templates/index.html.tmpl",
		"templates/methodology.html.tmpl",
		"templates/theme.html.tmpl",
		"templates/selectors.html.tmpl",
	)
}

// parseTemplateFiles parses files[0] as the page template named name and the
// rest as partials into the same tree. Both page parsers go through it so the
// shared partials (selectors.html.tmpl) are wired identically.
func parseTemplateFiles(source fs.FS, name string, files ...string) (*template.Template, error) {
	var tmpl *template.Template
	for _, file := range files {
		data, err := fs.ReadFile(source, file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		if tmpl == nil {
			tmpl, err = template.New(name).Parse(string(data))
		} else {
			_, err = tmpl.Parse(string(data))
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", file, err)
		}
	}
	return tmpl, nil
}
