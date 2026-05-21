package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/factory"
	"github.com/jcrussell/solvent-streets/pkg/cmd/root"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestE2ENetwork_Livermore drives the full pipeline against real Overpass
// + ArcGIS endpoints: all ingest -> all compute -> forecast -> export. It
// is the only test that exercises the network path; pipeline_test.go
// short-circuits ingest with fixtures.
//
// Gated by PVMT_E2E_NETWORK=1 so `make test` and CI stay hermetic. Run
// with `make e2e`. Failure on a 429/504 from Overpass is upstream
// flakiness, not a regression — re-run before investigating code.
//
// Isolation: XDG_* env vars point at t.TempDir() so the test never
// touches the user's real ~/.local/share/pvmt or ~/.cache/pvmt. Verified
// by asserting the SQLite file landed under the isolated XDG_DATA_HOME.
//
// Asserted resources are roads + parking only. `all ingest` also walks
// sidewalks, but `forEachResource` (pkg/cmd/all/all.go) warns-and-continues
// on per-resource failure, and Livermore's OSM sidewalk tagging is
// uneven, so a hard assertion on sidewalks would make this test flake
// on real-world upstream data. Sidewalks count is logged for visibility.
func TestE2ENetwork_Livermore(t *testing.T) {
	if os.Getenv("PVMT_E2E_NETWORK") != "1" {
		t.Skip("set PVMT_E2E_NETWORK=1 to run the network-fed e2e test")
	}
	if testing.Short() {
		t.Skip("network e2e: skipped in -short mode")
	}

	state := setupE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	runStages(t, ctx, state)

	rootStore, store, cityID := openIsolatedStore(t, ctx, state.xdgData)
	t.Cleanup(func() { _ = rootStore.Close() })

	t.Run("boundary saved", func(t *testing.T) { assertBoundary(t, ctx, store) })
	t.Run("features ingested", func(t *testing.T) { assertFeaturesIngested(t, ctx, store) })
	t.Run("snapshot recorded", func(t *testing.T) { assertSnapshotRecorded(t, ctx, rootStore, cityID) })
	t.Run("export files written", func(t *testing.T) { assertExportFiles(t, state.outDir) })
	t.Run("forecast.json contract", func(t *testing.T) { assertForecastJSON(t, state.outDir) })
	t.Run("--json manifest emitted", func(t *testing.T) { assertExportManifest(t, state.exportStdout) })
}

type e2eState struct {
	xdgData      string
	workdir      string
	outDir       string
	factory      *cmdutil.Factory
	ios          *iostreams.IOStreams
	stdout       *bytes.Buffer
	stderr       *bytes.Buffer
	exportStdout string // captured stdout from the export --json stage
}

// setupE2E isolates XDG dirs, copies the Livermore config into a fresh
// workdir, chdirs into it, and builds the production factory.
func setupE2E(t *testing.T) *e2eState {
	t.Helper()

	xdgData := filepath.Join(t.TempDir(), "xdg-data")
	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg-cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg-config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "xdg-state"))

	repo := repoRoot(t)
	workdir := t.TempDir()
	src := filepath.Join(repo, "examples", "livermore-ca", "pvmt.toml")
	dst := filepath.Join(workdir, "pvmt.toml")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
	t.Chdir(workdir)

	f := factory.New()
	ios, _, stdout, stderr := iostreams.Test()
	f.IOStreams = ios

	return &e2eState{
		xdgData: xdgData,
		workdir: workdir,
		outDir:  filepath.Join(workdir, "dist"),
		factory: f,
		ios:     ios,
		stdout:  stdout,
		stderr:  stderr,
	}
}

// runStages executes the four pipeline stages in order. Stops at the
// first failure via t.Fatalf so downstream assertions don't run against
// half-built state.
func runStages(t *testing.T, ctx context.Context, s *e2eState) {
	t.Helper()

	exec := func(stage string, args ...string) {
		t.Helper()
		s.stdout.Reset()
		s.stderr.Reset()
		cmd := root.NewCmdRoot(s.factory)
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetOut(s.ios.Out)
		cmd.SetErr(s.ios.ErrOut)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(ctx); err != nil {
			t.Fatalf("%s (%v): %v\nstdout:\n%s\nstderr:\n%s",
				stage, args, err, s.stdout.String(), s.stderr.String())
		}
	}

	exec("ingest", "all", "ingest")
	exec("compute", "all", "compute")
	exec("forecast", "forecast")
	exec("export", "export", "--output", s.outDir, "--clean", "--json")
	s.exportStdout = s.stdout.String()

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		_ = filepath.Walk(s.outDir, func(p string, info os.FileInfo, _ error) error {
			if info != nil && !info.IsDir() {
				t.Logf("  %s (%d bytes)", p, info.Size())
			}
			return nil
		})
	})
}

// openIsolatedStore confirms the DB landed under the isolated XDG_DATA_HOME
// (proves the t.Setenv isolation worked) and returns store handles for
// assertions.
func openIsolatedStore(t *testing.T, ctx context.Context, xdgData string) (*db.RootStore, db.Store, int64) {
	t.Helper()
	dbPath := filepath.Join(xdgData, "pvmt", "pvmt.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected isolated DB at %s, not found: %v", dbPath, err)
	}
	rootStore, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open(%s): %v", dbPath, err)
	}
	city := config.CityConfig{Name: "Livermore, CA"}
	cityID, err := rootStore.EnsureCity(ctx, city.Slug(), city.Name)
	if err != nil {
		t.Fatalf("EnsureCity: %v", err)
	}
	return rootStore, rootStore.ForCity(cityID), cityID
}

