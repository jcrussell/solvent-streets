package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	exportpkg "github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func TestNewCmdExport_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdExport(f, func(_ context.Context, opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{"-o", "/tmp/site", "--clean"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts == nil {
		t.Fatal("runF was not invoked")
	}
	if gotOpts.OutputDir != "/tmp/site" {
		t.Errorf("expected OutputDir /tmp/site, got %q", gotOpts.OutputDir)
	}
	if !gotOpts.Clean {
		t.Errorf("expected --clean to be true")
	}
}

func TestNewCmdExport_Defaults(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdExport(f, func(_ context.Context, opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts.OutputDir != "dist" {
		t.Errorf("expected default OutputDir dist, got %q", gotOpts.OutputDir)
	}
	if gotOpts.Clean {
		t.Errorf("expected default --clean to be false")
	}
}

// TestOptions_Validate_EmptyOutputDir locks in byob-input-validation.5:
// an empty --output value fails at the Options boundary, not deep inside
// the exporter, and surfaces as *cmdutil.FlagError so the top-level
// runner maps it to exit code 2.
func TestOptions_Validate_EmptyOutputDir(t *testing.T) {
	opts := &Options{OutputDir: ""}
	err := opts.Validate()
	if err == nil {
		t.Fatal("expected error for empty --output, got nil")
	}
	var flag *cmdutil.FlagError
	if !errors.As(err, &flag) {
		t.Errorf("error %v is not *FlagError", err)
	}
}

// TestOptions_Validate_SensitiveLocations locks in the byob-input-validation.5
// rejection of paths that would erase user data on --clean (the filesystem
// root and the user's home directory). The cmdutil.ResolveOutputDir helper
// owns this list; this test pins the integration through Options.Validate.
func TestOptions_Validate_SensitiveLocations(t *testing.T) {
	cases := []struct {
		name string
		dir  func(t *testing.T) string
	}{
		{"root", func(*testing.T) string { return "/" }},
		{"home", func(t *testing.T) string {
			t.Helper()
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				t.Skipf("no home dir: %v", err)
			}
			return home
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{OutputDir: tc.dir(t), Clean: true}
			err := opts.Validate()
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			var flag *cmdutil.FlagError
			if !errors.As(err, &flag) {
				t.Errorf("error %v is not *FlagError", err)
			}
		})
	}
}

// TestOptions_Validate_ExistingWithoutClean covers the safety check that
// stops Export from blowing away a populated directory the user didn't
// authorize. The same condition is re-checked just before os.RemoveAll
// in runExport, but the boundary check is what makes the failure cheap
// (no DB open, no compute) and FlagError-shaped.
func TestOptions_Validate_ExistingWithoutClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	opts := &Options{OutputDir: dir, Clean: false}
	err := opts.Validate()
	if err == nil {
		t.Fatal("expected error for existing dir without --clean, got nil")
	}
	var flag *cmdutil.FlagError
	if !errors.As(err, &flag) {
		t.Errorf("error %v is not *FlagError", err)
	}
}

// TestOptions_Validate_ExistingWithClean accepts the same existing dir
// once --clean is passed. The Validate call also canonicalises OutputDir
// (abs-resolved, symlinks followed) — assert that the resolved value
// roundtrips through the field so downstream code reads the resolved
// path, not the raw flag value.
func TestOptions_Validate_ExistingWithClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	opts := &Options{OutputDir: dir, Clean: true}
	if err := opts.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(opts.OutputDir) {
		t.Errorf("OutputDir not resolved to abs: %q", opts.OutputDir)
	}
}

// TestOptions_Validate_NonExistentInWritableParent accepts the common
// case: an output dir that doesn't exist yet but whose parent is
// writable. This is the path runExport takes when there's nothing to
// remove.
func TestOptions_Validate_NonExistentInWritableParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh-site")
	opts := &Options{OutputDir: dir, Clean: false}
	if err := opts.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestNewCmdExport_JSONFlag pins that --json reaches Options. runExport
