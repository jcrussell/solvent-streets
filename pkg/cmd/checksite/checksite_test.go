package checksite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/export/exporttest"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestNewCmdCheckSite_RunFInjection pins the test-injection seam and the
// default directory argument.
func TestNewCmdCheckSite_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	var gotDir string
	cmd := NewCmdCheckSite(f, func(_ context.Context, o *Options) error {
		gotDir = o.Dir
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotDir != "site" {
		t.Errorf("default dir = %q, want %q", gotDir, "site")
	}
}

func TestCheckSite_MissingDir(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &Options{IO: ios, Dir: filepath.Join(t.TempDir(), "nope"), Assets: testAssetsLoader(t)}
	err := runCheckSite(context.Background(), opts)
	var h *hint.ErrHint
	if err == nil || !strings.Contains(err.Error(), "not found") || !errors.As(err, &h) || !strings.Contains(h.Hint, "pvmt export") {
		t.Fatalf("want a not-found error naming pvmt export, got %v", err)
	}
}

// run executes the checker against dir and returns (combined stdout, err).
func run(t *testing.T, dir string, strict bool) (string, error) {
	t.Helper()
	ios, _, out, _ := iostreams.Test()
	opts := &Options{IO: ios, Dir: dir, Strict: strict, Assets: testAssetsLoader(t)}
	err := runCheckSite(context.Background(), opts)
	return out.String(), err
}

func TestCheckSite_ValidSite(t *testing.T) {
	dir := buildValidSite(t)
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("valid site failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("valid site produced a FAIL line:\n%s", out)
	}
	if !strings.Contains(out, "0 failed") {
		t.Errorf("summary should report 0 failed:\n%s", out)
	}
}

// TestCheckSite_CanceledContext pins finding 881e: a canceled context must
// abort the check run promptly (before finish) and return the cancellation
// error, instead of grinding through every check with ctx ignored.
func TestCheckSite_CanceledContext(t *testing.T) {
	dir := buildValidSite(t)
	ios, _, out, _ := iostreams.Test()
	opts := &Options{IO: ios, Dir: dir, Assets: testAssetsLoader(t)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runCheckSite(ctx, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	// The cancellation probe fires before the first check, so no summary line
	// ("N passed, ...") should have been printed. The bare ctx error is the
	// interrupt signal itself; exitCode maps it to 1.
	if strings.Contains(out.String(), "passed,") {
		t.Errorf("canceled run should abort before finish(); got output:\n%s", out.String())
	}
}

func TestCheckSite_MissingDataFile(t *testing.T) {
	dir := buildValidSite(t)
	os.Remove(filepath.Join(dir, "demo-ca", "data", "scenarios.json"))
	assertFails(t, dir, "scenarios.json")
}

func TestCheckSite_DanglingReference(t *testing.T) {
	dir := buildValidSite(t)
	idx := filepath.Join(dir, "demo-ca", "index.html")
	writeFile(t, idx, `<html><script src="../missing.js"></script></html>`)
	assertFails(t, dir, "missing.js")
}

func TestCheckSite_TamperedWasm(t *testing.T) {
	dir := buildValidSite(t)
	writeFile(t, filepath.Join(dir, "pvmt.wasm"), "tampered")
	assertFails(t, dir, "stale")
}

// TestCheckSite_BinaryWithoutWasm: a `go install` build has no WASM to
// compare against, so the freshness check must fail with that reason rather
// than call every asset stale.
func TestCheckSite_BinaryWithoutWasm(t *testing.T) {
	dir := buildValidSite(t)
	ios, _, out, _ := iostreams.Test()
	opts := &Options{IO: ios, Dir: dir, Assets: func() (*export.Assets, error) {
		return nil, fmt.Errorf("load: %w", export.ErrNoWasm)
	}}
	if err := runCheckSite(context.Background(), opts); !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("err = %v; want ErrSilent (a FAIL was reported)\n%s", err, out)
	}
	if !strings.Contains(out.String(), "FAIL  wasm freshness: cannot check the site's assets") {
		t.Errorf("want the no-WASM reason as the failure:\n%s", out)
	}
	if strings.Contains(out.String(), "stale") {
		t.Errorf("a missing WASM must not be reported as stale assets:\n%s", out)
	}
}

func TestCheckSite_HygieneHit(t *testing.T) {
	dir := buildValidSite(t)
	// Inject a host path into a shipped data file.
	writeFile(t, filepath.Join(dir, "demo-ca", "data", "boundary.geojson"),
		`{"path":"/home/ubuntu/secret"}`)
	assertFails(t, dir, "host path")
}

// TestScanHygiene_URLPathNotHostPath pins the /home/ and /Users/ patterns
// against URL path segments. A citation link like
// "https://www.newtonma.gov/home/showpublisheddocument/..." is NOT a host
// filesystem path and must not trip the hygiene scan, while a genuine leaked
// absolute path still must. Regression for the audit false positive that
// blocked the --strict publish gate on a legitimate Newton, MA citation URL.
func TestScanHygiene_URLPathNotHostPath(t *testing.T) {
	dir := t.TempDir()
	clean := []string{
		`#@cite https://www.newtonma.gov/home/showpublisheddocument/128379/63 (accessed)`,
		`see https://example.gov/Users/profile/123 for details`,
	}
	for i, content := range clean {
		p := filepath.Join(dir, "clean.html")
		writeFile(t, p, content)
		if hit := scanHygiene(dir, p); hit != "" {
			t.Errorf("clean[%d] should not match, got: %s (content: %s)", i, hit, content)
		}
	}
	leaks := map[string]string{
		"quoted":     `{"path":"/home/ubuntu/secret"}`,
		"start":      "/home/ubuntu/repos",
		"whitespace": "cwd = /Users/jon/project",
	}
	for name, content := range leaks {
		p := filepath.Join(dir, "leak.json")
		writeFile(t, p, content)
		if hit := scanHygiene(dir, p); hit == "" {
			t.Errorf("leak %q should match host path, got clean (content: %s)", name, content)
		}
	}
}

// TestScanHygiene_HeaderTokenAfterShortRead guards Gap 1: a hygiene-sensitive
// token sitting within the first maxHygieneRead bytes must still be detected
// after the io.ReadFull change, even when the underlying file could short-read.
// A leading run of filler exercises the "read more than one Read returns" path.
func TestScanHygiene_HeaderTokenAfterShortRead(t *testing.T) {
	dir := t.TempDir()
	// A host path a good way into the file, but still under the 4 MiB cap.
	filler := strings.Repeat("x", 1<<20) // 1 MiB, well within maxHygieneRead
	p := filepath.Join(dir, "big.json")
	writeFile(t, p, filler+` "/home/ubuntu/secret" `+"@gmail")
	hit := scanHygiene(dir, p)
	if !strings.Contains(hit, "host path") && !strings.Contains(hit, "email") {
		t.Fatalf("header token within cap should be detected, got %q", hit)
	}

	// A normal small valid file still scans clean.
	clean := filepath.Join(dir, "clean.json")
	writeFile(t, clean, `{"ok":true,"note":"nothing to see"}`)
	if hit := scanHygiene(dir, clean); hit != "" {
		t.Errorf("clean small file should not match, got %q", hit)
	}
}

// TestCheckSite_ZeroBaselineAreaWarns: a resource whose baseline carries zero
// paved area (while paved_share stays plausible) surfaces a WARN, not a FAIL —
// a hard failure would collide with legitimately empty resources — and the
// warning names the common benign cause (every feature outside the boundary)
// alongside a geometry drop, rather than blaming geometry alone.
func TestCheckSite_ZeroBaselineAreaWarns(t *testing.T) {
	dir := buildValidSite(t)
	// paved_share stays plausible (unchanged), but the roads resource is zeroed.
	writeForecastArea(t, filepath.Join(dir, "demo-ca", "data", "forecast.json"),
		[]float64{85, 80, 75}, []float64{0, 5, 12}, 0)
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("zero baseline area should warn, not fail (non-strict): %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "zero paved area") {
		t.Errorf("expected a WARN about zero paved area:\n%s", out)
	}
	if !strings.Contains(out, "outside the city boundary") || !strings.Contains(out, "geometry was dropped") {
		t.Errorf("the zero-area WARN must name both causes:\n%s", out)
	}
	if !strings.Contains(out, "pvmt --city demo-ca roads status") {
		t.Errorf("the zero-area WARN must name the read-only command for this city:\n%s", out)
	}
	// --strict promotes the warning to a failure.
	if _, err := run(t, dir, true); !errors.Is(err, cmdutil.ErrSilent) {
		t.Errorf("strict should fail on the zero-area warning, got %v", err)
	}
}

func TestCheckSite_NearZeroPavedShare(t *testing.T) {
	dir := buildValidSite(t)
	writeMeta(t, filepath.Join(dir, "demo-ca", "data", "meta.json"), 0.005, 1000)
	assertFails(t, dir, "near-zero-area")
}

// A city with a layer that was never ingested is WARNed by name, so the
// release build has a re-ingest list.
func TestCheckSite_MissingResourcesWarns(t *testing.T) {
	dir := buildValidSite(t)
	m := export.MetaJSON{Name: "Demo", PavedShare: 0.2, TotalPaved: 20000, CityArea: 100000, MissingResources: []string{"sidewalks"}}
	writeJSONFile(t, filepath.Join(dir, "demo-ca", "data", "meta.json"), m)
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("a missing layer warns, it does not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no data for sidewalks") {
		t.Errorf("no missing-layer warning:\n%s", out)
	}
}

// A missing layer every source answered empty for (OpenStreetMap maps none
// there) is not a re-ingest: no WARN names it, while a missing layer beside
// it that was never ingested still is.
func TestCheckSite_EmptyUpstreamDoesNotAskForReingest(t *testing.T) {
	dir := buildValidSite(t)
	m := export.MetaJSON{Name: "Demo", PavedShare: 0.2, TotalPaved: 20000, CityArea: 100000,
		MissingResources: []string{"parking", "sidewalks"},
		EmptySources:     map[string][]string{"sidewalks": {"overpass"}}}
	writeJSONFile(t, filepath.Join(dir, "demo-ca", "data", "meta.json"), m)
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("missing layers warn, they do not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no data for parking — re-ingest") {
		t.Errorf("no re-ingest warning for the un-ingested layer:\n%s", out)
	}
	if strings.Contains(out, "sidewalks — re-ingest") || strings.Contains(out, "parking, sidewalks") {
		t.Errorf("an empty-upstream layer is asked to be re-ingested:\n%s", out)
	}
	if !strings.Contains(out, "no sidewalks mapped upstream") {
		t.Errorf("the empty-upstream layer is not reported:\n%s", out)
	}
}

// A layer with data whose other source failed is WARNed as partial; one that
// is also missing gets only the missing warning.
func TestCheckSite_PartialResourcesWarn(t *testing.T) {
	dir := buildValidSite(t)
	m := export.MetaJSON{Name: "Demo", PavedShare: 0.2, TotalPaved: 20000, CityArea: 100000,
		MissingResources: []string{"sidewalks"},
		FailingSources:   map[string][]string{"roads": {"overpass"}, "sidewalks": {"overpass"}}}
	writeJSONFile(t, filepath.Join(dir, "demo-ca", "data", "meta.json"), m)
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("a partial layer warns, it does not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "partial roads: no overpass data") {
		t.Errorf("no partial-layer warning:\n%s", out)
	}
	if strings.Contains(out, "partial sidewalks") {
		t.Errorf("a missing layer is also warned as partial:\n%s", out)
	}
}

func TestCheckSite_NonMonotonicForecast(t *testing.T) {
	dir := buildValidSite(t)
	// A baseline whose PCI rises year over year.
	writeForecast(t, filepath.Join(dir, "demo-ca", "data", "forecast.json"),
		[]float64{80, 85}, []float64{0, 10})
	assertFails(t, dir, "PCI increased")
}

// A flat all-zero baseline is the degenerate "empty resource" state (a city
// with no area of that type) and must not FAIL the do-nothing monotonicity
// rule, which targets physically-impossible *increases*.
func TestCheckSite_FlatZeroBaselinePasses(t *testing.T) {
	dir := buildValidSite(t)
	writeForecast(t, filepath.Join(dir, "demo-ca", "data", "forecast.json"),
		[]float64{0, 0, 0}, []float64{0, 0, 0})
	if out, err := run(t, dir, false); err != nil {
		t.Fatalf("flat-zero baseline should pass: %v\n%s", err, out)
	}
}

func TestCheckSite_BacklogDecreases(t *testing.T) {
	dir := buildValidSite(t)
	writeForecast(t, filepath.Join(dir, "demo-ca", "data", "forecast.json"),
		[]float64{80, 75}, []float64{10, 5})
	assertFails(t, dir, "deferred_backlog decreased")
}

func TestCheckSite_MissingNojekyll(t *testing.T) {
	dir := buildValidSite(t)
	os.Remove(filepath.Join(dir, ".nojekyll"))
	assertFails(t, dir, ".nojekyll is missing")
}

func TestCheckSite_NonZeroNojekyll(t *testing.T) {
	dir := buildValidSite(t)
	writeFile(t, filepath.Join(dir, ".nojekyll"), "content")
	assertFails(t, dir, "expected zero-byte")
}

func TestCheckSite_CrossExampleDivergence(t *testing.T) {
	dir := buildValidSite(t)
	// Add a second example carrying the same city slug with a divergent
	// total_paved. This is a WARN, not a FAIL: a divergence can be an
	// intentional config difference (different ingest sources per example),
	// which check-site can't distinguish from the built tree alone.
	addMultiCityExample(t, dir, "region-ca", "demo-ca", 999, 0.6)
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("divergence should warn, not fail (non-strict): %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "diverges") {
		t.Errorf("expected a WARN about divergence:\n%s", out)
	}
	// --strict promotes the warning to a failure.
	if _, err := run(t, dir, true); !errors.Is(err, cmdutil.ErrSilent) {
		t.Errorf("strict should fail on the divergence warning, got %v", err)
	}
}

// TestCheckSite_SizeBudgetBreachWarns pins the SIZES check: every bucket is
// reported with its per-city share, and a per-city data file over its budget
// WARNs rather than FAILs — a heavy export is a publish decision, not a broken
// tree.
func TestCheckSite_SizeBudgetBreachWarns(t *testing.T) {
	dir := buildValidSite(t)
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("valid site should pass: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sizes: scenarios.json") {
		t.Errorf("expected a per-file size line for scenarios.json:\n%s", out)
	}

	// scenarios.json is inert to every other check (nothing parses it), so
	// padding it past its budget isolates the size warning.
	pad := strings.Repeat("x", int(sizeBudgets["scenarios.json"])+1)
	writeFile(t, filepath.Join(dir, "demo-ca", "data", "scenarios.json"), `{"pad":"`+pad+`"}`)

	out, err = run(t, dir, false)
	if err != nil {
		t.Fatalf("a size breach should warn, not fail (non-strict): %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN  sizes: scenarios.json") || !strings.Contains(out, "budget") {
		t.Errorf("expected a WARN naming scenarios.json and its budget:\n%s", out)
	}
}

// TestCheckSite_SizeBudgetIsWorstCityNotMean is the regression that the budget
// mechanism exists for. One bloated city must breach even when the tree average
// stays comfortably under, because a mean moves the wrong way: it lets an
// outlier hide behind its neighbours, and adding cities would un-trip an
// existing breach rather than surfacing it.
//
// The fixture is deliberately built so the two readings disagree. Exactly one
// of the two cities carries 1.5x the scenarios.json budget, so the worst city
// is over (WARN) while the mean across both is at 0.75x (silent). A check that
// divided the total by the city count would report nothing here.
func TestCheckSite_SizeBudgetIsWorstCityNotMean(t *testing.T) {
	dir := buildValidSite(t)

	// A second example is a second entry from dataDirsWithLabel, which is what
	// the check divides by — mirroring demo-ca so every other check still passes.
	ex2 := filepath.Join(dir, "demo2-ca")
	writeFile(t, filepath.Join(ex2, "index.html"),
		`<html><head><script src="../wasm_exec.js"></script>`+
			`<script>script.src = '..\/pvmt.wasm';</script></head><body>map</body></html>`)
	writeDataDir(t, filepath.Join(ex2, "data"), 0.525, 50000)

	budget := sizeBudgets["scenarios.json"]
	pad := strings.Repeat("x", int(budget+budget/2))
	writeFile(t, filepath.Join(dir, "demo-ca", "data", "scenarios.json"), `{"pad":"`+pad+`"}`)

	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("a size breach should warn, not fail (non-strict): %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN  sizes: scenarios.json") {
		t.Errorf("worst city is over budget but no WARN was reported:\n%s", out)
	}
	// The offending city must be named, or the report cannot be acted on.
	if !strings.Contains(out, "demo-ca") {
		t.Errorf("expected the breaching city to be named in the WARN:\n%s", out)
	}
	// Guard the fixture itself: if this ever stops holding, the test would pass
	// for the wrong reason and stop distinguishing worst-city from mean.
	if mean := (budget + budget/2) / 2; mean >= budget {
		t.Fatalf("fixture no longer separates the two readings: mean %d >= budget %d", mean, budget)
	}
}

func TestCheckSite_Strict(t *testing.T) {
	dir := buildValidSite(t)
	// paved_share 0 produces a WARN (absent paved area); --strict turns it fatal.
	writeMeta(t, filepath.Join(dir, "demo-ca", "data", "meta.json"), 0, 0)
	if out, err := run(t, dir, false); err != nil {
		t.Fatalf("non-strict should pass with a warning: %v\n%s", err, out)
	}
	out, err := run(t, dir, true)
	if !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("strict should fail on warnings, got %v\n%s", err, out)
	}
}

// assertFails runs the checker and asserts it returns ErrSilent with a FAIL
// line containing want.
func assertFails(t *testing.T, dir, want string) {
	t.Helper()
	out, err := run(t, dir, false)
	if !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("expected ErrSilent, got %v\n%s", err, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "FAIL") && strings.Contains(line, want) {
			return
		}
	}
	t.Fatalf("no FAIL line containing %q\n%s", want, out)
}

// --- fixtures ---

// testAssets is the fake asset set fixture sites carry and the checker
// compares against, so no check-site test needs `make wasm`.
func testAssets(t *testing.T) *export.Assets {
	t.Helper()
	a, err := export.NewAssets(exporttest.AssetFiles())
	if err != nil {
		t.Fatalf("NewAssets: %v", err)
	}
	return a
}

func testAssetsLoader(t *testing.T) func() (*export.Assets, error) {
	t.Helper()
	a := testAssets(t)
	return func() (*export.Assets, error) { return a, nil }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMeta(t *testing.T, path string, pavedShare, totalPaved float64) {
	t.Helper()
	m := export.MetaJSON{Name: "Demo", PavedShare: pavedShare, TotalPaved: totalPaved, CityArea: 100000}
	writeJSONFile(t, path, m)
}

// writeForecast writes a forecast.json with a single roads resource whose
// baseline has the given per-year PCI and deferred-backlog series and a
// plausible non-zero per-year paved area (so the zero-area WARN does not fire).
func writeForecast(t *testing.T, path string, pci, backlog []float64) {
	t.Helper()
	writeForecastArea(t, path, pci, backlog, 10000)
}

// writeForecastArea is writeForecast with an explicit per-year baseline area,
// letting a test drive the zero-area warning.
func writeForecastArea(t *testing.T, path string, pci, backlog []float64, area float64) {
	t.Helper()
	if len(pci) != len(backlog) {
		t.Fatal("pci/backlog length mismatch")
	}
	var years []map[string]any
	for i := range pci {
		years = append(years, map[string]any{
			"year": i + 1, "pci": pci[i], "deferred_backlog": backlog[i], "area": area,
		})
	}
	fc := []map[string]any{{
		"resource_type": "roads",
		"baseline":      map[string]any{"scenario": map[string]any{"name": "baseline"}, "years": years},
		"scenarios":     []any{},
	}}
	writeJSONFile(t, path, fc)
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeDataDir writes a full set of valid data files into dataDir.
func writeDataDir(t *testing.T, dataDir string, pavedShare, totalPaved float64) {
	t.Helper()
	writeMeta(t, filepath.Join(dataDir, "meta.json"), pavedShare, totalPaved)
	writeForecast(t, filepath.Join(dataDir, "forecast.json"),
		[]float64{85, 80, 75}, []float64{0, 5, 12})
	for _, name := range []string{
		"boundary.geojson", "forecast_seed.json", "hex-cost-summary.json",
		"scenarios.json",
	} {
		writeFile(t, filepath.Join(dataDir, name), "{}")
	}
	writeJSONFile(t, filepath.Join(dataDir, "hexgrid.geojson"), map[string]any{"v": export.HexGridFileVersion})
}

// buildValidSite constructs a minimal but fully valid single-city site and
// returns its root.
func buildValidSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Root shared assets: copied from the embedded bytes so sha matches.
	assets := testAssets(t)
	for _, name := range assets.Names() {
		writeFile(t, filepath.Join(dir, name), string(assets.Bytes(name)))
	}
	writeFile(t, filepath.Join(dir, "index.html"), `<html><body>landing</body></html>`)
	writeFile(t, filepath.Join(dir, "play.html"), `<html><body>play</body></html>`)
	if err := os.WriteFile(filepath.Join(dir, ".nojekyll"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// One single-city example with a resolvable WASM reference, plus a
	// JS-escaped script.src (the real exporter injects the loader this way)
	// to exercise the \/ unescaping in the reference resolver.
	exDir := filepath.Join(dir, "demo-ca")
	writeFile(t, filepath.Join(exDir, "index.html"),
		`<html><head><script src="../wasm_exec.js"></script>`+
			`<script>script.src = '..\/pvmt.wasm';</script></head><body>map</body></html>`)
	writeDataDir(t, filepath.Join(exDir, "data"), 0.525, 50000)

	return dir
}

// buildValidRootSite builds the single-root layout a lone `pvmt export` emits:
// index.html, WASM, cities.json and cities/<slug>/data all at the site root
// (no per-example subdirectory). discoverSite must treat the root itself as the
// sole example, or the whole audit runs vacuously.
func buildValidRootSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	assets := testAssets(t)
	for _, name := range assets.Names() {
		writeFile(t, filepath.Join(dir, name), string(assets.Bytes(name)))
	}
	// Root index.html references WASM in the same directory,
	// exactly as the consolidated dashboard does.
	writeFile(t, filepath.Join(dir, "index.html"),
		`<html><head><script src="wasm_exec.js"></script>`+
			`<script>script.src = 'pvmt.wasm';</script></head><body>map</body></html>`)
	writeFile(t, filepath.Join(dir, "play.html"), `<html><body>play</body></html>`)
	if err := os.WriteFile(filepath.Join(dir, ".nojekyll"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "cities.json"), `[{"slug":"demo","name":"Demo"}]`)
	writeFile(t, filepath.Join(dir, "compare.json"), `{"v":1,"cities":[]}`)
	writeDataDir(t, filepath.Join(dir, "cities", "demo", "data"), 0.525, 50000)

	return dir
}

// TestCheckSite_SingleRootLayout verifies the single-root `pvmt export` tree is
// discovered as one example (rooted at the site dir) and passes the full audit.
func TestCheckSite_SingleRootLayout(t *testing.T) {
	dir := buildValidRootSite(t)
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("valid single-root site failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("valid single-root site produced a FAIL line:\n%s", out)
	}
	if strings.Contains(out, "no exports found") {
		t.Errorf("single-root site should be discovered as an example, not empty:\n%s", out)
	}
	if !strings.Contains(out, "0 failed") {
		t.Errorf("summary should report 0 failed:\n%s", out)
	}
}

// TestCheckSite_RootShadowsChildren: when the site
// root classifies as an export AND child directories also look like exports, the
// audit only covers the root and silently skips the children — so it must WARN
// (and --strict must fail) rather than pass vacuously.
func TestCheckSite_RootShadowsChildren(t *testing.T) {
	dir := buildValidRootSite(t)
	// A child directory that itself looks like a single-city export.
	writeFile(t, filepath.Join(dir, "extra-ca", "index.html"), `<html><body>x</body></html>`)
	writeDataDir(t, filepath.Join(dir, "extra-ca", "data"), 0.525, 50000)

	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("shadowing should warn, not fail (non-strict): %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "extra-ca") {
		t.Errorf("expected a WARN naming the skipped child example:\n%s", out)
	}
	// --strict promotes the warning to a failure.
	if _, err := run(t, dir, true); !errors.Is(err, cmdutil.ErrSilent) {
		t.Errorf("strict should fail on the shadowing warning, got %v", err)
	}
}

// addMultiCityExample appends a multi-city example named exSlug containing one
// city citySlug, with cities.json and a full data dir using the given paved
// values. Used to create a cross-example slug collision.
func addMultiCityExample(t *testing.T, dir, exSlug, citySlug string, totalPaved, pavedShare float64) {
	t.Helper()
	exDir := filepath.Join(dir, exSlug)
	writeFile(t, filepath.Join(exDir, "index.html"),
		`<html><head><script src="../wasm_exec.js"></script></head><body>map</body></html>`)
	writeFile(t, filepath.Join(exDir, "cities.json"), `[{"slug":"`+citySlug+`","name":"Demo"}]`)
	writeFile(t, filepath.Join(exDir, "compare.json"), `{"v":1,"cities":[]}`)
	writeDataDir(t, filepath.Join(exDir, "cities", citySlug, "data"), pavedShare, totalPaved)
}

// cancelAfterNProbes is a context whose Err() reports Canceled only after the
// first n calls, so a test can land the cancellation BETWEEN checks
// deterministically. runCheckSite polls ctx.Err() and never selects on Done(),
// which is what makes this a faithful stand-in for a SIGINT arriving mid-run —
// the real path, since Main installs signal.NotifyContext and the first Ctrl-C
// cancels the context rather than killing the process.
type cancelAfterNProbes struct {
	context.Context
	n     int
	after int
}

func (c *cancelAfterNProbes) Err() error {
	c.n++
	if c.n > c.after {
		return context.Canceled
	}
	return nil
}

// TestCheckSite_InterruptedAfterFailStillReportsFailure: a run that has
// recorded a FAIL and then takes SIGINT must still go through finish() — the
// only thing that prints the tally and turns recorded FAILs into
// cmdutil.ErrSilent. Returning the bare ctx error instead would print no
// summary and let a publish gate with known failures look like a plain
// interrupt.
func TestCheckSite_InterruptedAfterFailStillReportsFailure(t *testing.T) {
	dir := buildValidSite(t)
	// checkStructure runs first and fails on a missing data file.
	if err := os.Remove(filepath.Join(dir, "demo-ca", "data", "scenarios.json")); err != nil {
		t.Fatal(err)
	}

	ios, _, out, _ := iostreams.Test()
	opts := &Options{IO: ios, Dir: dir, Assets: testAssetsLoader(t)}
	// Let the first check run (recording the FAIL), then cancel.
	ctx := &cancelAfterNProbes{Context: context.Background(), after: 1}

	err := runCheckSite(ctx, opts)

	if !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("runCheckSite = %v, want cmdutil.ErrSilent so the run exits non-zero\n%s", err, out.String())
	}
	// finish() must be REACHED, not merely survived: returning the ctx error
	// (even joined with ErrSilent) would mean the tally below never printed.
	if errors.Is(err, context.Canceled) {
		t.Errorf("returned error wraps context.Canceled; finish() was not reached")
	}
	if !strings.Contains(out.String(), "passed,") {
		t.Errorf("no summary line printed for an interrupted run with failures:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "FAIL") {
		t.Errorf("expected the recorded FAIL in the output:\n%s", out.String())
	}
	// The tally must not read like a completed audit: it names the interrupt
	// and how many checks never ran.
	// Only the first check ran, so every other check is counted as not run,
	// however many checks there are.
	m := regexp.MustCompile(`interrupted: (\d+) of (\d+) checks not run`).FindStringSubmatch(out.String())
	var skipped, total int
	if m != nil {
		skipped, _ = strconv.Atoi(m[1])
		total, _ = strconv.Atoi(m[2])
	}
	if m == nil || total < 2 || skipped != total-1 {
		t.Errorf("interrupted tally does not say how many checks were skipped:\n%s", out.String())
	}
}

// TestCheckSite_CompletedRunHasNoInterruptLine: a run that finished every
// check must not carry the interrupted note.
func TestCheckSite_CompletedRunHasNoInterruptLine(t *testing.T) {
	dir := buildValidSite(t)
	ios, _, out, _ := iostreams.Test()
	opts := &Options{IO: ios, Dir: dir, Assets: testAssetsLoader(t)}
	_ = runCheckSite(context.Background(), opts)
	if strings.Contains(out.String(), "interrupted") {
		t.Errorf("completed run mentions an interrupt:\n%s", out.String())
	}
}

// TestCheckSite_InterruptedWithNoFailuresStaysQuiet is the companion guard for
// finding 881e: with nothing recorded there is no verdict to report, so a clean
// interrupt must abort quietly rather than printing a summary for checks that
// never ran. Quiet, but not successful — the bare context.Canceled it returns
// now maps to exit 1, so `check-site && deploy` cannot publish a site whose
// remaining checks were skipped.
func TestCheckSite_InterruptedWithNoFailuresStaysQuiet(t *testing.T) {
	dir := buildValidSite(t)

	ios, _, out, _ := iostreams.Test()
	opts := &Options{IO: ios, Dir: dir, Assets: testAssetsLoader(t)}
	ctx := &cancelAfterNProbes{Context: context.Background(), after: 1}

	err := runCheckSite(ctx, opts)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runCheckSite = %v, want context.Canceled\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "passed,") {
		t.Errorf("a clean interrupt should not print a summary:\n%s", out.String())
	}
}

// TestCheckDataDir_OptionalFiles: an optional per-city file (hex-metrics.json)
// is neither a FAIL when absent nor a WARN when present.
func TestCheckDataDir_OptionalFiles(t *testing.T) {
	dir := buildValidSite(t)
	dataDir := filepath.Join(dir, "demo-ca", "data")
	ios, _, _, _ := iostreams.Test()
	r := newRunner(ios, false)
	r.checkDataDir("x", dataDir)
	if r.fail != 0 || r.warn != 0 {
		t.Errorf("valid site without the optional file: fail=%d warn=%d, want 0/0", r.fail, r.warn)
	}
	writeFile(t, filepath.Join(dataDir, "hex-metrics.json"), `{"v":1,"metrics":{}}`)
	r = newRunner(ios, false)
	r.checkDataDir("x", dataDir)
	if r.fail != 0 || r.warn != 0 {
		t.Errorf("optional file present: fail=%d warn=%d, want 0/0 (must not be reported as extra)", r.fail, r.warn)
	}
}

// check-site requires every file the exporter writes at a site root
// (export.SiteRootFiles), the /play page included.
func TestCheckSite_MissingRootFileFails(t *testing.T) {
	for _, name := range export.SiteRootFiles() {
		dir := buildValidSite(t)
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, dir, false)
		if err == nil || !strings.Contains(out, "site root is missing "+name) {
			t.Errorf("a site without %s must fail naming it; err=%v\n%s", name, err, out)
		}
	}
}

// writeCoverageMeta writes a meta.json with a population and one area per
// resource, for the coverage check.
func writeCoverageMeta(t *testing.T, path string, pop float64, areas map[string]float64) {
	t.Helper()
	m := export.MetaJSON{Name: "Demo", PavedShare: 0.2, TotalPaved: 20000, CityArea: 100000,
		Metrics: map[string]export.MetricSummaryJSON{"population": {Label: "Population", Total: &pop}}}
	for _, rt := range []string{"roads", "parking", "sidewalks"} {
		if a, ok := areas[rt]; ok {
			m.Stats = append(m.Stats, export.StatJSON{ResourceType: rt, Area: a})
		}
	}
	writeJSONFile(t, path, m)
}

// A layer far below the site's per-resident median is WARNed as a likely
// OpenStreetMap gap; one the city lacks outright is
// left to the missing-layer warning, and a small site is not judged.
func TestCheckSite_LowCoverageWarns(t *testing.T) {
	build := func(n int) string {
		dir := buildValidSite(t)
		for i := range n {
			slug := fmt.Sprintf("city-%02d", i)
			addMultiCityExample(t, dir, "region-ca", slug, 20000, 0.2)
			areas := map[string]float64{"roads": 50000, "parking": 10000, "sidewalks": 2000}
			switch slug {
			case "city-00":
				areas["parking"] = 500 // 5% of the median
			case "city-01":
				areas["sidewalks"] = 1500 // 75%: fine
			}
			writeCoverageMeta(t, filepath.Join(dir, "region-ca", "cities", slug, "data", "meta.json"), 1000, areas)
		}
		return dir
	}

	out, err := run(t, build(12), false)
	if err != nil {
		t.Fatalf("low coverage warns, it does not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "coverage: region-ca/city-00 [parking]: 0.50 m² per resident") {
		t.Errorf("no low-parking warning for city-00:\n%s", out)
	}
	if strings.Contains(out, "city-01 [sidewalks]") || strings.Contains(out, "coverage: region-ca/city-00 [roads]") {
		t.Errorf("a city near the median was warned:\n%s", out)
	}

	if out, _ := run(t, build(5), false); strings.Contains(out, "coverage: region-ca/city-00") {
		t.Errorf("a five-city site was judged against its median:\n%s", out)
	}
}

// A partial or zero layer already has its own warning with its own cause, so
// coverage leaves it alone; a malformed meta.json FAILs in the existing checks
// only.
func TestCheckSite_LowCoverageSkipsExplainedLayers(t *testing.T) {
	dir := buildValidSite(t)
	for i := range 12 {
		slug := fmt.Sprintf("city-%02d", i)
		addMultiCityExample(t, dir, "region-ca", slug, 20000, 0.2)
		areas := map[string]float64{"roads": 50000, "parking": 10000, "sidewalks": 2000}
		path := filepath.Join(dir, "region-ca", "cities", slug, "data", "meta.json")
		switch slug {
		case "city-00":
			areas["parking"] = 500
			pop := 1000.0
			m := export.MetaJSON{Name: "Demo", PavedShare: 0.2, TotalPaved: 20000, CityArea: 100000,
				Metrics:        map[string]export.MetricSummaryJSON{"population": {Total: &pop}},
				FailingSources: map[string][]string{"parking": {"overpass"}}}
			for _, rt := range []string{"roads", "parking", "sidewalks"} {
				m.Stats = append(m.Stats, export.StatJSON{ResourceType: rt, Area: areas[rt]})
			}
			writeJSONFile(t, path, m)
			continue
		case "city-01":
			areas["sidewalks"] = 0
		case "city-02":
			writeFile(t, path, "{not json")
			continue
		}
		writeCoverageMeta(t, path, 1000, areas)
	}
	out, _ := run(t, dir, false)
	if !strings.Contains(out, "partial parking") {
		t.Fatalf("setup: no partial warning for city-00:\n%s", out)
	}
	for _, unwanted := range []string{"coverage: region-ca/city-00", "coverage: region-ca/city-01", "coverage: region-ca/city-02"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("coverage judged a layer another check explains (%s):\n%s", unwanted, out)
		}
	}
}

// writeDecayFiles writes a seed whose city road cohorts blend to 0.04 and a
// hexgrid whose road hexes carry k (one city_same hex, one with its own city
// object), for the decay check.
func writeDecayFiles(t *testing.T, dataDir string, k float64) {
	t.Helper()
	writeJSONFile(t, filepath.Join(dataDir, "forecast_seed.json"), map[string]any{
		"city_cohorts": []map[string]any{
			{"resource_type": "roads", "area": 300, "decay_rate": 0.03},
			{"resource_type": "roads", "area": 100, "decay_rate": 0.07},
			{"resource_type": "parking", "area": 999, "decay_rate": 0.5},
		},
	})
	writeJSONFile(t, filepath.Join(dataDir, "hexgrid.geojson"), map[string]any{
		"type": "FeatureCollection", "v": 6,
		"features": []map[string]any{
			{"properties": map[string]any{"id": "a", "k": k, "city_same": 1, "all": map[string]any{"roads": map[string]any{"area": 50}}}},
			{"properties": map[string]any{"id": "b", "k": k, "all": map[string]any{"roads": map[string]any{"area": 90}}, "city": map[string]any{"roads": map[string]any{"area": 50}}}},
			{"properties": map[string]any{"id": "c"}},
		},
	})
}

// A map whose hex decay rates match the forecast passes; one left over from a
// different set of class rates fails and names the recompute.
func TestCheckSite_DecayDrift(t *testing.T) {
	dir := buildValidSite(t)
	data := filepath.Join(dir, "demo-ca", "data")

	writeDecayFiles(t, data, 0.0405)
	out, err := run(t, dir, false)
	if err != nil || !strings.Contains(out, "decay: demo-ca: map and forecast road decay rates agree") {
		t.Fatalf("matching decay rates did not pass: %v\n%s", err, out)
	}

	writeDecayFiles(t, data, 0.028) // the old rates, 0.7x
	out, err = run(t, dir, false)
	if !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("a 30%% decay drift did not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "30% off the forecast's (0.0400)") || !strings.Contains(out, "recompute this city") {
		t.Errorf("drift failure does not give the gap and the fix:\n%s", out)
	}
}

// A low paved share with plenty of pavement per resident is a rural city,
// not lost geometry: it WARNs. Without residents to judge by, or with almost
// no pavement per resident, it still FAILs.
func TestCheckSite_LowPavedShareRuralWarns(t *testing.T) {
	meta := func(pop float64) export.MetaJSON {
		m := export.MetaJSON{Name: "Demo", PavedShare: 0.005, TotalPaved: 50000, CityArea: 1e7}
		if pop > 0 {
			m.Metrics = map[string]export.MetricSummaryJSON{"population": {Total: &pop}}
		}
		return m
	}
	for _, tc := range []struct {
		name string
		pop  float64
		fail bool
	}{
		{"rural: 125 m² per resident", 400, false},
		{"lost geometry: 0.5 m² per resident", 100000, true},
		{"no population", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := buildValidSite(t)
			writeJSONFile(t, filepath.Join(dir, "demo-ca", "data", "meta.json"), meta(tc.pop))
			out, err := run(t, dir, false)
			if tc.fail {
				if !errors.Is(err, cmdutil.ErrSilent) || !strings.Contains(out, "near-zero-area") {
					t.Fatalf("want the near-zero-area FAIL, got %v:\n%s", err, out)
				}
				return
			}
			if err != nil || !strings.Contains(out, "125 m² per resident is plausible") {
				t.Fatalf("want a rural WARN and no failure, got %v:\n%s", err, out)
			}
		})
	}
}

