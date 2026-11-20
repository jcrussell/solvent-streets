package export

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/fsutil"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/metric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// exportTestEntry builds a CityEntry whose store satisfies the exporter's
// preconditions: a boundary (so BBoxAndCenter/Info succeed) and a snapshot
// whose config hash matches the city's CityHash (so PinNewest pins it silently).
// Compute results drive the per-resource stats / forecast totals.
func exportTestEntry(cfg *config.Config, name, slug, boundary string, totals map[resource.Type]results.Totals) CityEntry {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { return boundary, nil },
		ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) {
			return []db.SnapshotCity{{ID: 1, ConfigHash: cfg.CityHash(&config.CityConfig{Name: name}), Parts: paved}}, nil
		},
		CityMetrics: totalsFixture(totals),
	}
	return CityEntry{
		Config: cfg,
		City:   config.CityConfig{Name: name},
		Store:  store,
		Slug:   slug,
	}
}

const (
	exportBoundaryA = `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`
	exportBoundaryB = `{"type":"Polygon","coordinates":[[[-121.5,37.5],[-121.4,37.5],[-121.4,37.6],[-121.5,37.6],[-121.5,37.5]]]}`
)

// TestCityEntry_InfoMapsTags: Info() carries the city's Tags into CityInfo — the
// source of tags for cities.json and the index CITIES array.
func TestCityEntry_InfoMapsTags(t *testing.T) {
	entry := exportTestEntry(&config.Config{}, "San Jose", "san-jose", exportBoundaryA, nil)
	entry.City.Tags = []string{"Bay Area", "Top 50"}
	info, err := entry.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if got := strings.Join(info.Tags, ","); got != "Bay Area,Top 50" {
		t.Errorf("Info().Tags = %v; want [Bay Area Top 50]", info.Tags)
	}
	// A ", XX" name suffix derives a state tag after the explicit ones; an
	// explicit state tag suppresses the derivation (CityConfig.EffectiveTags).
	derived := exportTestEntry(&config.Config{}, "Livermore, CA", "livermore-ca", exportBoundaryA, nil)
	derived.City.Tags = []string{"Bay Area"}
	dinfo, err := derived.Info(context.Background())
	if err != nil {
		t.Fatalf("Info (derived): %v", err)
	}
	if got := strings.Join(dinfo.Tags, ","); got != "Bay Area,state:CA" {
		t.Errorf("Info().Tags = %v; want [Bay Area state:CA]", dinfo.Tags)
	}
	explicit := exportTestEntry(&config.Config{}, "Kansas City, MO", "kansas-city-mo", exportBoundaryA, nil)
	explicit.City.Tags = []string{"state:KS"}
	einfo, err := explicit.Info(context.Background())
	if err != nil {
		t.Fatalf("Info (explicit state): %v", err)
	}
	if got := strings.Join(einfo.Tags, ","); got != "state:KS" {
		t.Errorf("Info().Tags = %v; want [state:KS]", einfo.Tags)
	}
	// An untagged city with no state suffix yields no tags.
	untagged := exportTestEntry(&config.Config{}, "Reno", "reno", exportBoundaryB, nil)
	uinfo, err := untagged.Info(context.Background())
	if err != nil {
		t.Fatalf("Info (untagged): %v", err)
	}
	if len(uinfo.Tags) != 0 {
		t.Errorf("untagged city Info().Tags = %v; want empty", uinfo.Tags)
	}
}

