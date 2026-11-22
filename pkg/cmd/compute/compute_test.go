package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/metric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/tui"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmd/cmdtest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

var testCity = cmdtest.NewTestCity()

var testCfg = cmdtest.NewTestConfig(testCity)

var testBoundary = `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`

func TestNewCmdCompute_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	// Only IOStreams is needed: runF short-circuits before any other Factory field is accessed.
	f := &cmdutil.Factory{IOStreams: ios, UnitSystem: func() units.System { return units.Imperial }}
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdCompute(f, rt, func(_ context.Context, opts *Options) error {
		called = true
		if opts.ResourceType.Type() != resource.TypeRoads {
			t.Errorf("expected KindRoads, got %v", opts.ResourceType.Type())
		}
		return nil
	})

	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("runF was not called")
	}
}

func TestRunCompute_NoFeatures(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
	}
	ios, _, _, errBuf := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		CityDB: func() (db.Store, error) {
			return store, nil
		},
		CurrentCity: func() (*config.CityConfig, error) {
			return testCity, nil
		},
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("expected ErrNoResults, got: %v", err)
	}
	// The hint's command retries exactly this city's missing layer.
	if want := "pvmt --city " + testCity.Slug() + " roads ingest"; !strings.Contains(errBuf.String(), want) {
		t.Errorf("stderr should hint %q, got: %s", want, errBuf.String())
	}
}

// resultBlock is one "<slug>: <label>:" block of a compute's stdout.
type resultBlock struct {
	label string
	lines []string
}

// resultBlocks parses a compute's stdout into its result blocks and reports
// the lines that belong to none: stdout is data only, so there should be
// none. A block is a ResultHeader line followed by indented lines.
func resultBlocks(out, slug string) (blocks []resultBlock, stray []string) {
	for line := range strings.SplitSeq(out, "\n") {
		label, isHeader := strings.CutPrefix(line, slug+": ")
		switch {
		case strings.TrimSpace(line) == "":
		case isHeader && strings.HasSuffix(label, ":"):
			blocks = append(blocks, resultBlock{label: strings.TrimSuffix(label, ":")})
		case strings.HasPrefix(line, "  ") && len(blocks) > 0:
			last := &blocks[len(blocks)-1]
			last.lines = append(last.lines, line)
		default:
			stray = append(stray, line)
		}
	}
	return blocks, stray
}

// blockLabeled returns the block whose label is label, failing the test when
// stdout has none.
func blockLabeled(t *testing.T, out, slug, label string) resultBlock {
	t.Helper()
	blocks, _ := resultBlocks(out, slug)
	for _, b := range blocks {
		if b.label == label {
			return b
		}
	}
	t.Fatalf("stdout has no %q block:\n%s", label, out)
	return resultBlock{}
}

func TestRunCompute_Success(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{
				{
					ID:           "test1",
					ResourceType: resource.TypeRoads,
					Name:         "Test Rd",
					Tags:         map[string]string{"highway": "residential"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
				},
			}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		CityDB: func() (db.Store, error) {
			return store, nil
		},
		CurrentCity: func() (*config.CityConfig, error) {
			return testCity, nil
		},
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	total, ok, _ := results.LoadTotal(context.Background(), store, resource.TypeRoads)
	if !ok || total.Area <= 0 {
		t.Fatalf("expected the roads city totals to be saved, got %+v", total)
	}
	// The report shows the saved area in the configured (imperial) units.
	if want := units.FormatArea(total.Area, units.Imperial); !strings.Contains(output, want) {
		t.Errorf("stdout lacks the saved area in imperial units (%s), got: %s", want, output)
	}
	if metricArea := units.FormatArea(total.Area, units.Metric); strings.Contains(output, metricArea) {
		t.Errorf("stdout shows the area in metric units (%s) under --units imperial: %s", metricArea, output)
	}
}

// TestRunCompute_ChatterToErrOut: in the plain (non-TTY,
// non-JSON) path, human progress chatter ("Loading...", "Total: N features",
// "Generated N hexes", ...) must land on ErrOut, while the actual result DATA
// (printResults / printCohortBreakdown) stays on Out. A redirected stdout must
// therefore capture only the command's data, not the progress noise.
func TestRunCompute_ChatterToErrOut(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
	}
	ios, _, stdout, errBuf := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v", err)
	}

	out := stdout.String()
	errStr := errBuf.String()

	// stdout is result blocks and nothing else: every line is a city-named
	// header or an indented line of the block under it.
	blocks, stray := resultBlocks(out, testCity.Slug())
	if len(blocks) == 0 || len(stray) > 0 {
		t.Errorf("stdout should be result blocks only; got %d blocks and stray lines %q:\n%s", len(blocks), stray, out)
	}
	// The result data, the saved area, is on stdout and not on stderr.
	total, ok, _ := results.LoadTotal(context.Background(), store, resource.TypeRoads)
	if !ok {
		t.Fatal("no roads total saved")
	}
	area := units.FormatArea(total.Area, units.Metric)
	if !strings.Contains(out, area) || strings.Contains(errStr, area) {
		t.Errorf("the area %s should be on stdout only:\nstdout: %s\nstderr: %s", area, out, errStr)
	}
	// Progress is on stderr, and none of it leaked: the streams share no line.
	if strings.TrimSpace(errStr) == "" {
		t.Error("no progress on stderr")
	}
	onStdout := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		onStdout[strings.TrimSpace(line)] = true
	}
	for line := range strings.SplitSeq(errStr, "\n") {
		if l := strings.TrimSpace(line); l != "" && onStdout[l] {
			t.Errorf("progress line %q is also on stdout", l)
		}
	}
}

