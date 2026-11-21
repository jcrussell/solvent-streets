package export

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/jcrussell/solvent-streets/internal/build"
)

//go:embed templates
var templatesFS embed.FS

// aboutMarkdown is the About tab's prose: a short plain-language summary
// that links to the full methodology (docs/methodology.md) on GitHub. It is
// embedded at compile time from an in-repo file. Goldmark's default HTML
// renderer escapes raw HTML blocks (no html.WithUnsafe), so a stray <script>
// in this file becomes escaped text rather than live markup — but the
// template.HTML wrapper still bypasses Go's escaper on the rendered output,
// so do NOT reuse this pattern for markdown sourced from user input, config,
// or the network without also adding a sanitizer.
//
//go:embed docs/about.md
var aboutMarkdown []byte

// repoURL is the project's GitHub repository.
const repoURL = "https://github.com/jcrussell/solvent-streets"

// aboutDir is about.md's directory in the repository: its relative links
// (methodology.md#color-by, ../../../README.md) resolve against it, as they
// do when GitHub renders the file.
const aboutDir = "internal/export/docs"

// releaseVersion matches a build of a release tag (v0.4.1). A development
// build's version (v0.4.1-532-gb5bbc142, v0.4.1-dirty, dev) does not, and
// its docs links point at main.
var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// repoRef is the git ref the dashboard's links into the repository use for
// a build of version: the release tag, so a released site links the docs it
// was built from, or main otherwise.
func repoRef(version string) string {
	if releaseVersion.MatchString(version) {
		return version
	}
	return "main"
}

// aboutHTMLOnce lazily renders the embedded About markdown against this
// binary's repoRef. Lazy so that programs which import internal/export but
// never render the About tab (pvmt forecast, most tests) don't pay the
// parse cost.
var aboutHTMLOnce = sync.OnceValues(func() (template.HTML, error) {
	return renderAbout(aboutMarkdown, repoRef(build.Current().Version))
})

// AboutHTML returns the rendered About prose. Its relative links point at
// the repository on GitHub at repoRef, and open in a new tab.
func AboutHTML() (template.HTML, error) { return aboutHTMLOnce() }

// renderAbout renders src with its relative links rewritten to the
// repository at ref (see repoLinks).
func renderAbout(src []byte, ref string) (template.HTML, error) {
	var buf bytes.Buffer
	md := goldmark.New(goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		parser.WithASTTransformers(util.Prioritized(repoLinks{ref: ref}, 100)),
	))
	if err := md.Convert(src, &buf); err != nil {
		return "", fmt.Errorf("render about markdown: %w", err)
	}
	return template.HTML(buf.String()), nil
}

// repoLinks rewrites each relative link (methodology.md#color-by) to the
// file's page on GitHub at ref, and opens every link that leaves the page in
// a new tab. In-page links (#heading) are left as they are.
type repoLinks struct{ ref string }

func (r repoLinks) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if link, ok := n.(*ast.Link); entering && ok {
			r.rewrite(link)
		}
		return ast.WalkContinue, nil
	})
}

// rewrite points one link at the repository and opens it in a new tab. A
// destination that does not parse as a URL is left alone.
func (r repoLinks) rewrite(link *ast.Link) {
	u, err := url.Parse(string(link.Destination))
	if err != nil {
		return
	}
	if u.Scheme == "" && u.Host == "" && u.Path != "" {
		dest := repoURL + "/blob/" + r.ref + "/" + path.Join(aboutDir, u.Path)
		if u.Fragment != "" {
			dest += "#" + u.EscapedFragment()
		}
		link.Destination = []byte(dest)
	} else if u.Scheme != "http" && u.Scheme != "https" {
		return
	}
	link.SetAttributeString("target", "_blank")
	link.SetAttributeString("rel", "noopener")
}

// TemplateFS returns the embedded template filesystem for use by the server.
func TemplateFS() fs.ReadFileFS {
	return templatesFS
}

// ParseIndexTemplate returns the parsed template tree for the index page,
// including the about, theme, selectors and common partials that
// index.html.tmpl references via {{template ...}}. Shared between the static exporter and the
// live server so they can't drift.
func ParseIndexTemplate() (*template.Template, error) {
	return ParseIndexTemplateFS(templatesFS)
}

// ParseIndexTemplateFS is the fs.FS-parametrized form of ParseIndexTemplate.
// Production callers use ParseIndexTemplate, which feeds
// the embedded templatesFS; tests can pass an fstest.MapFS with synthetic
// template content to exercise the parse wiring without touching disk or the
// embed.
func ParseIndexTemplateFS(source fs.FS) (*template.Template, error) {
	return parseTemplateFiles(source, "index",
		"templates/index.html.tmpl",
		"templates/about.html.tmpl",
		"templates/theme.html.tmpl",
		"templates/selectors.html.tmpl",
		"templates/common.html.tmpl",
	)
}

// parseTemplateFiles parses files[0] as the page template named name and the
// rest as partials into the same tree. Both page parsers go through it so the
// shared partials (selectors.html.tmpl, common.html.tmpl) are wired identically.
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