// TestRunSingleCity_ReusesExportedMetaAndSeed is the 7ou7.11 regression:
// runSingleCity must render index.html from the very MetaJSON + forecast seed
// that exportCityData already built and wrote, not a second from-scratch
// rebuild. We assert equivalence by reading the written data files back and
// checking they match a single fresh build — i.e. the bytes index.html embeds
// (the exportCityData return values) are exactly the on-disk data files.
func TestRunSingleCity_ReusesExportedMetaAndSeed(t *testing.T) {
	cfg := &config.Config{}
	entry := exportTestEntry(cfg, "Solo City", "solo-city", exportBoundaryA, map[resource.Type]results.Totals{
		resource.TypeRoads: {Area: 1000, Features: 10},
	})

	dir := t.TempDir()
	e := New([]CityEntry{entry}, cfg, dir, "metric", testAssets(t))
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The seed file written by exportCityData must equal a fresh BuildForecastSeed
	// — runSingleCity reuses exportCityData's value rather than recomputing, so the
	// rendered page and the written file are guaranteed to agree.
	fc := cfg.ResolvedForecast(&entry.City)
	wantSeed, err := BuildForecastSeed(context.Background(), &fc, entry.Store)
	if err != nil {
		t.Fatalf("BuildForecastSeed: %v", err)
	}
	gotSeed, err := os.ReadFile(filepath.Join(dir, "data", "forecast_seed.json"))
	if err != nil {
		t.Fatalf("read forecast_seed.json: %v", err)
	}
	if string(gotSeed) != string(wantSeed) {
		t.Errorf("forecast_seed.json = %s; want %s", gotSeed, wantSeed)
	}

	// Likewise the meta written to disk must equal a fresh BuildMeta — the same
	// value runSingleCity feeds renderHTML.
	wantMeta, err := BuildMeta(context.Background(), entry, 0)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	wantMetaBytes, err := json.Marshal(wantMeta)
	if err != nil {
		t.Fatalf("marshal want meta: %v", err)
	}
	gotMeta, err := os.ReadFile(filepath.Join(dir, "data", "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if string(gotMeta) != string(wantMetaBytes) {
		t.Errorf("meta.json = %s; want %s", gotMeta, wantMetaBytes)
	}

	// index.html was rendered from the reused meta (meta.json name comes straight
	// from MetaJSON). Its presence confirms renderHTML ran on the export's meta
	// rather than failing or being skipped.
	indexHTML, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(indexHTML), wantMeta.ProjectName) {
		t.Errorf("index.html does not contain the reused project name %q", wantMeta.ProjectName)
	}
}

// TestRunMultiCity_FailureLeavesRecoverableDir pins yvlv.19: when the export
// fails partway (here, a second city whose snapshot holds no pavement, so
// RequirePavement fails), the output dir is left non-empty and
// WITHOUT index.html — but WITH the ExportMarkerName sentinel written up front,
// so a retry with --clean can recover it instead of dead-ending on a manual
// rm -rf.
func TestRunMultiCity_FailureLeavesRecoverableDir(t *testing.T) {
	cfg := &config.Config{}
	good := exportTestEntry(cfg, "Good", "good", exportBoundaryA, map[resource.Type]results.Totals{
		resource.TypeRoads: {Area: 1000, Features: 10},
	})
	// A city whose snapshot holds only fire coverage: RequirePavement aborts
	// the run after the marker (and possibly the good city's data) is written.
	fireOnly := []db.SnapshotCity{{ID: 2, ConfigHash: cfg.CityHash(&config.CityConfig{Name: "Bad"}), Parts: []db.SnapshotPart{{Part: "service:fire", RunID: 2}}}}
	badStore := &dbtest.MockStore{
		GetBoundaryFunc:   func(context.Context) (string, error) { return exportBoundaryB, nil },
		ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) { return fireOnly, nil },
		ViewSnapshotFunc:  func(context.Context) (*db.SnapshotCity, error) { return &fireOnly[0], nil },
	}
	bad := CityEntry{Config: cfg, City: config.CityConfig{Name: "Bad"}, Store: badStore, Slug: "bad"}

	dir := t.TempDir()
	err := New([]CityEntry{good, bad}, cfg, dir, "metric", testAssets(t)).Run(context.Background())
	if err == nil {
		t.Fatal("expected export to fail on the city with no pavement")
	}

	// index.html is NOT present (it's written last, after the failing city).
	if _, statErr := os.Stat(filepath.Join(dir, "index.html")); !os.IsNotExist(statErr) {
		t.Errorf("index.html should be absent after a mid-run failure (stat err=%v)", statErr)
	}
	// The early marker IS present, so the partial dir is recoverable.
	if _, statErr := os.Stat(filepath.Join(dir, fsutil.ExportMarkerName)); statErr != nil {
		t.Errorf("export marker should be present after a mid-run failure: %v", statErr)
	}
	// SafeCleanDir must now accept the partial dir (marker, no index.html).
	if cleanErr := fsutil.SafeCleanDir(dir); cleanErr != nil {
		t.Errorf("partial export dir should be recoverable with --clean: %v", cleanErr)
	}
}

// Fixture road decay rates: roadEntry gives its road hexes alternating
// default-rate blends, so a board built from it is not one uniform k.
const (
	fixtureArterialK0 = 0.025
	fixtureLocalK0    = 0.04
)

