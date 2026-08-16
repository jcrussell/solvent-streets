package export

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// exportTestEntry builds a CityEntry whose store satisfies the exporter's
// preconditions: a boundary (so BBoxAndCenter/Info succeed) and a snapshot
// whose config hash matches cfg.Hash() (so RequireMatchingSnapshot passes).
// Compute results drive the per-resource stats / forecast totals.
func exportTestEntry(cfg *config.Config, name, slug, boundary string, results map[resource.Type]db.ComputeResult) CityEntry {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { return boundary, nil },
		ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) {
			return []db.Snapshot{{ID: 1, ConfigHash: cfg.Hash()}}, nil
		},
		LatestComputeResultFunc: func(_ context.Context, rt resource.Type) (*db.ComputeResult, error) {
			if r, ok := results[rt]; ok {
				return &r, nil
			}
			return nil, sql.ErrNoRows
		},
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
// source of tags for cities.json and the index CITIES array (solvent-streets-8je6).
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
	// An untagged city yields no tags.
	untagged := exportTestEntry(&config.Config{}, "Reno", "reno", exportBoundaryB, nil)
	uinfo, err := untagged.Info(context.Background())
	if err != nil {
		t.Fatalf("Info (untagged): %v", err)
	}
	if len(uinfo.Tags) != 0 {
		t.Errorf("untagged city Info().Tags = %v; want empty", uinfo.Tags)
	}
}

// TestRunMultiCity_WritesNojekyll asserts the multi-city path writes a zero-byte
// .nojekyll at the site root (export.go:159), which only the single-city path
// was asserting (solvent-streets-8je6).
func TestRunMultiCity_WritesNojekyll(t *testing.T) {
	cfg := &config.Config{}
	entries := []CityEntry{
		exportTestEntry(cfg, "Alpha", "alpha", exportBoundaryA, nil),
		exportTestEntry(cfg, "Beta", "beta", exportBoundaryB, nil),
	}
	dir := t.TempDir()
	if err := New(entries, cfg, dir, "metric").Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, ".nojekyll"))
	if err != nil {
		t.Fatalf(".nojekyll should exist after multi-city run: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf(".nojekyll should be zero-byte, got %d bytes", info.Size())
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
	entry := exportTestEntry(cfg, "Solo City", "solo-city", exportBoundaryA, map[resource.Type]db.ComputeResult{
		resource.TypeRoads: {ResourceType: resource.TypeRoads, TotalArea: 1000, FeatureCount: 10},
	})

	dir := t.TempDir()
	e := New([]CityEntry{entry}, cfg, dir, "metric")
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

	// index.html was rendered from the reused meta (project_name comes straight
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

// TestRunSingleCity_RendersMaterialsTab pins the Materials tab end-to-end: the
// exported page must carry the tab button + panel, and the forecast seed the
// page reads must ship the per-tier material intensities the tab multiplies by
// treated area. Without material_tiers the tab has nothing to compute from.
func TestRunSingleCity_RendersMaterialsTab(t *testing.T) {
	cfg := &config.Config{}
	entry := exportTestEntry(cfg, "Solo City", "solo-city", exportBoundaryA, map[resource.Type]db.ComputeResult{
		resource.TypeRoads: {ResourceType: resource.TypeRoads, TotalArea: 1000, FeatureCount: 10},
	})

	dir := t.TempDir()
	if err := New([]CityEntry{entry}, cfg, dir, "metric").Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	indexHTML, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	for _, want := range []string{`data-tab="materials-tab"`, `id="materials-tab"`, `id="materials-charts"`} {
		if !strings.Contains(string(indexHTML), want) {
			t.Errorf("index.html missing Materials markup %q", want)
		}
	}

	seed, err := os.ReadFile(filepath.Join(dir, "data", "forecast_seed.json"))
	if err != nil {
		t.Fatalf("read forecast_seed.json: %v", err)
	}
	if !strings.Contains(string(seed), `"material_tiers"`) {
		t.Errorf("forecast_seed.json missing material_tiers: %s", seed)
	}
	if !strings.Contains(string(seed), `"mix_kg_per_sqm"`) {
		t.Errorf("forecast_seed.json material_tiers missing mix_kg_per_sqm: %s", seed)
	}
}

// TestRunMultiCity_FailureLeavesRecoverableDir pins yvlv.19: when the export
// fails partway (here, a second city whose stored snapshot hash doesn't match,
// so RequireMatchingSnapshot fails), the output dir is left non-empty and
// WITHOUT index.html — but WITH the ExportMarkerName sentinel written up front,
// so a retry with --clean can recover it instead of dead-ending on a manual
// rm -rf.
func TestRunMultiCity_FailureLeavesRecoverableDir(t *testing.T) {
	cfg := &config.Config{}
	good := exportTestEntry(cfg, "Good", "good", exportBoundaryA, map[resource.Type]db.ComputeResult{
		resource.TypeRoads: {ResourceType: resource.TypeRoads, TotalArea: 1000, FeatureCount: 10},
	})
	// A city whose only snapshot carries a non-matching config hash: its
	// RequireMatchingSnapshot returns ErrNoMatchingSnapshot, aborting the run
	// after the marker (and possibly the good city's data) is written.
	badStore := &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { return exportBoundaryB, nil },
		ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) {
			return []db.Snapshot{{ID: 2, ConfigHash: "mismatch"}}, nil
		},
	}
	bad := CityEntry{Config: cfg, City: config.CityConfig{Name: "Bad"}, Store: badStore, Slug: "bad"}

	dir := t.TempDir()
	err := New([]CityEntry{good, bad}, cfg, dir, "metric").Run(context.Background())
	if err == nil {
		t.Fatal("expected export to fail on the mismatched-snapshot city")
	}

	// index.html is NOT present (it's written last, after the failing city).
	if _, statErr := os.Stat(filepath.Join(dir, "index.html")); !os.IsNotExist(statErr) {
		t.Errorf("index.html should be absent after a mid-run failure (stat err=%v)", statErr)
	}
	// The early marker IS present, so the partial dir is recoverable.
	if _, statErr := os.Stat(filepath.Join(dir, cmdutil.ExportMarkerName)); statErr != nil {
		t.Errorf("export marker should be present after a mid-run failure: %v", statErr)
	}
	// SafeCleanDir must now accept the partial dir (marker, no index.html).
	if cleanErr := cmdutil.SafeCleanDir(dir); cleanErr != nil {
		t.Errorf("partial export dir should be recoverable with --clean: %v", cleanErr)
	}
}

