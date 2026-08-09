package checksite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/export"
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
	opts := &Options{IO: ios, Dir: filepath.Join(t.TempDir(), "nope")}
	err := runCheckSite(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "make site") {
		t.Fatalf("want 'make site' error, got %v", err)
	}
}

// run executes the checker against dir and returns (combined stdout, err).
func run(t *testing.T, dir string, strict bool) (string, error) {
	t.Helper()
	ios, _, out, _ := iostreams.Test()
	opts := &Options{IO: ios, Dir: dir, Strict: strict}
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

// TestCheckSite_ZeroBaselineAreaWarns guards Gap 2: a resource whose baseline
// carries zero paved area (while pct_paved stays plausible) surfaces a WARN,
// not a FAIL — a hard failure would collide with legitimately empty resources.
func TestCheckSite_ZeroBaselineAreaWarns(t *testing.T) {
	dir := buildValidSite(t)
	// pct_paved stays plausible (unchanged), but the roads resource is zeroed.
	writeForecastArea(t, filepath.Join(dir, "demo-ca", "data", "forecast.json"),
		[]float64{85, 80, 75}, []float64{0, 5, 12}, 0)
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("zero baseline area should warn, not fail (non-strict): %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "zero paved area") {
		t.Errorf("expected a WARN about zero paved area:\n%s", out)
	}
	// --strict promotes the warning to a failure.
	if _, err := run(t, dir, true); !errors.Is(err, cmdutil.ErrSilent) {
		t.Errorf("strict should fail on the zero-area warning, got %v", err)
	}
}

func TestCheckSite_NearZeroPctPaved(t *testing.T) {
	dir := buildValidSite(t)
	writeMeta(t, filepath.Join(dir, "demo-ca", "data", "meta.json"), 0.5, 1000)
	assertFails(t, dir, "near-zero-area")
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
	addMultiCityExample(t, dir, "region-ca", "demo-ca", 999, 60.0)
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

func TestCheckSite_Strict(t *testing.T) {
	dir := buildValidSite(t)
	// pct_paved 0 produces a WARN (absent paved area); --strict turns it fatal.
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMeta(t *testing.T, path string, pctPaved, totalPaved float64) {
	t.Helper()
	m := export.MetaJSON{ProjectName: "Demo", PctPaved: pctPaved, TotalPaved: totalPaved, CityArea: 100000}
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
// letting a test drive the zero-area (per-resource geometry drop) warning.
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
func writeDataDir(t *testing.T, dataDir string, pctPaved, totalPaved float64) {
	t.Helper()
	writeMeta(t, filepath.Join(dataDir, "meta.json"), pctPaved, totalPaved)
	writeForecast(t, filepath.Join(dataDir, "forecast.json"),
		[]float64{85, 80, 75}, []float64{0, 5, 12})
	for _, name := range []string{
		"boundary.geojson", "forecast_seed.json", "hex-cost-summary.json",
		"hexgrid.geojson", "play-hexes.json", "scenarios.json",
	} {
		writeFile(t, filepath.Join(dataDir, name), "{}")
	}
}

// buildValidSite constructs a minimal but fully valid single-city site and
// returns its root.
func buildValidSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Root shared assets: copied from the embedded bytes so sha matches.
	for name, data := range export.EmbeddedSharedAssets() {
		writeFile(t, filepath.Join(dir, name), string(data))
	}
	writeFile(t, filepath.Join(dir, "index.html"), `<html><body>landing</body></html>`)
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
	writeDataDir(t, filepath.Join(exDir, "data"), 52.5, 50000)

	return dir
}

// buildValidRootSite builds the single-root layout a lone `pvmt export` emits:
// index.html, WASM, cities.json and cities/<slug>/data all at the site root
// (no per-example subdirectory). discoverSite must treat the root itself as the
// sole example, or the whole audit runs vacuously.
func buildValidRootSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	for name, data := range export.EmbeddedSharedAssets() {
		writeFile(t, filepath.Join(dir, name), string(data))
	}
	// Root index.html references WASM in the same directory (wasmPrefix empty),
	// exactly as the consolidated dashboard does.
	writeFile(t, filepath.Join(dir, "index.html"),
		`<html><head><script src="wasm_exec.js"></script>`+
			`<script>script.src = 'pvmt.wasm';</script></head><body>map</body></html>`)
	if err := os.WriteFile(filepath.Join(dir, ".nojekyll"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "cities.json"), `[{"slug":"demo","name":"Demo"}]`)
	writeDataDir(t, filepath.Join(dir, "cities", "demo", "data"), 52.5, 50000)

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
	if strings.Contains(out, "no examples found") {
		t.Errorf("single-root site should be discovered as an example, not empty:\n%s", out)
	}
	if !strings.Contains(out, "0 failed") {
		t.Errorf("summary should report 0 failed:\n%s", out)
	}
}

// TestCheckSite_RootShadowsChildren guards solvent-streets-lof3: when the site
// root classifies as an export AND child directories also look like exports, the
// audit only covers the root and silently skips the children — so it must WARN
// (and --strict must fail) rather than pass vacuously.
func TestCheckSite_RootShadowsChildren(t *testing.T) {
	dir := buildValidRootSite(t)
	// A child directory that itself looks like a single-city export.
	writeFile(t, filepath.Join(dir, "extra-ca", "index.html"), `<html><body>x</body></html>`)
	writeDataDir(t, filepath.Join(dir, "extra-ca", "data"), 52.5, 50000)

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
func addMultiCityExample(t *testing.T, dir, exSlug, citySlug string, totalPaved, pctPaved float64) {
	t.Helper()
	exDir := filepath.Join(dir, exSlug)
	writeFile(t, filepath.Join(exDir, "index.html"),
		`<html><head><script src="../wasm_exec.js"></script></head><body>map</body></html>`)
	writeFile(t, filepath.Join(exDir, "cities.json"), `[{"slug":"`+citySlug+`","name":"Demo"}]`)
	writeDataDir(t, filepath.Join(exDir, "cities", citySlug, "data"), pctPaved, totalPaved)
}