// roadEntry augments an exportTestEntry with per-hex results on the city's own
// grid, the way a roads compute stores them: every resource covers every hex
// in both scopes, except that roads:city covers only every other hex (so the
// rest are not road hexes in the city scope), at half the all-scope area on
// every fourth hex (so some road hexes carry a "city" object rather than
// city_same), and each roads:city hex has its default-rate decay blend
// (metric.DefaultDecayRate). Export must build the /play board from
// these rows alone, so reading road features fails the test.
func roadEntry(t *testing.T, cfg *config.Config, name, slug, boundary string) CityEntry {
	t.Helper()
	ctx := context.Background()
	entry := exportTestEntry(cfg, name, slug, boundary, map[resource.Type]results.Totals{
		resource.TypeRoads: {Area: 1000, Features: 2},
	})
	store := entry.Store.(*dbtest.MockStore)
	store.ListFeaturesFunc = func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
		t.Errorf("export read %s features; the board must come from stored results", rt)
		return nil, nil
	}
	_, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	hexes, err := cityHexGrid(ctx, entry, geo.NewUTMProjector(lon, lat))
	if err != nil {
		t.Fatalf("cityHexGrid: %v", err)
	}
	slices.SortFunc(hexes, func(a, b geo.Hex) int { return strings.Compare(a.ID, b.ID) })
	cov := make([]results.HexCoverage, len(hexes))
	var cityRoads []results.HexCoverage
	var decay []db.HexMetric
	cityRoadsType := resource.TypeRoads.With(resource.ScopeCity)
	for i, h := range hexes {
		cov[i] = results.HexCoverage{HexID: h.ID, Area: 1000, Share: 0.125}
		if i%2 != 0 {
			continue
		}
		c := cov[i]
		if i%4 == 2 {
			c.Area, c.Share = 500, 0.0625
		}
		cityRoads = append(cityRoads, c)
		k0 := fixtureArterialK0
		if i%4 == 2 {
			k0 = fixtureLocalK0
		}
		decay = append(decay, db.HexMetric{HexID: h.ID, Metric: metric.DefaultDecayRate, Subject: resource.Subject(cityRoadsType), Value: k0})
	}
	var others []resource.Type
	for _, rt := range pavementTypes() {
		if rt != cityRoadsType {
			others = append(others, rt)
		}
	}
	store.HexMetrics = append(coverageFixture(others, cov), results.HexCoverageRows(cityRoadsType, cityRoads)...)
	store.HexMetrics = append(store.HexMetrics, decay...)
	return entry
}

// TestRunSingleCity_WritesPlayPage: the static export must emit a play.html at
// the site root and a data/hexgrid.geojson carrying the board's k, so /play
// works on a plain static host. Single-city has no city selector and an empty
// DATA_PREFIX.
func TestRunSingleCity_WritesPlayPage(t *testing.T) {
	cfg := &config.Config{}
	entry := roadEntry(t, cfg, "Solo City", "solo-city", exportBoundaryA)

	dir := t.TempDir()
	if err := New([]CityEntry{entry}, cfg, dir, "metric", testAssets(t)).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	play, err := os.ReadFile(filepath.Join(dir, "play.html"))
	if err != nil {
		t.Fatalf("read play.html: %v", err)
	}
	if !strings.Contains(string(play), `"data_prefix":""`) {
		t.Errorf("single-city play.html should set data_prefix to empty in PVMT_CONFIG")
	}
	if strings.Contains(string(play), `id="city-select"`) {
		t.Errorf("single-city play.html should not render a city selector")
	}
	assertBoardHexgrid(t, filepath.Join(dir, "data"))
}

// assertBoardHexgrid checks that dataDir's hexgrid.geojson carries the /play
// board (some hex with k) and that no separate board file was written.
func assertBoardHexgrid(t *testing.T, dataDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dataDir, "play-hexes.json")); err == nil {
		t.Errorf("%s: play-hexes.json was written; the board reads hexgrid.geojson", dataDir)
	}
	var fc struct {
		Features []struct {
			Properties map[string]any `json:"properties"`
		} `json:"features"`
	}
	if ok, err := readDataFile(dataDir, HexGridFileName, &fc); err != nil || !ok {
		t.Fatalf("%s: read hexgrid: ok=%v err=%v", dataDir, ok, err)
	}
	for _, f := range fc.Features {
		if _, ok := f.Properties["k"]; ok {
			return
		}
	}
	t.Errorf("%s: no hexgrid feature carries k; the /play board would be empty", dataDir)
}