// TestRunMultiCity_AlphabetisesCitySelector is the dao7 regression: the city
// selector must be sorted case-insensitively by Name, not left in TOML
// (definition) order. Feed entries out of order and assert cities.json — the
// source for the dropdown and JS CITIES array — comes out alphabetised.
func TestRunMultiCity_AlphabetisesCitySelector(t *testing.T) {
	cfg := &config.Config{}
	// Deliberately unsorted, with a lowercase-leading name to exercise the
	// case-insensitive comparator (a flat byte sort would order "apple" after
	// "Banana" because 'B' < 'a').
	entries := []CityEntry{
		exportTestEntry(cfg, "Charlie", "charlie", exportBoundaryA, nil),
		exportTestEntry(cfg, "apple", "apple", exportBoundaryB, nil),
		exportTestEntry(cfg, "Banana", "banana", exportBoundaryA, nil),
	}

	dir := t.TempDir()
	e := New(entries, cfg, dir, "metric")
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "cities.json"))
	if err != nil {
		t.Fatalf("read cities.json: %v", err)
	}
	var cities []CityInfo
	if err := json.Unmarshal(raw, &cities); err != nil {
		t.Fatalf("unmarshal cities.json: %v", err)
	}
	gotNames := make([]string, len(cities))
	for i, c := range cities {
		gotNames[i] = c.Name
	}
	want := []string{"apple", "Banana", "Charlie"}
	if len(gotNames) != len(want) {
		t.Fatalf("cities.json has %d entries %v; want %d", len(gotNames), gotNames, len(want))
	}
	for i := range want {
		if gotNames[i] != want[i] {
			t.Errorf("cities.json order = %v; want %v (case-insensitive by Name)", gotNames, want)
			break
		}
	}
}