func assertBoundary(t *testing.T, ctx context.Context, store db.Store) {
	t.Helper()
	geom, err := store.GetBoundary(ctx)
	if err != nil {
		t.Fatalf("GetBoundary: %v", err)
	}
	if len(geom) == 0 {
		t.Error("boundary is empty after ingest")
	}
}

func assertFeaturesIngested(t *testing.T, ctx context.Context, store db.Store) {
	t.Helper()
	for _, rt := range []resource.Type{resource.TypeRoads, resource.TypeParking} {
		feats, err := store.ListFeatures(ctx, rt)
		if err != nil {
			t.Errorf("ListFeatures(%s): %v", rt, err)
			continue
		}
		if len(feats) == 0 {
			t.Errorf("zero %s features after ingest — upstream may have returned empty", rt)
		}
	}
	// Sidewalks are informational only; see file-level doc comment.
	if sw, err := store.ListFeatures(ctx, resource.TypeSidewalks); err == nil {
		t.Logf("sidewalks: %d features (informational; not asserted)", len(sw))
	}
}

func assertSnapshotRecorded(t *testing.T, ctx context.Context, rs *db.RootStore, cityID int64) {
	t.Helper()
	snaps, err := rs.ForCity(cityID).ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) == 0 {
		t.Fatal("no snapshots recorded after compute+forecast")
	}
	// compute writes configHash[:16] (pkg/cmd/compute/compute.go) — an empty
	// hash means the snapshot row was inserted bypassing that path, which
	// breaks reproducibility.
	if snaps[0].ConfigHash == "" {
		t.Errorf("snapshot %d has empty config_hash", snaps[0].ID)
	}
}

func assertExportFiles(t *testing.T, outDir string) {
	t.Helper()
	want := []string{
		"data/meta.json",
		"data/scenarios.json",
		"data/forecast.json",
		"index.html",
		"forecast.wasm",
		"wasm_exec.js",
	}
	for _, name := range want {
		p := filepath.Join(outDir, name)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing %s: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is zero bytes", name)
		}
	}
}

func assertForecastJSON(t *testing.T, outDir string) {
	t.Helper()
	var forecasts []map[string]any
	readJSON(t, filepath.Join(outDir, "data", "forecast.json"), &forecasts)

	if len(forecasts) == 0 {
		t.Fatal("forecast.json has no entries")
	}

	got := map[string]bool{}
	for i, fc := range forecasts {
		rt, _ := fc["resource_type"].(string)
		got[rt] = true
		assertBaselinePCIFinite(t, i, rt, fc)
	}
	for _, want := range []string{"roads", "parking"} {
		if !got[want] {
			t.Errorf("forecast.json missing resource %q; got %v", want, got)
		}
	}
}

// assertBaselinePCIFinite walks every year of fc["baseline"]["years"] and
// fails if any pci value is NaN, ±Inf, or outside [0, 100]. Catches
// numerical regressions (e.g. negative PCI from a bad decay curve, NaN
// from a divide-by-zero in cohort sizing) that a year-0-only spot check
// would miss.
func assertBaselinePCIFinite(t *testing.T, idx int, rt string, fc map[string]any) {
	t.Helper()
	baseline, _ := fc["baseline"].(map[string]any)
	years, _ := baseline["years"].([]any)
	if len(years) == 0 {
		t.Errorf("forecast[%d] (%s) has no baseline years", idx, rt)
		return
	}
	for y, raw := range years {
		yr, _ := raw.(map[string]any)
		pci, ok := yr["pci"].(float64)
		if !ok {
			t.Errorf("forecast[%d] (%s) year %d pci missing or non-numeric: %+v", idx, rt, y, yr)
			continue
		}
		if math.IsNaN(pci) || math.IsInf(pci, 0) || pci < 0 || pci > 100 {
			t.Errorf("forecast[%d] (%s) year %d pci = %v; want finite in [0, 100]", idx, rt, y, pci)
		}
	}
}

func assertExportManifest(t *testing.T, stdout string) {
	t.Helper()
	var m struct {
		OutputDir string   `json:"output_dir"`
		Shared    []string `json:"shared"`
		Total     int      `json:"total"`
		Cities    []struct {
			Slug      string   `json:"slug"`
			Name      string   `json:"name"`
			FileCount int      `json:"file_count"`
			Files     []string `json:"files"`
		} `json:"cities"`
	}
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("export --json stdout is not valid JSON: %v\nstdout:\n%s", err, stdout)
	}
	if m.Total == 0 {
		t.Errorf("manifest total=0; want >0")
	}
	if len(m.Cities) == 0 {
		t.Fatalf("manifest cities is empty: %+v", m)
	}
	if len(m.Cities[0].Files) == 0 {
		t.Errorf("manifest cities[0].files is empty: %+v", m.Cities[0])
	}
	if len(m.Shared) == 0 {
		t.Errorf("manifest shared is empty (want forecast.wasm etc.): %+v", m)
	}
}

// repoRoot walks up from this test file until it finds go.mod. Resolved
// before t.Chdir so subsequent cwd changes do not break the lookup.
//
// runtime.Caller(0) returns the source path baked in at compile time,
// so this only works for `go test ./...` and `make e2e`. If you ever
// `go test -c` this package and relocate the binary, the path won't
// resolve — but no current workflow does that.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, this, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(this)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repoRoot: no go.mod found walking up from " + this)
		}
		dir = parent
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