// TestRunMultiCity_WritesPlayPageAndHexes: multi-city export emits one root
// play.html (with a city selector) and, per city, a hexgrid.geojson under
// cities/<slug>/data/ that carries the board's k.
func TestRunMultiCity_WritesPlayPageAndHexes(t *testing.T) {
	cfg := &config.Config{}
	entries := []CityEntry{
		roadEntry(t, cfg, "Alpha", "alpha", exportBoundaryA),
		roadEntry(t, cfg, "Bravo", "bravo", exportBoundaryB),
	}

	dir := t.TempDir()
	if err := New(entries, cfg, dir, "metric", testAssets(t)).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	play, err := os.ReadFile(filepath.Join(dir, "play.html"))
	if err != nil {
		t.Fatalf("read play.html: %v", err)
	}
	if !strings.Contains(string(play), `id="city-select"`) {
		t.Errorf("multi-city play.html should render a city selector")
	}
	for _, slug := range []string{"alpha", "bravo"} {
		assertBoardHexgrid(t, filepath.Join(dir, "cities", slug, "data"))
	}
}

// TestExport_HexGridBytesPerFeature bounds the per-feature weight of
// hexgrid.geojson, the single largest contributor to the published site. CI
// cannot gate site size — .github/workflows/ci.yaml never runs `make site`, and
// `pvmt check-site`'s SIZES budgets need a built tree — so this is the hermetic
// stand-in: a synthetic grid whose bytes-per-feature must stay under a constant.
//
// The constant is a ceiling on the encoder's output, not a target. It moves DOWN
// at B6 (coordinate precision) and again at B7; lowering it as those land is the
// point of the gate.
func TestExport_HexGridBytesPerFeature(t *testing.T) {
	// The current encoding measures ~520 bytes/feature (logged below); this
	// leaves headroom for a property or two without letting the shape double.
	const maxBytesPerFeature = 700

	ctx := context.Background()
	cfg := &config.Config{Grid: config.GridConfig{HexEdgeM: 200}}
	boundary := func(context.Context) (string, error) { return exportBoundaryA, nil }

	// The grid is built first so the synthetic coverage rows carry the very
	// hex ids BuildHexGeoJSON joins against — a mismatch would silently yield
	// zero features and a vacuous assertion.
	entry := CityEntry{
		Config: cfg,
		City:   config.CityConfig{Name: "Synth City"},
		Store:  &dbtest.MockStore{GetBoundaryFunc: boundary},
		Slug:   "synth-city",
	}
	_, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	proj := geo.NewUTMProjector(lon, lat)
	hexes, err := cityHexGrid(ctx, entry, proj)
	if err != nil {
		t.Fatalf("cityHexGrid: %v", err)
	}
	if len(hexes) < 100 {
		t.Fatalf("test setup: synthetic grid has %d hexes; too few to average over", len(hexes))
	}

	// Every hex carries coverage for every resource in both scopes — the
	// heaviest per-feature shape the exporter can emit — with varying values so
	// the numbers encode at realistic width rather than as a repeated "0".
	stats := make([]results.HexCoverage, len(hexes))
	for i, h := range hexes {
		stats[i] = results.HexCoverage{
			HexID: h.ID,
			Area:  1000 + float64(i%997)*1.37,
			Share: (float64(i%100) + 0.37) / 100,
		}
	}
	entry.Store = &dbtest.MockStore{
		GetBoundaryFunc: boundary,
		HexMetrics:      coverageFixture(pavementTypes(), stats),
	}

	fc, err := BuildHexGeoJSON(ctx, entry, proj)
	if err != nil {
		t.Fatalf("BuildHexGeoJSON: %v", err)
	}
	feats, _ := fc["features"].([]map[string]any)
	if len(feats) == 0 {
		t.Fatal("BuildHexGeoJSON returned no features")
	}

	// json.Marshal is exactly what writeJSON writes to disk.
	data, err := json.Marshal(fc)
	if err != nil {
		t.Fatalf("marshal hexgrid: %v", err)
	}
	perFeature := len(data) / len(feats)
	t.Logf("hexgrid.geojson: %d bytes over %d features = %d bytes/feature", len(data), len(feats), perFeature)
	if perFeature > maxBytesPerFeature {
		t.Errorf("hexgrid.geojson is %d bytes/feature; budget is %d — the exported grid got heavier",
			perFeature, maxBytesPerFeature)
	}
}