// TestRunMultiCity_IndexAlignsDataPrefixToSelector: on initial load without a
// ?city= deep link, the browser default-selects the first DOM <option>, which
// follows CitiesByTag (tag groups first) and can differ from CITIES[0]
// (flat alphabetical) that seeds DATA_PREFIX. The rendered index must contain
// the boot-time alignment that re-derives DATA_PREFIX from the selector's
// actual default value in the no-?city= (else) branch, so the page never loads
// one city's data while the dropdown names another. See bead yvlv.24.
func TestRunMultiCity_IndexAlignsDataPrefixToSelector(t *testing.T) {
	cfg := &config.Config{}
	entries := []CityEntry{
		exportTestEntry(cfg, "Charlie", "charlie", exportBoundaryA, nil),
		exportTestEntry(cfg, "apple", "apple", exportBoundaryB, nil),
	}

	dir := t.TempDir()
	if err := New(entries, cfg, dir, "metric").Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The selector-derived DATA_PREFIX alignment lives in the extracted app.js
	// (written alongside index.html), not inline in the page.
	appJS, err := os.ReadFile(filepath.Join(dir, "app.js"))
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	const want = "DATA_PREFIX = 'cities/' + sel.value + '/'"
	if !strings.Contains(string(appJS), want) {
		t.Errorf("app.js missing selector-derived DATA_PREFIX alignment %q", want)
	}
}

// boundaryMinLon returns the smallest longitude in a GeoJSON Polygon boundary
// string. roadEntry uses it to place roads relative to the boundary's western
// edge, so the same helper drops roads inside exportBoundaryA (-122.5..-122.4)
// or exportBoundaryB (-121.5..-121.4) — otherwise a fixed -122.x road would
// clip out of B and leave bravo with no play-hexes.
func boundaryMinLon(t *testing.T, boundary string) float64 {
	t.Helper()
	var poly struct {
		Coordinates [][][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(boundary), &poly); err != nil {
		t.Fatalf("parse boundary: %v", err)
	}
	if len(poly.Coordinates) == 0 || len(poly.Coordinates[0]) == 0 {
		t.Fatalf("boundary has no ring")
	}
	minLon := poly.Coordinates[0][0][0]
	for _, pt := range poly.Coordinates[0] {
		if pt[0] < minLon {
			minLon = pt[0]
		}
	}
	return minLon
}

// roadEntry augments an exportTestEntry with city-jurisdiction road features so
// BuildPlayHexes emits a board (and the exporter writes play-hexes.json). The
// roads sit inside the given boundary (offsets from its western edge) so their
// buffered footprint clips into hexes.
func roadEntry(t *testing.T, cfg *config.Config, name, slug, boundary string) CityEntry {
	t.Helper()
	lon0 := boundaryMinLon(t, boundary)
	entry := exportTestEntry(cfg, name, slug, boundary, map[resource.Type]db.ComputeResult{
		resource.TypeRoads: {ResourceType: resource.TypeRoads, TotalArea: 1000, FeatureCount: 2},
	})
	entry.Store.(*dbtest.MockStore).ListFeaturesFunc = func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
		if rt == resource.TypeRoads {
			return []db.Feature{
				lineFeature("primary-1", "primary", lon0+0.015, 37.515, lon0+0.030, 37.515),
				lineFeature("res-1", "residential", lon0+0.080, 37.585, lon0+0.095, 37.585),
			}, nil
		}
		return nil, nil
	}
	return entry
}