// countingSource wraps a resource.Source to count buffer-and-index invocations
// so tests can assert the pipeline doesn't re-buffer the city subset or per
// classification.
type countingSource struct {
	inner       resource.Source
	pairedCalls int
}

func (c *countingSource) Type() resource.Type               { return c.inner.Type() }
func (c *countingSource) OverpassQuery(b [4]float64) string { return c.inner.OverpassQuery(b) }
func (c *countingSource) HasCohorts() bool                  { return c.inner.HasCohorts() }
func (c *countingSource) AsphaltSurfaced() bool             { return c.inner.AsphaltSurfaced() }
func (c *countingSource) BufferFeaturesPaired(ctx context.Context, f []resource.Feature, p geo.Projector) []resource.BufferedFeature {
	c.pairedCalls++
	return c.inner.BufferFeaturesPaired(ctx, f, p)
}

// TestRunCompute_BuffersFeaturesOncePerRun:
// across the all-jurisdiction pass, the city-only pass, and the per-class
// cohort breakdown, every feature is buffered exactly once. Regression
// guard against re-introducing a second BufferFeaturesPaired pass on the city
// subset.
func TestRunCompute_BuffersFeaturesOncePerRun(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{
				{
					ID:           "city1",
					ResourceType: resource.TypeRoads,
					Name:         "City Rd",
					Tags:         map[string]string{"highway": "residential"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
				},
				{
					ID:           "city2",
					ResourceType: resource.TypeRoads,
					Tags:         map[string]string{"highway": "tertiary"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6820],[-121.7690,37.6830]]}`,
				},
				{
					ID:           "state1",
					ResourceType: resource.TypeRoads,
					Tags:         map[string]string{"highway": "trunk"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6840],[-121.7690,37.6850]]}`,
				},
			}, nil
		},
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	counter := &countingSource{inner: &resource.Pavement{}}
	cmd := NewCmdCompute(f, counter, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v", err)
	}

	if counter.pairedCalls != 1 {
		t.Errorf("BufferFeaturesPaired called %d times; want 1 (all jurisdictions, city subset, and per-class cohorts must share the buffered slice)",
			counter.pairedCalls)
	}
}