// minifyFixture is roadEntry: its per-hex rows carry the city grid's own hex
// ids. BuildHexGeoJSON joins on those ids, and the exporter skips
// hexgrid.geojson entirely when the join is empty — so without that seeding
// the heaviest file would be silently absent and the sweep in
// TestExport_DataFilesAreMinified would not cover it.
func minifyFixture(t *testing.T, cfg *config.Config, name, slug, boundary string) CityEntry {
	t.Helper()
	return roadEntry(t, cfg, name, slug, boundary)
}

// TestExport_DataFilesAreMinified pins that every JSON file the export writes is
// compact. Indentation is dead weight on a published site, and for the files the
// server also serves it was a whitespace-only divergence from /api on the same
// snapshot.
//
// The assertion is "no newline byte anywhere", not "no newline + two spaces":
// json.Marshal emits no newline at all, so the wider assertion is the true
// invariant and it also catches a tab-indented or SetIndent-configured encoder
// that a two-space probe would wave through. Escaping keeps it free of false
// failures — real newlines inside string values are written as \n, and embedded
// json.RawMessage goes through compact().
//
// The export is run multi-city on purpose: cities.json is a writeJSON call site
// that only that path reaches, and it is not in DataFileNames, so nothing else
// would notice it regressing to an indented encoder.
func TestExport_DataFilesAreMinified(t *testing.T) {
	cfg := &config.Config{Grid: config.GridConfig{HexEdgeM: 200}}
	entries := []CityEntry{
		minifyFixture(t, cfg, "Alpha", "alpha", exportBoundaryA),
		minifyFixture(t, cfg, "Bravo", "bravo", exportBoundaryB),
	}

	dir := t.TempDir()
	if err := New(entries, cfg, dir, "metric", testAssets(t)).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	rels := []string{"cities.json"}
	for _, entry := range entries {
		for _, name := range DataFileNames {
			rels = append(rels, filepath.Join("cities", entry.Slug, "data", name))
		}
	}

	// Each file must exist as well as be minified — one the fixture failed to
	// produce would make this vacuous for that name rather than fail loudly.
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Errorf("read %s: %v", rel, err)
			continue
		}
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			t.Errorf("%s has a newline at byte %d; every exported JSON file must be written minified", rel, i)
		}
	}
}

// TestExportOneCity_SkippedCityLeavesNoDirectory: a city exportOneCity skips
// (no boundary, kept=false) must leave no data directory behind. cities.json
// omits it, but checksite's classifyExample walks every directory under
// cities/ regardless of cities.json, so a leftover empty directory would fail
// the `pvmt check-site` deploy gate on a city the exporter deliberately
// dropped.
func TestExportOneCity_SkippedCityLeavesNoDirectory(t *testing.T) {
	ctx := context.Background()
	outDir := t.TempDir()
	cfg := &config.Config{Forecast: jsFixtureForecastConfig(t)}

	// entry returns a fixture city wired to cfg, with a snapshot whose hash
	// matches so PinNewest pins it silently.
	entry := func(slug string) CityEntry {
		e := jsFixtureEntry(t)
		e.Config = cfg
		e.Slug = slug
		hash := cfg.CityHash(&e.City)
		e.Store.(*dbtest.MockStore).ListSnapshotsFunc = func(context.Context) ([]db.SnapshotCity, error) {
			return []db.SnapshotCity{{ID: jsFixtureSnapshotID, ConfigHash: hash, Parts: paved}}, nil
		}
		return e
	}

	// A city that has been ingested and computed: real boundary, matching snapshot.
	kept := entry("kept-city")

	// The skipped city: ingest failed earlier, so GetBoundary returns "" and
	// BBoxAndCenter reports ErrNoBoundary before anything is written.
	ghost := entry("ghost-city")
	ghost.Store.(*dbtest.MockStore).GetBoundaryFunc = func(context.Context) (string, error) { return "", nil }

	e := New([]CityEntry{kept, ghost}, cfg, outDir, "imperial", testAssets(t))

	if _, ok, err := e.exportOneCity(ctx, kept); err != nil || !ok {
		t.Fatalf("exportOneCity(kept) = ok %v, err %v; want ok true, nil", ok, err)
	}
	info, ok, err := e.exportOneCity(ctx, ghost)
	if err != nil {
		t.Fatalf("exportOneCity(ghost) = %v; want nil error (a skip is not a failure)", err)
	}
	if ok {
		t.Fatalf("exportOneCity(ghost) kept = true; want false — the city has no boundary")
	}
	if info.Slug != "" || info.Name != "" {
		t.Errorf("exportOneCity(ghost) info = %+v; want the zero CityInfo", info)
	}

	if _, err := os.Stat(filepath.Join(outDir, "cities", "ghost-city")); !os.IsNotExist(err) {
		t.Errorf("cities/ghost-city exists after a skip (stat err %v); check-site walks every "+
			"directory under cities/ and would fail the deploy gate on it", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "cities", "kept-city", "data")); err != nil {
		t.Errorf("cities/kept-city/data missing: %v", err)
	}
}

