package export

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/jcrussell/solvent-streets/internal/fsutil"
	"github.com/jcrussell/solvent-streets/internal/hint"
)

// assetNames are the browser files every generated site carries at its root,
// in the order they are written and served. The pages reference them by
// these names, so this list is the one definition `pvmt export` writes,
// `pvmt serve` routes and `pvmt check-site` verifies.
var assetNames = []string{"pvmt.wasm", "wasm_exec.js", "app.js", "game.js", "selectors.js"}

// Assets is the set of site-root browser files: the forecast WASM, the Go
// runtime shim that loads it, and the pages' JavaScript. Production code gets
// the embedded copies from EmbeddedAssets; tests build small fakes with
// NewAssets (exporttest.AssetFiles), so no unit test needs the real 3.5 MB
// WASM. An Assets is immutable once built.
type Assets struct {
	files map[string][]byte
}

// NewAssets builds an Assets from file name to contents. It requires exactly
// the site-root names (see Names), each non-empty: a missing or empty asset
// would ship a page that cannot load.
func NewAssets(files map[string][]byte) (*Assets, error) {
	var missing, unknown []string
	for _, name := range assetNames {
		if len(files[name]) == 0 {
			missing = append(missing, name)
		}
	}
	for name := range files {
		if !slices.Contains(assetNames, name) {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	switch {
	case len(missing) > 0:
		return nil, fmt.Errorf("site assets: missing or empty %s", strings.Join(missing, ", "))
	case len(unknown) > 0:
		return nil, fmt.Errorf("site assets: unknown %s", strings.Join(unknown, ", "))
	}
	return &Assets{files: files}, nil
}

// Names returns the asset file names in write/serve order.
func (a *Assets) Names() []string { return slices.Clone(assetNames) }

// Bytes returns the named asset's contents, or nil for a name not in Names.
// The slice is shared: callers must not modify it.
func (a *Assets) Bytes(name string) []byte { return a.files[name] }

// WriteTo writes every asset into dir, each atomically.
func (a *Assets) WriteTo(dir string) error {
	for _, name := range assetNames {
		if err := fsutil.WriteFile(filepath.Join(dir, name), a.files[name], 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

// wasmFS holds the forecast WASM pair when `make wasm` has run. It embeds the
// directory, not the two files, so a build without them still compiles:
// wasm/README.md is tracked, while pvmt.wasm and wasm_exec.js are generated
// and gitignored, so `go install …@version` (which builds from the module zip
// of tracked files) gets a binary without them. EmbeddedAssets reports that
// as ErrNoWasm; only export, serve and check-site need the assets.
//
//go:embed wasm
var wasmFS embed.FS

// app.js and game.js are the index and play pages' application JavaScript,
// extracted out of index.html.tmpl / game.html.tmpl so they can be linted
// (ESLint + tsc --checkJs) as real files; selectors.js is the selector logic
// both pages load first. They are tracked, so unlike the WASM they are always
// embedded. The templates reference all of them as external <script src>.
//
//go:embed templates/app.js
var appJS []byte

//go:embed templates/game.js
var gameJS []byte

//go:embed templates/selectors.js
var selectorsJS []byte

// ErrNoWasm means this binary was built without the forecast WASM, so it
// cannot write, serve or verify a site.
var ErrNoWasm = errors.New("this pvmt binary was built without the forecast WASM")

// embeddedAssets loads once: embed.FS.ReadFile copies, and pvmt.wasm is
// ~3.5 MB.
var embeddedAssets = sync.OnceValues(func() (*Assets, error) { return loadEmbeddedAssets(wasmFS) })

// EmbeddedAssets returns the assets compiled into this binary, or an error
// wrapping ErrNoWasm, with a remediation hint, when it has no WASM.
func EmbeddedAssets() (*Assets, error) { return embeddedAssets() }

// loadEmbeddedAssets reads the WASM pair from wasm (the embed in production,
// an fstest.MapFS in tests: byob-interfaces.3) and joins it with the
// always-embedded JavaScript.
func loadEmbeddedAssets(wasm fs.FS) (*Assets, error) {
	wasmBin, werr := fs.ReadFile(wasm, "wasm/pvmt.wasm")
	execJS, jerr := fs.ReadFile(wasm, "wasm/wasm_exec.js")
	if werr != nil || jerr != nil || len(wasmBin) == 0 || len(execJS) == 0 {
		return nil, hint.With(ErrNoWasm,
			"`go install` builds from the published module, which cannot include the\n"+
				"generated WASM. Install a release (.deb or tarball), or build from a\n"+
				"checkout with `make build`. Every other pvmt command works with this binary.")
	}
	return NewAssets(map[string][]byte{
		"pvmt.wasm":    wasmBin,
		"wasm_exec.js": execJS,
		"app.js":       appJS,
		"game.js":      gameJS,
		"selectors.js": selectorsJS,
	})
}