// roadsFeaturesFunc returns a small set of road features (city + state) that
// buffer to non-empty geometries inside testBoundary, used by the fixtures
// below that need real hex stats and cohorts.
func roadsFeaturesFunc(_ context.Context, _ resource.Type) ([]db.Feature, error) {
	return []db.Feature{
		{
			ID:           "city1",
			ResourceType: resource.TypeRoads,
			Tags:         map[string]string{"highway": "residential"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
		},
		{
			ID:           "state1",
			ResourceType: resource.TypeRoads,
			Tags:         map[string]string{"highway": "trunk"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6840],[-121.7690,37.6850]]}`,
		},
	}, nil
}

// TestRunCompute_PrebuiltGridMatchesInline pins grid sharing: computing a resource with an injected PrebuiltGrid must produce
// byte-identical saved hex stats to computing it with the grid built inline.
// This is the equivalence guardrail for `all compute` reusing one clipped grid
// across the per-resource passes instead of rebuilding it each time.
func TestRunCompute_PrebuiltGridMatchesInline(t *testing.T) {
	ctx := context.Background()

	// Several short roads scattered across testBoundary so multiple hexes get
	// coverage — the more covered hexes, the more the comparison bites.
	features := []db.Feature{
		{ID: "r1", ResourceType: resource.TypeRoads, Tags: map[string]string{"highway": "residential"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7660,37.6820]]}`},
		{ID: "r2", ResourceType: resource.TypeRoads, Tags: map[string]string{"highway": "tertiary"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7500,37.6900],[-121.7450,37.6930]]}`},
		{ID: "r3", ResourceType: resource.TypeRoads, Tags: map[string]string{"highway": "trunk"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7800,37.7000],[-121.7750,37.7010]]}`},
	}

	run := func(grid []geo.Hex) []db.HexMetric {
		t.Helper()
		store := &dbtest.MockStore{
			GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
			ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) { return features, nil },
		}
		ios, _, _, _ := iostreams.Test()
		opts := &Options{
			IO:           ios,
			UnitSystem:   func() units.System { return units.Metric },
			CityDB:       func() (db.Store, error) { return store, nil },
			CurrentCity:  func() (*config.CityConfig, error) { return testCity, nil },
			Config:       func() (*config.Config, error) { return testCfg, nil },
			ResourceType: &resource.Pavement{},
			PrebuiltGrid: grid,
		}
		if err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts); err != nil {
			t.Fatalf("doCompute: %v", err)
		}
		return store.HexMetrics
	}

	// Build the shared grid exactly as BuildCityGrid would for this city.
	bstore := &dbtest.MockStore{GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil }}
	boundaryGJSON, bbox, proj, err := LoadBoundary(ctx, bstore, testCity, io.Discard)
	if err != nil {
		t.Fatalf("LoadBoundary: %v", err)
	}
	grid := BuildClippedHexGrid(ctx, testCfg, testCity, proj, bbox, boundaryGJSON, io.Discard)
	if len(grid) == 0 {
		t.Fatal("shared grid is empty; fixture would not exercise the comparison")
	}

	inline := run(nil)
	shared := run(grid)

	if len(inline) == 0 {
		t.Fatal("inline run saved no hex stats; fixture produced no coverage")
	}
	if len(inline) != len(shared) {
		t.Fatalf("hex stat count differs: inline=%d shared=%d", len(inline), len(shared))
	}
	// Compare keyed by (HexID, Metric, Subject) so ordering differences
	// don't matter; assert each value (area, coverage) matches exactly.
	type key struct{ id, metric, subject string }
	inlineByKey := make(map[key]float64, len(inline))
	for _, s := range inline {
		inlineByKey[key{s.HexID, s.Metric, s.Subject}] = s.Value
	}
	for _, s := range shared {
		want, ok := inlineByKey[key{s.HexID, s.Metric, s.Subject}]
		if !ok {
			t.Errorf("shared run produced hex %s %s (%s) absent from inline run", s.HexID, s.Metric, s.Subject)
			continue
		}
		if want != s.Value {
			t.Errorf("hex %s %s (%s): inline %v != shared %v", s.HexID, s.Metric, s.Subject, want, s.Value)
		}
	}
}