// itself is not exercised end-to-end here (it needs a DB + boundaries);
// the manifest build is covered separately in TestBuildManifest_*.
func TestNewCmdExport_JSONFlag(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdExport(f, func(_ context.Context, opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gotOpts.JSON {
		t.Errorf("expected --json to be true, got false")
	}
}

func entry(slug, name string) exportpkg.CityEntry {
	return exportpkg.CityEntry{
		Slug: slug,
		City: config.CityConfig{Name: name},
	}
}

// writeTree creates an empty file at each path under root. Intermediate
// directories are created as needed.
func writeTree(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
}

// TestBuildManifest_SingleCity exercises the single-city layout: data
// files live under data/ and attach to the lone entry; index.html and
// WASM assets land in Shared.
func TestBuildManifest_SingleCity(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir,
		"index.html",
		"forecast.wasm",
		"wasm_exec.js",
		"data/meta.json",
		"data/forecast.json",
		"data/hexgrid-bbox.geojson",
	)

	m, err := buildManifest(dir, []exportpkg.CityEntry{entry("portland-or", "Portland, OR")})
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}

	if m.OutputDir != dir {
		t.Errorf("OutputDir = %q, want %q", m.OutputDir, dir)
	}
	if m.Total != 6 {
		t.Errorf("Total = %d, want 6", m.Total)
	}
	if len(m.Cities) != 1 {
		t.Fatalf("Cities len = %d, want 1", len(m.Cities))
	}
	c := m.Cities[0]
	if c.Slug != "portland-or" || c.Name != "Portland, OR" {
		t.Errorf("city = %+v, want slug=portland-or name=Portland, OR", c)
	}
	wantFiles := []string{
		"data/forecast.json",
		"data/hexgrid-bbox.geojson",
		"data/meta.json",
	}
	if !reflect.DeepEqual(c.Files, wantFiles) {
		t.Errorf("Files = %v, want %v", c.Files, wantFiles)
	}
	if c.FileCount != 3 {
		t.Errorf("FileCount = %d, want 3", c.FileCount)
	}
	wantShared := []string{"forecast.wasm", "index.html", "wasm_exec.js"}
	if !reflect.DeepEqual(m.Shared, wantShared) {
		t.Errorf("Shared = %v, want %v", m.Shared, wantShared)
	}
}

// TestBuildManifest_MultiCity exercises the regional layout: files
// under cities/<slug>/ attach to that entry; the regional landing page,
// cities.json, and WASM assets are Shared. Each entry keeps its own
// (sorted) file list even when the walk order is interleaved.
func TestBuildManifest_MultiCity(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir,
		"index.html",
		"forecast.wasm",
		"wasm_exec.js",
		"cities.json",
		"cities/portland-or/data/meta.json",
		"cities/portland-or/data/forecast.json",
		"cities/seattle-wa/data/meta.json",
		"cities/seattle-wa/data/hexgrid-bbox.geojson",
	)

	entries := []exportpkg.CityEntry{
		entry("portland-or", "Portland, OR"),
		entry("seattle-wa", "Seattle, WA"),
	}
	m, err := buildManifest(dir, entries)
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}

	if m.Total != 8 {
		t.Errorf("Total = %d, want 8", m.Total)
	}
	if len(m.Cities) != 2 {
		t.Fatalf("Cities len = %d, want 2", len(m.Cities))
	}

	if got, want := m.Cities[0].Slug, "portland-or"; got != want {
		t.Errorf("Cities[0].Slug = %q, want %q (entry order should be preserved)", got, want)
	}
	wantPortland := []string{
		"cities/portland-or/data/forecast.json",
		"cities/portland-or/data/meta.json",
	}
	if !reflect.DeepEqual(m.Cities[0].Files, wantPortland) {
		t.Errorf("Cities[0].Files = %v, want %v", m.Cities[0].Files, wantPortland)
	}
	wantSeattle := []string{
		"cities/seattle-wa/data/hexgrid-bbox.geojson",
		"cities/seattle-wa/data/meta.json",
	}
	if !reflect.DeepEqual(m.Cities[1].Files, wantSeattle) {
		t.Errorf("Cities[1].Files = %v, want %v", m.Cities[1].Files, wantSeattle)
	}

	// Regional-only assets live in Shared, not on any city.
	wantShared := []string{"cities.json", "forecast.wasm", "index.html", "wasm_exec.js"}
	if !reflect.DeepEqual(m.Shared, wantShared) {
		t.Errorf("Shared = %v, want %v", m.Shared, wantShared)
	}
}

// TestBuildManifest_UnknownCityDirFallsThrough covers the case where a
// directory under cities/ doesn't match any configured entry slug —
// stale leftovers, manual additions. Those files must surface in Shared
// rather than silently disappear from the manifest.
func TestBuildManifest_UnknownCityDirFallsThrough(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir,
		"cities/portland-or/data/meta.json",
		"cities/leftover/data/old.json",
	)

	m, err := buildManifest(dir, []exportpkg.CityEntry{
		entry("portland-or", "Portland, OR"),
		entry("seattle-wa", "Seattle, WA"),
	})
	if err != nil {
		t.Fatalf("buildManifest: %v", err)
	}

	if m.Total != 2 {
		t.Errorf("Total = %d, want 2", m.Total)
	}
	if len(m.Cities[0].Files) != 1 {
		t.Errorf("portland Files = %v, want one entry", m.Cities[0].Files)
	}
	if len(m.Cities[1].Files) != 0 {
		t.Errorf("seattle Files = %v, want empty", m.Cities[1].Files)
	}
	wantShared := []string{"cities/leftover/data/old.json"}
	if !reflect.DeepEqual(m.Shared, wantShared) {
		t.Errorf("Shared = %v, want %v", m.Shared, wantShared)
	}
}
