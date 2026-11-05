package export

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/jcrussell/solvent-streets/internal/hint"
)

// TestLoadWasmPair pins the graceful-degradation contract: a binary built
// from the module zip (`go install …@version`) has wasm/README.md but no
// generated WASM, and must report ErrNoWasm with a hint rather than fail to
// compile or ship empty assets.
func TestLoadWasmPair(t *testing.T) {
	readme := &fstest.MapFile{Data: []byte("# Forecast WASM assets\n")}
	wasm := &fstest.MapFile{Data: []byte("\x00asm")}
	execJS := &fstest.MapFile{Data: []byte("// wasm_exec")}

	for _, tc := range []struct {
		name string
		fsys fstest.MapFS
		ok   bool
	}{
		{"make wasm ran", fstest.MapFS{"wasm/README.md": readme, "wasm/pvmt.wasm": wasm, "wasm/wasm_exec.js": execJS}, true},
		{"go install: README only", fstest.MapFS{"wasm/README.md": readme}, false},
		{"wasm_exec.js missing", fstest.MapFS{"wasm/README.md": readme, "wasm/pvmt.wasm": wasm}, false},
		{"pvmt.wasm empty", fstest.MapFS{"wasm/pvmt.wasm": &fstest.MapFile{}, "wasm/wasm_exec.js": execJS}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair, err := loadWasmPair(tc.fsys)
			if tc.ok {
				if err != nil {
					t.Fatalf("loadWasmPair: %v", err)
				}
				if string(pair.Wasm) != "\x00asm" || string(pair.ExecJS) != "// wasm_exec" {
					t.Errorf("pair = %q, %q; want the embedded files", pair.Wasm, pair.ExecJS)
				}
				return
			}
			if !errors.Is(err, ErrNoWasm) {
				t.Fatalf("err = %v; want ErrNoWasm", err)
			}
			var he *hint.ErrHint
			if !errors.As(err, &he) || he.Hint == "" {
				t.Errorf("err = %v; want a remediation hint", err)
			}
		})
	}
}

// TestWasmAssets_EmbeddedInThisBuild guards the other direction: the test
// binary is built after `make wasm` (Makefile, CI), so the real embed must
// carry the pair. If it does not, every export test below is exercising the
// degraded path by accident.
func TestWasmAssets_EmbeddedInThisBuild(t *testing.T) {
	if _, err := WasmAssets(); err != nil {
		t.Fatalf("WasmAssets: %v — run `make wasm` before testing", err)
	}
}