// TestCheckSite_JSON: --json prints one row per check result on stdout, split
// into the check's name and its message, and the tally on stderr; a failure
// still exits non-zero.
func TestCheckSite_JSON(t *testing.T) {
	dir := buildValidSite(t)
	os.Remove(filepath.Join(dir, "demo-ca", "data", "scenarios.json"))
	ios, _, stdout, stderr := iostreams.Test()
	cmd := NewCmdCheckSite(&cmdutil.Factory{IOStreams: ios}, func(ctx context.Context, o *Options) error {
		o.Assets = testAssetsLoader(t)
		return runCheckSite(ctx, o)
	})
	cmd.SetArgs([]string{dir, "--json", "check,status,message"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("err = %v, want ErrSilent for a failing site", err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	var failed bool
	for _, r := range rows {
		if !slices.Contains(checkNames, checkName(r["check"])) || strings.HasPrefix(r["message"], r["check"]+":") {
			t.Errorf("row %v: check is not one of %v, or is repeated in the message", r, checkNames)
		}
		if r["status"] == "fail" && r["check"] == "structure" && strings.Contains(r["message"], "scenarios.json") {
			failed = true
		}
	}
	if !failed {
		t.Errorf("rows lack the structure failure for scenarios.json: %v", rows)
	}
	if !strings.Contains(stderr.String(), "1 failed") {
		t.Errorf("stderr lacks the tally: %q", stderr.String())
	}
}

// TestCheckSite_HelpNamesEveryCheck: each check's name is a --help section
// heading (STRUCTURE, WASM FRESHNESS, ...).
func TestCheckSite_HelpNamesEveryCheck(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	long := NewCmdCheckSite(&cmdutil.Factory{IOStreams: ios}, nil).Long
	for _, c := range checkNames {
		heading := "  - " + strings.ToUpper(strings.ReplaceAll(string(c), "_", " ")) + ":"
		if !strings.Contains(long, heading) {
			t.Errorf("check-site --help has no %q section", heading)
		}
	}
}

// TestCheckSite_HelpListsEverySiteRootFile: --help's STRUCTURE item names
// every file the check requires at the site root.
func TestCheckSite_HelpListsEverySiteRootFile(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	long := strings.Join(strings.Fields(NewCmdCheckSite(&cmdutil.Factory{IOStreams: ios}, nil).Long), " ")
	for _, name := range export.SiteRootFiles() {
		if !strings.Contains(long, name) {
			t.Errorf("check-site --help does not list the site-root file %s", name)
		}
	}
}

// TestCheckSite_NoExportsFound: a directory with no export in it says so in
// those words.
func TestCheckSite_NoExportsFound(t *testing.T) {
	out, err := run(t, t.TempDir(), false)
	if err == nil || !strings.Contains(out, "FAIL  structure: no exports found under ") {
		t.Errorf("empty dir: (%v)\n%s", err, out)
	}
}
