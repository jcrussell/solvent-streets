package integration

import (
	"bytes"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/export"
)

// TestEmbeddedAssets_CarryTheRealWasm is the one test that requires
// `make wasm`. Unit tests run on export.NewAssets fakes, so without this a
// build that silently lost the forecast WASM (a dropped Makefile step, a
// renamed output path) would keep every unit test green and ship a binary
// whose export and serve refuse to run. It asserts the WebAssembly magic and
// version, not just a non-empty file, so a placeholder cannot pass.
func TestEmbeddedAssets_CarryTheRealWasm(t *testing.T) {
	assets, err := export.EmbeddedAssets()
	if err != nil {
		t.Fatalf("EmbeddedAssets: %v\nrun `make wasm` (or `make test`) before testing", err)
	}
	wasmMagic := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}
	if got := assets.Bytes("pvmt.wasm"); !bytes.HasPrefix(got, wasmMagic) {
		t.Errorf("pvmt.wasm does not start with the WebAssembly v1 header (% x); got % x", wasmMagic, got[:min(len(got), 8)])
	}
	// wasm_exec.js is the Go runtime shim; the real one defines globalThis.Go.
	if !bytes.Contains(assets.Bytes("wasm_exec.js"), []byte("globalThis.Go")) {
		t.Error("wasm_exec.js is not the Go toolchain's runtime shim")
	}
}