// TestRun_WritesPublishReadyRoot pins what both layouts leave at the site
// root, which check-site and a static host need: a zero-byte .nojekyll (or
// GitHub Pages runs Jekyll and drops "_"-prefixed files), both pages, and
// exactly the assets the exporter was given. Multi-city also writes
// cities.json in selector order; the sort itself is TestSortCitiesByName.
func TestRun_WritesPublishReadyRoot(t *testing.T) {
	cfg := &config.Config{}
	for _, tc := range []struct {
		name    string
		entries []CityEntry
		cities  []string // cities.json names, nil for single-city
	}{
		{"single-city", []CityEntry{exportTestEntry(cfg, "Solo City", "solo-city", exportBoundaryA, nil)}, nil},
		{"multi-city", []CityEntry{
			exportTestEntry(cfg, "Bravo", "bravo", exportBoundaryA, nil),
			exportTestEntry(cfg, "alpha", "alpha", exportBoundaryB, nil),
		}, []string{"alpha", "Bravo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			assets := testAssets(t)
			if err := New(tc.entries, cfg, dir, "metric", assets).Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if info, err := os.Stat(filepath.Join(dir, ".nojekyll")); err != nil {
				t.Errorf(".nojekyll: %v", err)
			} else if info.Size() != 0 {
				t.Errorf(".nojekyll is %d bytes; must be zero-byte", info.Size())
			}
			for _, page := range []string{"index.html", "play.html"} {
				if _, err := os.Stat(filepath.Join(dir, page)); err != nil {
					t.Errorf("%s: %v", page, err)
				}
			}
			for _, name := range assets.Names() {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(got) != string(assets.Bytes(name)) {
					t.Errorf("%s = %q (%v); want the exporter's asset bytes", name, got, err)
				}
			}
			if tc.cities == nil {
				return
			}
			raw, err := os.ReadFile(filepath.Join(dir, "cities.json"))
			if err != nil {
				t.Fatalf("cities.json: %v", err)
			}
			var cities []CityInfo
			if err := json.Unmarshal(raw, &cities); err != nil {
				t.Fatalf("unmarshal cities.json: %v", err)
			}
			var got []string
			for _, c := range cities {
				got = append(got, c.Name)
			}
			if !slices.Equal(got, tc.cities) {
				t.Errorf("cities.json order = %v; want %v", got, tc.cities)
			}
		})
	}
}

// TestSortCitiesByName pins the selector order: case-insensitive by Name, and
// stable for ties so same-named cities keep config order.
func TestSortCitiesByName(t *testing.T) {
	cities := []CityInfo{
		{Name: "Charlie", Slug: "charlie"},
		{Name: "apple", Slug: "apple"},
		{Name: "Springfield", Slug: "springfield-il"},
		{Name: "Banana", Slug: "banana"},
		{Name: "springfield", Slug: "springfield-ma"},
	}
	sortCitiesByName(cities)
	var got []string
	for _, c := range cities {
		got = append(got, c.Slug)
	}
	// A byte sort would put "apple" after "Banana" ('B' < 'a'); the two
	// Springfields compare equal and must keep their input order.
	want := []string{"apple", "banana", "charlie", "springfield-il", "springfield-ma"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v; want %v", got, want)
	}
}

