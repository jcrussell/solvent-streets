package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/export/exporttest"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// testAssets is the fake asset set server tests serve, so none of them needs
// `make wasm` (the real embedded assets are exercised in integration/).
func testAssets(t *testing.T) *export.Assets {
	t.Helper()
	a, err := export.NewAssets(exporttest.AssetFiles())
	if err != nil {
		t.Fatalf("NewAssets: %v", err)
	}
	return a
}

// TestAssets_ServedAtSiteRoot pins that the server routes every site-root
// asset — the same list `pvmt export` writes — through the real handler,
// with the bytes it was given, the right Content-Type, and the same
// Cache-Control as the JSON endpoints so browsers stop re-downloading the
// immutable-per-binary assets on every page load.
func TestAssets_ServedAtSiteRoot(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	assets := testAssets(t)
	h := New(nil, "127.0.0.1", 0, ios, assets).handler()

	wantType := map[string]string{
		"pvmt.wasm":    "application/wasm",
		"wasm_exec.js": "application/javascript",
		"app.js":       "application/javascript",
		"game.js":      "application/javascript",
		"selectors.js": "application/javascript",
	}
	if len(assets.Names()) != len(wantType) {
		t.Fatalf("asset names = %v; update wantType", assets.Names())
	}
	for _, name := range assets.Names() {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/"+name, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("GET /%s = %d, want 200", name, w.Code)
			}
			if got := w.Body.String(); got != string(assets.Bytes(name)) {
				t.Errorf("body = %q; want the asset bytes %q", got, assets.Bytes(name))
			}
			if got := w.Header().Get("Content-Type"); got != wantType[name] {
				t.Errorf("Content-Type = %q, want %q", got, wantType[name])
			}
			if got := w.Header().Get("Cache-Control"); got != "public, max-age=300" {
				t.Errorf("Cache-Control = %q, want %q", got, "public, max-age=300")
			}
		})
	}
}