// TestRunSingleCity_WritesPlayPage: the static export must emit a play.html at
// the site root and the board's data/play-hexes.json, so /play works on a plain
// static host. Single-city has no city selector and an empty DATA_PREFIX.
func TestRunSingleCity_WritesPlayPage(t *testing.T) {
	cfg := &config.Config{}
	entry := roadEntry(t, cfg, "Solo City", "solo-city", exportBoundaryA)

	dir := t.TempDir()
	if err := New([]CityEntry{entry}, cfg, dir, "metric").Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	play, err := os.ReadFile(filepath.Join(dir, "play.html"))
	if err != nil {
		t.Fatalf("read play.html: %v", err)
	}
	if !strings.Contains(string(play), `"dataPrefix":""`) {
		t.Errorf("single-city play.html should set dataPrefix to empty in PVMT_CONFIG")
	}
	if strings.Contains(string(play), `id="city-select"`) {
		t.Errorf("single-city play.html should not render a city selector")
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "play-hexes.json")); err != nil {
		t.Errorf("expected data/play-hexes.json: %v", err)
	}
	// The exporter emits a publish-ready tree: a zero-byte .nojekyll at the root
	// (required by check-site; this was gensite's job before consolidation).
	if info, err := os.Stat(filepath.Join(dir, ".nojekyll")); err != nil {
		t.Errorf("expected .nojekyll at site root: %v", err)
	} else if info.Size() != 0 {
		t.Errorf(".nojekyll must be zero-byte, got %d bytes", info.Size())
	}
}

// TestRunMultiCity_WritesPlayPageAndHexes: multi-city export emits one root
// play.html (with a city selector) and per-city play-hexes.json under
// cities/<slug>/data/.
func TestRunMultiCity_WritesPlayPageAndHexes(t *testing.T) {
	cfg := &config.Config{}
	entries := []CityEntry{
		roadEntry(t, cfg, "Alpha", "alpha", exportBoundaryA),
		roadEntry(t, cfg, "Bravo", "bravo", exportBoundaryB),
	}

	dir := t.TempDir()
	if err := New(entries, cfg, dir, "metric").Run(context.Background()); err != nil {
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
		if _, err := os.Stat(filepath.Join(dir, "cities", slug, "data", "play-hexes.json")); err != nil {
			t.Errorf("expected cities/%s/data/play-hexes.json: %v", slug, err)
		}
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

	// The grid is built first so the synthetic hex_stats rows carry the very
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
	stats := make([]db.HexStat, len(hexes))
	for i, h := range hexes {
		stats[i] = db.HexStat{
			HexID:      h.ID,
			Area:       1000 + float64(i%997)*1.37,
			PctCovered: float64(i%100) + 0.37,
		}
	}
	entry.Store = &dbtest.MockStore{
		GetBoundaryFunc: boundary,
		ListHexStatsFunc: func(_ context.Context, rt resource.Type) ([]db.HexStat, error) {
			rows := make([]db.HexStat, len(stats))
			copy(rows, stats)
			for i := range rows {
				rows[i].ResourceType = rt
			}
			return rows, nil
		},
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

// minifyFixture builds a roadEntry whose hex_stats rows carry the city grid's
// own hex ids. BuildHexGeoJSON joins on those ids, and the exporter skips
// hexgrid.geojson entirely when the join is empty (export.go:279) — so without
// this seeding the heaviest file would be silently absent and the sweep in
// TestExport_DataFilesAreMinified would not cover it.
func minifyFixture(t *testing.T, cfg *config.Config, name, slug, boundary string) CityEntry {
	t.Helper()
	ctx := context.Background()
	entry := roadEntry(t, cfg, name, slug, boundary)
	_, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatalf("BBoxAndCenter: %v", err)
	}
	hexes, err := cityHexGrid(ctx, entry, geo.NewUTMProjector(lon, lat))
	if err != nil {
		t.Fatalf("cityHexGrid: %v", err)
	}
	entry.Store.(*dbtest.MockStore).ListHexStatsFunc = func(_ context.Context, rt resource.Type) ([]db.HexStat, error) {
		rows := make([]db.HexStat, len(hexes))
		for i, h := range hexes {
			rows[i] = db.HexStat{HexID: h.ID, ResourceType: rt, Area: 1000, PctCovered: 12.5}
		}
		return rows, nil
	}
	return entry
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
	if err := New(entries, cfg, dir, "metric").Run(context.Background()); err != nil {
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