// TestSortCitiesByName_PlaceThenState: "Bell, CA" sorts before
// "Bell Gardens, CA" (whole names would not: ' ' < ','), and a shared place
// orders by state. selectors.test.mjs pins the same
// order for the client's byName.
func TestSortCitiesByName_PlaceThenState(t *testing.T) {
	cities := []CityInfo{
		{Name: "Bell Gardens, CA", Slug: "bell-gardens-ca"},
		{Name: "Bellflower, CA", Slug: "bellflower-ca"},
		{Name: "Portland, OR", Slug: "portland-or"},
		{Name: "Bell, CA", Slug: "bell-ca"},
		{Name: "Portland, ME", Slug: "portland-me"},
		{Name: "Bell", Slug: "bell"},
	}
	sortCitiesByName(cities)
	var got []string
	for _, c := range cities {
		got = append(got, c.Slug)
	}
	want := []string{"bell", "bell-ca", "bell-gardens-ca", "bellflower-ca", "portland-me", "portland-or"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v; want %v", got, want)
	}
}

// TestRunMultiCity_WritesCompareRollup: a
// multi-city export writes compare.json beside cities.json, one entry per
// exported city, each a strict subset of that city's files, and far smaller
// than the four files the Compare and Aggregate tabs used to fetch.
func TestRunMultiCity_WritesCompareRollup(t *testing.T) {
	cfg := &config.Config{}
	entries := []CityEntry{
		roadEntry(t, cfg, "Alpha", "alpha", exportBoundaryA),
		roadEntry(t, cfg, "Bravo", "bravo", exportBoundaryB),
	}
	dir := t.TempDir()
	if err := New(entries, cfg, dir, "metric", testAssets(t)).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, CompareFileName))
	if err != nil {
		t.Fatalf("read %s: %v", CompareFileName, err)
	}
	var got CompareJSON
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.V != CompareFileVersion || len(got.Cities) != 2 {
		t.Fatalf("compare.json = v%d with %d cities, want v%d with 2", got.V, len(got.Cities), CompareFileVersion)
	}
	var perCity int
	for _, c := range got.Cities {
		dataDir := filepath.Join(dir, "cities", c.Slug, "data")
		for _, name := range []string{"meta.json", "scenarios.json", "forecast_seed.json", "forecast.json"} {
			if fi, err := os.Stat(filepath.Join(dataDir, name)); err == nil {
				perCity += int(fi.Size())
			}
		}
		if c.Meta == nil || len(c.Meta.Stats) == 0 || c.Seed == nil || len(c.Scenarios) == 0 || len(c.Forecast) != 1 {
			t.Errorf("%s: incomplete entry %+v", c.Slug, c)
			continue
		}
		var meta MetaJSON
		if ok, err := readDataFile(dataDir, "meta.json", &meta); err != nil || !ok {
			t.Fatal(err)
		}
		if c.Meta.TotalPaved != meta.TotalPaved || c.Meta.Stats[0].TotalArea != meta.Stats[0].TotalArea {
			t.Errorf("%s: rollup meta %+v differs from meta.json", c.Slug, c.Meta)
		}
		if c.Forecast[0].ResourceType != "roads" {
			t.Errorf("%s: rollup forecast is %q, want the roads entry", c.Slug, c.Forecast[0].ResourceType)
		}
		// The rollup's seed carries every setting the Aggregate divergence
		// note compares, as forecast_seed.json has them.
		var seed ForecastSeedJSON
		if ok, err := readDataFile(dataDir, "forecast_seed.json", &seed); err != nil || !ok {
			t.Fatal(err)
		}
		if c.Seed.CostMultiplier != seed.CostMultiplier || c.Seed.TreatmentCycleYears != seed.TreatmentCycleYears ||
			c.Seed.GrowthRate != seed.GrowthRate || c.Seed.CostMultiplier == 0 || c.Seed.TreatmentCycleYears == 0 {
			t.Errorf("%s: rollup seed %+v lacks forecast_seed.json's multiplier %g, cycle %g, growth %g",
				c.Slug, c.Seed, seed.CostMultiplier, seed.TreatmentCycleYears, seed.GrowthRate)
		}
	}
	if len(raw)*4 > perCity {
		t.Errorf("compare.json is %d bytes against %d for the per-city files; want well under a quarter", len(raw), perCity)
	}
}
