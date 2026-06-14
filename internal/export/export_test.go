package export

import (
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
	"github.com/jcrussell/solvent-streets/internal/resource"
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
	wantMeta, err := BuildMeta(context.Background(), entry)
	if err != nil {
		t.Fatalf("BuildMeta: %v", err)
	}
	wantMetaBytes, err := json.MarshalIndent(wantMeta, "", "  ")
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
