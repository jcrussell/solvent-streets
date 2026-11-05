package export

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/jcrussell/solvent-streets/internal/export/exporttest"
	"github.com/jcrussell/solvent-streets/internal/hint"
)

// testAssets is the fake asset set every export unit test writes, so none of
// them depends on `make wasm` having run.
func testAssets(t *testing.T) *Assets {
	t.Helper()
	a, err := NewAssets(exporttest.AssetFiles())
	if err != nil {
		t.Fatalf("NewAssets(exporttest.AssetFiles()): %v", err)
	}
	return a
}

func TestNewAssets_RequiresExactlyTheSiteRootFiles(t *testing.T) {
	if _, err := NewAssets(exporttest.AssetFiles()); err != nil {
		t.Fatalf("complete set rejected: %v", err)
	}
	for _, name := range assetNames {
		t.Run("missing "+name, func(t *testing.T) {
			files := exporttest.AssetFiles()
			delete(files, name)
			if _, err := NewAssets(files); err == nil {
				t.Errorf("NewAssets accepted a set without %s", name)
			}
		})
		t.Run("empty "+name, func(t *testing.T) {
			files := exporttest.AssetFiles()
			files[name] = nil
			if _, err := NewAssets(files); err == nil {
				t.Errorf("NewAssets accepted an empty %s", name)
			}
		})
	}
	t.Run("unknown name", func(t *testing.T) {
		files := exporttest.AssetFiles()
		files["extra.js"] = []byte("x")
		if _, err := NewAssets(files); err == nil {
			t.Error("NewAssets accepted a file outside the site-root set")
		}
	})
}

// TestAssets_WriteToWritesEveryName pins that what export writes is exactly
// what Names lists — the list serve routes and check-site verifies.
func TestAssets_WriteToWritesEveryName(t *testing.T) {
	a := testAssets(t)
	dir := t.TempDir()
	if err := a.WriteTo(dir); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := a.Names()
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("WriteTo wrote %v; want %v", got, want)
	}
	for _, name := range want {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != string(a.Bytes(name)) {
			t.Errorf("%s on disk = %q (%v); want %q", name, data, err, a.Bytes(name))
		}
	}
}

// TestLoadEmbeddedAssets pins the graceful-degradation contract: a binary
// built from the module zip (`go install …@version`) has wasm/README.md but
// no generated WASM, and must report ErrNoWasm with a hint rather than fail
// to compile or ship empty assets. (That the real build embeds the WASM is
// checked in integration/.)
func TestLoadEmbeddedAssets(t *testing.T) {
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
			a, err := loadEmbeddedAssets(tc.fsys)
			if tc.ok {
				if err != nil {
					t.Fatalf("loadEmbeddedAssets: %v", err)
				}
				if string(a.Bytes("pvmt.wasm")) != "\x00asm" || string(a.Bytes("app.js")) != string(appJS) {
					t.Errorf("assets carry the wrong bytes")
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
