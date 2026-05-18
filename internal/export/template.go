package export

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/yuin/goldmark"

	"github.com/jcrussell/solvent-streets/internal/units"
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

//go:embed wasm/forecast.wasm
var forecastWasm []byte

//go:embed wasm/wasm_exec.js
var wasmExecJS []byte

// TemplateFS returns the embedded template filesystem for use by the server.
func TemplateFS() fs.ReadFileFS {
	return templatesFS
}

// ForecastWasm returns the embedded WASM binary for the forecast simulator.
func ForecastWasm() []byte { return forecastWasm }

// WasmExecJS returns the embedded Go WASM support JavaScript.
func WasmExecJS() []byte { return wasmExecJS }

// ExampleInfo describes one example card on the landing page. Defined in
// this package so gensite and the landing-page tests reference the same
// shape; drift between the two used to be invisible until runtime.
type ExampleInfo struct {
	Slug       string
	Title      string
	CityNames  string
	CityCount  int
	HexEdgeM   int
	UnitSystem string
}

// RenderLandingPage writes index.html into outputDir using the embedded
// landing and methodology templates. Centralizing the wiring here means
// both gensite and the internal template tests exercise the same code path.
func RenderLandingPage(outputDir string, examples []ExampleInfo) (err error) {
	landingData, err := templatesFS.ReadFile("templates/landing.html.tmpl")
	if err != nil {
		return fmt.Errorf("read landing template: %w", err)
	}
	methData, err := templatesFS.ReadFile("templates/methodology.html.tmpl")
	if err != nil {
		return fmt.Errorf("read methodology template: %w", err)
	}
	themeData, err := templatesFS.ReadFile("templates/theme.html.tmpl")
	if err != nil {
		return fmt.Errorf("read theme template: %w", err)
	}

	tmpl := template.New("landing")
	if _, err := tmpl.Parse(string(landingData)); err != nil {
		return fmt.Errorf("parse landing template: %w", err)
	}
	if _, err := tmpl.Parse(string(methData)); err != nil {
		return fmt.Errorf("parse methodology template: %w", err)
	}
	if _, err := tmpl.Parse(string(themeData)); err != nil {
		return fmt.Errorf("parse theme template: %w", err)
	}

	f, err := os.Create(filepath.Join(outputDir, "index.html"))
	if err != nil {
		return fmt.Errorf("create index.html: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close index.html: %w", cerr)
		}
	}()

	methodology, err := MethodologyHTML()
	if err != nil {
		return fmt.Errorf("render methodology: %w", err)
	}
	return tmpl.Execute(f, struct {
		Examples        []ExampleInfo
		MethodologyHTML template.HTML
	}{
		Examples:        examples,
		MethodologyHTML: methodology,
	})
}

// WriteSharedWasmAssets writes the embedded WASM files to dir. Use this when
// writing a single shared copy at a site root instead of per-export copies.
func WriteSharedWasmAssets(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "forecast.wasm"), forecastWasm, 0o644); err != nil {
		return fmt.Errorf("write forecast.wasm: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wasm_exec.js"), wasmExecJS, 0o644); err != nil {
		return fmt.Errorf("write wasm_exec.js: %w", err)
	}
	return nil
}

func indexFuncMap(sys units.System) template.FuncMap {
	return template.FuncMap{
		"divf":          func(a, b float64) float64 { return a / b },
		"areaLarge":     func(sqm float64) float64 { return units.AreaLargeValue(sqm, sys) },
		"areaVeryLarge": func(sqm float64) float64 { return units.AreaVeryLargeValue(sqm, sys) },
		"areaLargeUnit": func() string {
			if sys == units.Imperial {
				return "acres"
			}
			return "ha"
		},
		"areaVeryLargeUnit": func() string {
			if sys == units.Imperial {
				return "sq mi"
			}
			return "sq km"
		},
	}
}

// ParseIndexTemplate returns the parsed template tree for the index page,
// including the methodology and theme partials that index.html.tmpl references
// via {{template ...}}. Shared between the static exporter and the live server
// so they can't drift.
func ParseIndexTemplate(sys units.System) (*template.Template, error) {
	return ParseIndexTemplateFS(templatesFS, sys)
}

// ParseIndexTemplateFS is the fs.FS-parametrized form of ParseIndexTemplate
// (byob-interfaces.3). Production callers use ParseIndexTemplate, which feeds
// the embedded templatesFS; tests can pass an fstest.MapFS with synthetic
// template content to exercise the parse + funcMap wiring without touching
// disk or the embed.
func ParseIndexTemplateFS(source fs.FS, sys units.System) (*template.Template, error) {
	files := []string{
		"templates/index.html.tmpl",
		"templates/methodology.html.tmpl",
		"templates/theme.html.tmpl",
	}
	var tmpl *template.Template
	for _, name := range files {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if tmpl == nil {
			tmpl, err = template.New("index").Funcs(indexFuncMap(sys)).Parse(string(data))
		} else {
			_, err = tmpl.Parse(string(data))
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	return tmpl, nil
}
