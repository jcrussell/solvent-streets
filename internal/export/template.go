package export

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/yuin/goldmark"

	"github.com/jcrussell/solvent-streets/internal/hint"
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

// wasmFS holds the forecast WASM pair when `make wasm` has run. It embeds
// the directory, not the two files, so a build without them still compiles:
// wasm/README.md is tracked, while pvmt.wasm and wasm_exec.js are generated
// and gitignored, so `go install …@version` (which builds from the module zip
// of tracked files) gets a binary without them. Only export, serve and
// check-site need them; WasmAssets reports their absence as ErrNoWasm.
//
//go:embed wasm
var wasmFS embed.FS

// ErrNoWasm means this binary was built without the forecast WASM, so it
// cannot write or serve a site.
var ErrNoWasm = errors.New("this pvmt binary was built without the forecast WASM")

// WasmPair is the forecast simulator and the Go runtime shim that loads it.
type WasmPair struct {
	Wasm   []byte // pvmt.wasm
	ExecJS []byte // wasm_exec.js
}

// wasmAssets reads the pair once: embed.FS.ReadFile copies, and pvmt.wasm is
// ~3.5 MB, so serve must not re-read it per request.
var wasmAssets = sync.OnceValues(func() (WasmPair, error) { return loadWasmPair(wasmFS) })

// WasmAssets returns the embedded forecast WASM pair, or an error wrapping
// ErrNoWasm, with a remediation hint, when this binary has none.
func WasmAssets() (WasmPair, error) { return wasmAssets() }

// RequireWasm fails with WasmAssets' error when the WASM is missing. Commands
// that ship it call this first, before any slow work.
func RequireWasm() error {
	_, err := WasmAssets()
	return err
}

func loadWasmPair(fsys fs.FS) (WasmPair, error) {
	wasm, werr := fs.ReadFile(fsys, "wasm/pvmt.wasm")
	execJS, jerr := fs.ReadFile(fsys, "wasm/wasm_exec.js")
	if werr == nil && jerr == nil && len(wasm) > 0 && len(execJS) > 0 {
		return WasmPair{Wasm: wasm, ExecJS: execJS}, nil
	}
	return WasmPair{}, hint.With(ErrNoWasm,
		"`go install` builds from the published module, which cannot include the\n"+
			"generated WASM. Install a release (.deb or tarball), or build from a\n"+
			"checkout with `make build`. Every other pvmt command works with this binary.")
}

// app.js and game.js are the index and play pages' application JavaScript,
// extracted out of index.html.tmpl / game.html.tmpl so they can be linted
// (ESLint + tsc --checkJs) as real files. The templates inject their config
// via window.PVMT_CONFIG (see TemplateData.IndexConfigJSON/GameConfigJSON) and
// reference these as external <script src>. They live under templates/ (already
// covered by the //go:embed templates directive above) but get their own embed
// vars here so the accessors mirror WasmExecJS with no error to handle.
//
//go:embed templates/app.js
var appJS []byte

//go:embed templates/game.js
var gameJS []byte

// selectors.js is the selector logic both pages share (tag filters + the
// client-side city-select rebuild); it is loaded before app.js/game.js.
//
//go:embed templates/selectors.js
var selectorsJS []byte

// TemplateFS returns the embedded template filesystem for use by the server.
func TemplateFS() fs.ReadFileFS {
	return templatesFS
}

// AppJS returns the embedded index-page application JavaScript (templates/app.js),
// referenced by index.html as an external <script src>.
func AppJS() []byte { return appJS }

// GameJS returns the embedded play-page application JavaScript (templates/game.js),
// referenced by play.html as an external <script src>.
func GameJS() []byte { return gameJS }

// SelectorsJS returns the embedded shared selector JavaScript
// (templates/selectors.js), loaded by both pages before their page script.
func SelectorsJS() []byte { return selectorsJS }

// WriteSharedWasmAssets writes the embedded shared browser assets — pvmt.wasm,
// wasm_exec.js, the extracted app.js/game.js and the shared selectors.js — to dir. Use this when writing
// a single shared copy at a site root instead of per-export copies. The pages
// reference these via {{.WasmPrefix}}, so they must all land in the same dir.
func WriteSharedWasmAssets(dir string) error {
	pair, err := WasmAssets()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "pvmt.wasm"), pair.Wasm, 0o644); err != nil {
		return fmt.Errorf("write pvmt.wasm: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wasm_exec.js"), pair.ExecJS, 0o644); err != nil {
		return fmt.Errorf("write wasm_exec.js: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), appJS, 0o644); err != nil {
		return fmt.Errorf("write app.js: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "game.js"), gameJS, 0o644); err != nil {
		return fmt.Errorf("write game.js: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "selectors.js"), selectorsJS, 0o644); err != nil {
		return fmt.Errorf("write selectors.js: %w", err)
	}
	return nil
}

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