// TestRunCompute_PavedShareIsAProportion: every stored paved_share row is in
// 0..1 and equals the hex's paved_area over its clipped area, so the stored
// value is the proportion the key names, not a percent.
func TestRunCompute_PavedShareIsAProportion(t *testing.T) {
	ctx := context.Background()
	features := []db.Feature{
		{ID: "r1", ResourceType: resource.TypeRoads, Tags: map[string]string{"highway": "primary", "lanes": "6"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7600,37.6850]]}`},
	}
	bstore := &dbtest.MockStore{GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil }}
	boundaryGJSON, bbox, proj, err := LoadBoundary(ctx, bstore, testCity, io.Discard)
	if err != nil {
		t.Fatalf("LoadBoundary: %v", err)
	}
	grid := BuildClippedHexGrid(ctx, testCfg, testCity, proj, bbox, boundaryGJSON, io.Discard)
	hexArea := make(map[string]float64, len(grid))
	for _, h := range grid {
		hexArea[h.ID] = h.Geom.Area()
	}
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) { return features, nil },
	}
	ios, _, _, _ := iostreams.Test()
	opts := &Options{
		IO:           ios,
		UnitSystem:   func() units.System { return units.Metric },
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return testCity, nil },
		Config:       func() (*config.Config, error) { return testCfg, nil },
		ResourceType: &resource.Pavement{},
		PrebuiltGrid: grid,
	}
	if err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts); err != nil {
		t.Fatalf("doCompute: %v", err)
	}
	type key struct{ id, subject string }
	area := map[key]float64{}
	for _, m := range store.HexMetrics {
		if m.Metric == metric.PavedArea {
			area[key{m.HexID, m.Subject}] = m.Value
		}
	}
	var n int
	for _, m := range store.HexMetrics {
		if m.Metric != metric.PavedShare {
			continue
		}
		n++
		if m.Value < 0 || m.Value > 1 {
			t.Errorf("hex %s (%s) paved_share = %v, outside 0..1", m.HexID, m.Subject, m.Value)
		}
		a, ok := area[key{m.HexID, m.Subject}]
		if !ok || hexArea[m.HexID] <= 0 {
			t.Errorf("hex %s (%s) has a share but no area or hex", m.HexID, m.Subject)
			continue
		}
		if want := a / hexArea[m.HexID]; math.Abs(m.Value-want) > 1e-9 {
			t.Errorf("hex %s (%s) paved_share = %v, want paved_area/hex area %v", m.HexID, m.Subject, m.Value, want)
		}
	}
	if n == 0 {
		t.Fatal("no paved_share rows stored; the fixture covers no hex")
	}
}

// TestRunCompute_SaveHexStatsErrorFatal: per-hex
// stats are the primary output, so a SaveHexStats failure must make compute
// exit non-zero rather than warn-and-continue.
func TestRunCompute_SaveHexStatsErrorFatal(t *testing.T) {
	saveErr := errors.New("disk full")
	store := &dbtest.MockStore{
		GetBoundaryFunc:    func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc:   roadsFeaturesFunc,
		SaveHexMetricsFunc: func(_ context.Context, _ int64, _ []db.HexMetric) error { return saveErr },
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected non-nil error when SaveHexStats fails")
	}
	if !errors.Is(err, saveErr) {
		t.Errorf("expected wrapped SaveHexStats error, got: %v", err)
	}
}

// TestRunCompute_MaintainedOnlySuppressesAllCohortBreakdown pins:
// With --maintained-only, the all-jurisdiction "Cohort
// breakdown" summary must be suppressed to match the gated printResults.
func TestRunCompute_MaintainedOnlySuppressesAllCohortBreakdown(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--maintained-only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v", err)
	}
	out := stdout.String()
	// Each result block's header names the city. The fixture has one city
	// road and one state road, so an all-jurisdiction block would show both:
	// with --maintained-only every block is the city's, a results block and
	// a cohort breakdown, and neither counts the state road.
	slug := testCity.Slug()
	blocks, stray := resultBlocks(out, slug)
	if len(blocks) != 2 || len(stray) > 0 {
		t.Fatalf("got %d blocks (stray %q), want the city's results and cohort breakdown:\n%s", len(blocks), stray, out)
	}
	cityTotal, ok, _ := results.LoadTotal(context.Background(), store, resource.TypeRoads.With(resource.ScopeCity))
	allTotal, okAll, _ := results.LoadTotal(context.Background(), store, resource.TypeRoads)
	if !ok || !okAll || cityTotal.Area >= allTotal.Area {
		t.Fatalf("fixture: want a city total below the all total, got %+v and %+v", cityTotal, allTotal)
	}
	if all := units.FormatArea(allTotal.Area, units.Metric); strings.Contains(out, all) {
		t.Errorf("--maintained-only should suppress the all-jurisdiction blocks, but stdout shows their area %s:\n%s", all, out)
	}
	// docs/upgrading.md shows the results header's shape verbatim.
	res := blockLabeled(t, out, slug, "roads Results (city only)")
	if city := units.FormatArea(cityTotal.Area, units.Metric); !strings.Contains(strings.Join(res.lines, "\n"), city) {
		t.Errorf("city results block lacks the city area %s:\n%s", city, out)
	}
}

// TestRunCompute_NoFeaturesDropsThePart pins the empty-part rule under
// runs and snapshots: when loadResourceFeatures reports no features, no run is
// created, and the only thing committed is the removal of the resource's part
// (a no-op in the store when the city never had it), so coverage from
// features that are gone stops being published.
func TestRunCompute_NoFeaturesDropsThePart(t *testing.T) {
	runCreated := false
	var committed map[string]int64
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) { return nil, nil },
		CreateRunFunc: func(_ context.Context, producer, hash string, _ map[string]string) (*db.Run, error) {
			runCreated = true
			return &db.Run{ID: 1, Producer: producer, ConfigHash: hash}, nil
		},
		CommitLatestFunc: func(_ context.Context, _ string, parts map[string]int64) error {
			committed = parts
			return nil
		},
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("expected ErrNoResults, got: %v", err)
	}
	if runCreated {
		t.Error("no-features path created a run")
	}
	want := map[string]int64{"roads": 0}
	if !maps.Equal(committed, want) {
		t.Errorf("committed %v, want only the drop %v", committed, want)
	}
}

// TestRunCompute_InvalidBoundaryWarns: when the
// stored boundary fails to parse into a projected geometry, compute must warn
// (naming the city) instead of silently computing over the full bbox.
func TestRunCompute_InvalidBoundaryWarns(t *testing.T) {
	// A GeoJSON whose coordinates span a valid bbox (so BBoxFromGeoJSON
	// succeeds and loadBoundary passes) but whose only polygon is a
	// zero-area collinear ring, so GeoJSONToProjectedGeometry yields "no
	// valid polygons" at the clip stage.
	const badGeom = `{"type":"MultiPolygon","coordinates":[[[[-121.84,37.64],[-121.68,37.72],[-121.84,37.64]]]]}`
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return badGeom, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
	}
	ios, _, _, errBuf := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v", err)
	}
	stderr := errBuf.String()
	// "warning: " is the prefix every warning carries (internal/warn).
	if !strings.Contains(stderr, "warning: ") || !strings.Contains(stderr, testCity.Name) {
		t.Errorf("expected a boundary warning naming the city, got stderr:\n%s", stderr)
	}
}

// TestRunCompute_CanceledDoesNotSave: a canceled
// context must make the compute path return context.Canceled and must NOT
// persist truncated hex stats / compute results under a complete-looking
// snapshot. geo.ParallelMap returns partial results with no error on cancel,
// so the ctx.Err() guard between compute and persistence is the load-bearing
// fix under test.
func TestRunCompute_CanceledDoesNotSave(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
	}
	ios, _, _, _ := iostreams.Test()
	opts := &Options{
		IO:           ios,
		UnitSystem:   func() units.System { return units.Metric },
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return testCity, nil },
		Config:       func() (*config.Config, error) { return testCfg, nil },
		ResourceType: &resource.Pavement{},
	}

	// Pre-canceled context: ParallelMap dispatches nothing and returns partial
	// (empty) results, then the ctx.Err() guard must fire before any save.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if len(store.HexMetrics) > 0 {
		t.Error("hex rows were saved on a canceled run; truncated stats must not be persisted")
	}
	if len(store.CityMetrics) > 0 {
		t.Error("city totals were saved on a canceled run; partial results must not be persisted")
	}
}

// TestRunCompute_CanceledDiscardsRun pins the cancellation cleanup: when a
// run has already been created and the compute is then canceled, doCompute
// discards that run and commits no snapshot, while still returning
// context.Canceled. The discard must use a fresh (non-canceled) context so it
// isn't itself canceled.
func TestRunCompute_CanceledDiscardsRun(t *testing.T) {
	const runID = int64(42)
	var discardedID int64
	var discardCtxErr error
	committed := false
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
		CreateRunFunc: func(_ context.Context, producer, hash string, _ map[string]string) (*db.Run, error) {
			return &db.Run{ID: runID, Producer: producer, ConfigHash: hash}, nil
		},
		DiscardRunFunc: func(ctx context.Context, id int64) error {
			discardedID = id
			discardCtxErr = ctx.Err()
			return nil
		},
		CommitLatestFunc: func(context.Context, string, map[string]int64) error {
			committed = true
			return nil
		},
	}
	ios, _, _, _ := iostreams.Test()
	opts := &Options{
		IO:           ios,
		UnitSystem:   func() units.System { return units.Metric },
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return testCity, nil },
		Config:       func() (*config.Config, error) { return testCfg, nil },
		ResourceType: &resource.Pavement{},
	}

	// Pre-canceled context: CreateRun (mock ignores ctx) still returns a run,
	// then the ctx.Err() guard fires before any save, and the draft must
	// discard the run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := doCompute(ctx, io.Discard, io.Discard, io.Discard, tui.NoopNotifier{}, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if discardedID != runID {
		t.Errorf("expected DiscardRun(%d) on cancellation, got id %d", runID, discardedID)
	}
	if discardCtxErr != nil {
		t.Errorf("DiscardRun must run on a fresh (non-canceled) context, got ctx.Err() = %v", discardCtxErr)
	}
	if committed {
		t.Error("a canceled compute committed a snapshot")
	}
}

var errDBDown = errors.New("db connection failed")

func TestRunCompute_DBError(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
		CurrentCity: func() (*config.CityConfig, error) {
			return testCity, nil
		},
		CityDB: func() (db.Store, error) {
			return nil, errDBDown
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for DB failure")
	}
	if !errors.Is(err, errDBDown) {
		t.Errorf("expected the database open error, got: %s", err.Error())
	}
}

// TestRunCompute_ParkingEstimatesSpaces pins the parking-space estimate end to
// end through the compute command: two lots with capacity tags (40 and 25)
// produce a city total of 65 on both the all-jurisdiction and city rows, and
// the per-hex space metrics (one key per scope, under the same run the
// coverage rows carry) apportion the same 65 across the hexes
// the lots touch. Lots are small squares inside the test boundary.
func TestRunCompute_ParkingEstimatesSpaces(t *testing.T) {
	lot := func(id, capacity string, lon, lat float64) db.Feature {
		d := 0.0004
		return db.Feature{
			ID:           id,
			ResourceType: resource.TypeParking,
			Tags:         map[string]string{"amenity": "parking", "capacity": capacity},
			GeometryJSON: fmt.Sprintf(`{"type":"Polygon","coordinates":[[[%f,%f],[%f,%f],[%f,%f],[%f,%f],[%f,%f]]]}`,
				lon, lat, lon+d, lat, lon+d, lat+d, lon, lat+d, lon, lat),
		}
	}
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{
				lot("lotA", "40", -121.7700, 37.6800),
				lot("lotB", "25", -121.7650, 37.6830),
			}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Parking{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	parkingCity := resource.TypeParking.With(resource.ScopeCity)
	totals, err := results.LoadTotals(ctx, store, []resource.Type{resource.TypeParking, parkingCity})
	if err != nil || len(totals) != 2 {
		t.Fatalf("expected all + city totals, got %+v (%v)", totals, err)
	}
	for rt, r := range totals {
		if r.Spaces != 65 {
			t.Errorf("%s Spaces = %d, want 65 (capacity 40 + 25)", rt, r.Spaces)
		}
	}
	// Both passes write per-hex spaces; each must apportion the full 65 and
	// sit under the same run as the coverage rows export joins them to.
	var coverageRun int64
	spaces := map[string][]db.HexMetric{}
	for _, m := range store.HexMetrics {
		switch m.Metric {
		case metric.PavedArea:
			coverageRun = m.RunID
		case metric.ParkingSpaces:
			spaces[m.Subject] = append(spaces[m.Subject], m)
		}
	}
	if coverageRun == 0 {
		t.Fatal("expected per-hex coverage rows")
	}
	for _, subject := range []string{"parking", "parking:city"} {
		rows, ok := spaces[subject]
		if !ok {
			t.Errorf("no %s parking spaces saved; got %v", subject, slices.Collect(maps.Keys(spaces)))
			continue
		}
		var sum float64
		for _, r := range rows {
			if r.Value <= 0 {
				t.Errorf("%s stored a non-positive row %+v", subject, r)
			}
			if r.RunID != coverageRun {
				t.Errorf("%s spaces run = %d, want the coverage run %d", subject, r.RunID, coverageRun)
			}
			sum += r.Value
		}
		if math.Abs(sum-65) > 0.01 {
			t.Errorf("%s apportions %v spaces, want 65", subject, sum)
		}
	}
	if len(spaces) != 2 {
		t.Errorf("expected exactly the two parking space subjects, got %v", slices.Collect(maps.Keys(spaces)))
	}
	// The report shows the estimate: both results blocks carry a line with
	// the 65 beyond the feature count and the three area lines a resource
	// without spaces prints (TestRunCompute_RoadsCarryNoSpaces).
	for _, label := range []string{"parking Results (all)", "parking Results (city only)"} {
		b := blockLabeled(t, stdout.String(), testCity.Slug(), label)
		if len(b.lines) != resultLinesWithoutSpaces+1 || !regexp.MustCompile(`\b65\b`).MatchString(b.lines[len(b.lines)-1]) {
			t.Errorf("%s block should end with a line reporting 65 spaces, got %q", label, b.lines)
		}
	}
}

// resultLinesWithoutSpaces is the size of a results block for a resource
// with no space estimate: the feature count and the area in three units.
const resultLinesWithoutSpaces = 4

// TestRunCompute_RoadsCarryNoSpaces: the estimate is a parking concept, so a
// roads compute writes no spaces anywhere and prints no Spaces line.
func TestRunCompute_RoadsCarryNoSpaces(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{{
				ID: "r1", ResourceType: resource.TypeRoads,
				Tags:         map[string]string{"highway": "residential", "capacity": "99"},
				GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
			}}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(store.CityMetrics) == 0 {
		t.Fatal("roads compute saved no city totals")
	}
	for _, m := range store.CityMetrics {
		if m.Metric == metric.ParkingSpaces {
			t.Errorf("roads compute saved a city parking_spaces row %+v", m)
		}
	}
	for _, m := range store.HexMetrics {
		if m.Metric == metric.ParkingSpaces {
			t.Errorf("roads compute saved a per-hex parking_spaces row %+v", m)
		}
	}
	// The report has no spaces line either: the block is the feature count
	// and the three area lines, and the tagged capacity appears nowhere.
	b := blockLabeled(t, stdout.String(), testCity.Slug(), "roads Results (all)")
	if len(b.lines) != resultLinesWithoutSpaces || regexp.MustCompile(`\b99\b`).MatchString(stdout.String()) {
		t.Errorf("roads output must not report spaces:\n%s", stdout.String())
	}
}

// TestRunCompute_SaveSpaceMetricsErrorFatal: a failed per-hex save (coverage
// and spaces go in one save) fails the parking compute, rather than leaving
// coverage published without its spaces.
func TestRunCompute_SaveSpaceMetricsErrorFatal(t *testing.T) {
	saveErr := errors.New("disk full")
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{{
				ID: "lot", ResourceType: resource.TypeParking,
				Tags:         map[string]string{"amenity": "parking", "capacity": "40"},
				GeometryJSON: `{"type":"Polygon","coordinates":[[[-121.7700,37.6800],[-121.7696,37.6800],[-121.7696,37.6804],[-121.7700,37.6804],[-121.7700,37.6800]]]}`,
			}}, nil
		},
		SaveHexMetricsFunc: func(_ context.Context, _ int64, _ []db.HexMetric) error { return saveErr },
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Parking{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, saveErr) {
		t.Errorf("expected wrapped SaveHexMetrics error, got: %v", err)
	}
}

// TestRunCompute_CommitsOnlyItsOwnPart: a standalone resource compute
// commits ONE update of the latest compute, swapping in exactly its own
// part, under the run its rows were saved with. No forecast is stored or
// rerun: every reader simulates the forecast from the compute's cohort stats
// on demand.
func TestRunCompute_CommitsOnlyItsOwnPart(t *testing.T) {
	producers := map[int64]string{}
	var next int64
	var commits []map[string]int64
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
		CreateRunFunc: func(_ context.Context, producer, hash string, _ map[string]string) (*db.Run, error) {
			next++
			producers[next] = producer
			return &db.Run{ID: next, Producer: producer, ConfigHash: hash}, nil
		},
		CommitLatestFunc: func(_ context.Context, _ string, parts map[string]int64) error {
			commits = append(commits, parts)
			return nil
		},
	}
	ios, _, _, stderr := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v\nstderr:\n%s", err, stderr.String())
	}

	if len(commits) != 1 {
		t.Fatalf("expected exactly one commit of the latest compute, got %d: %v", len(commits), commits)
	}
	parts := commits[0]
	roadsRun := parts[string(resource.TypeRoads)]
	if want := map[string]int64{string(resource.TypeRoads): roadsRun}; roadsRun == 0 || !maps.Equal(parts, want) {
		t.Fatalf("committed parts = %v, want exactly roads", parts)
	}
	if len(producers) != 1 || producers[roadsRun] != string(resource.TypeRoads) {
		t.Errorf("runs created = %v, want only the roads run", producers)
	}
	if tot, _, _ := results.LoadTotal(context.Background(), store, resource.TypeRoads); tot.RunID != roadsRun {
		t.Errorf("roads totals saved under run %d, committed roads run is %d", tot.RunID, roadsRun)
	}
}

// TestRunCompute_RecordsInputs: the roads run records the digests of the
// boundary and the roads it read, and is committed into the latest compute
// under the city's hash.
func TestRunCompute_RecordsInputs(t *testing.T) {
	var hash string
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: roadsFeaturesFunc,
		Digests:          map[string]string{"boundary": "b", "roads": "r", "parking": "p"},
		CommitLatestFunc: func(_ context.Context, h string, _ map[string]int64) error {
			hash = h
			return nil
		},
	}
	ios, _, _, stderr := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v\nstderr:\n%s", err, stderr.String())
	}
	want := map[string]string{"boundary": "b", "roads": "r"}
	if len(store.RunInputs) != 1 || !maps.Equal(store.RunInputs[1], want) {
		t.Errorf("run inputs = %v, want run 1 with %v", store.RunInputs, want)
	}
	if want := testCfg.CityHash(testCity); hash != want {
		t.Errorf("committed under hash %q, want %q", hash, want)
	}
}

// TestRunCompute_StoresRoadDecayBlend: a roads compute stores, per
// city-jurisdiction road hex, the default-rate blend of its road classes'
// decay rates (metric.DefaultDecayRate) under roads:city, in the same
// run as the coverage, on exactly the hexes roads:city covers. Two parallel
// roads of different classes share their hexes, so those hexes blend strictly
// between the two class rates; the exact weighting is pinned by
// resource.TestDefaultDecayBlends.
func TestRunCompute_StoresRoadDecayBlend(t *testing.T) {
	road := func(id, highway string, lat float64) db.Feature {
		return db.Feature{
			ID: id, ResourceType: resource.TypeRoads,
			Tags:         map[string]string{"highway": highway},
			GeometryJSON: fmt.Sprintf(`{"type":"LineString","coordinates":[[-121.7800,%f],[-121.7600,%f]]}`, lat, lat),
		}
	}
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{road("p", "primary", 37.6800), road("r", "residential", 37.6802)}, nil
		},
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}
	cmd := NewCmdCompute(f, &resource.Pavement{}, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	primary, residential := forecast.DecayRateForClass("primary"), forecast.DecayRateForClass("residential")
	covered := map[string]int64{}
	k0 := map[string]float64{}
	for _, m := range store.HexMetrics {
		switch {
		case m.Metric == metric.PavedArea && m.Subject == "roads:city":
			covered[m.HexID] = m.RunID
		case m.Metric == metric.DefaultDecayRate:
			if m.Subject != "roads:city" {
				t.Errorf("decay blend stored under %q, want roads:city only", m.Subject)
			}
			k0[m.HexID] = m.Value
			if run, ok := covered[m.HexID]; ok && run != m.RunID {
				t.Errorf("hex %s decay blend run %d, coverage run %d", m.HexID, m.RunID, run)
			}
		}
	}
	if len(covered) == 0 {
		t.Fatal("no roads:city coverage stored")
	}
	if !slices.Equal(slices.Sorted(maps.Keys(k0)), slices.Sorted(maps.Keys(covered))) {
		t.Errorf("decay blend hexes %v, want the roads:city hexes %v", slices.Sorted(maps.Keys(k0)), slices.Sorted(maps.Keys(covered)))
	}
	mixed := 0
	for id, k := range k0 {
		switch {
		case k > primary && k < residential:
			mixed++
		case k != primary && k != residential:
			t.Errorf("hex %s k0 = %v, outside [%v, %v]", id, k, primary, residential)
		}
	}
	if mixed == 0 {
		t.Errorf("no hex blends the two classes: %v", k0)
	}
}

// TestNewCmdCompute_CityOnlyIsADeprecatedAlias pins --city-only as a hidden,
// deprecated spelling of --maintained-only that sets the same option.
func TestNewCmdCompute_CityOnlyIsADeprecatedAlias(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	var got *Options
	cmd := NewCmdCompute(&cmdutil.Factory{IOStreams: ios}, &resource.Pavement{}, func(_ context.Context, o *Options) error {
		got = o
		return nil
	})
	cmd.SetArgs([]string{"--city-only"})
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute --city-only: %v", err)
	}
	if !got.MaintainedOnly {
		t.Error("--city-only did not set MaintainedOnly")
	}
	if fl := cmd.Flags().Lookup("city-only"); fl.Deprecated == "" || !fl.Hidden {
		t.Errorf("--city-only: deprecated %q, hidden %v; want both", fl.Deprecated, fl.Hidden)
	}
}
