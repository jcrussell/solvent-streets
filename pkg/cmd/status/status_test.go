package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

var rtRoads = resource.TypeRoads

// eachResourceTotals is a city-total fixture giving every pavement resource
// 30000 m².
func eachResourceTotals() []db.CityMetric {
	var rows []db.CityMetric
	for _, t := range resource.AllTypes(resource.ScopeAll) {
		rows = append(rows, results.TotalRows(t, results.Totals{Area: 30000})...)
	}
	return rows
}

func TestNewCmdStatus_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, UnitSystem: func() units.System { return units.Imperial }}
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdStatus(f, rt, func(_ context.Context, opts *Options) error {
		called = true
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

func TestRunStatus_SingleResource(t *testing.T) {
	now := time.Now()
	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			if rt == rtRoads {
				return &db.StatusInfo{
					ResourceType: rtRoads,
					FeatureCount: 42,
					LastIngestAt: &now,
				}, nil
			}
			return &db.StatusInfo{ResourceType: rt}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		CityDB: func() (db.Store, error) {
			return store, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdStatus(f, rt, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "roads") {
		t.Errorf("expected roads in output, got: %s", output)
	}
	if !strings.Contains(output, "42") {
		t.Errorf("expected feature count 42 in output, got: %s", output)
	}
}

func TestRunStatus_AllResources(t *testing.T) {
	rtParking := resource.TypeParking
	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			switch rt { //nolint:exhaustive // test fixture: only roads/parking matter; default covers the rest
			case rtRoads:
				return &db.StatusInfo{ResourceType: rtRoads, FeatureCount: 10}, nil
			case rtParking:
				return &db.StatusInfo{ResourceType: rtParking, FeatureCount: 5}, nil
			default:
				return &db.StatusInfo{ResourceType: rt}, nil
			}
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		CityDB: func() (db.Store, error) {
			return store, nil
		},
	}

	// nil ResourceType means show all
	cmd := NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "roads") || !strings.Contains(output, "parking") {
		t.Errorf("expected both resource types in output, got: %s", output)
	}
}

func TestRunStatus_CitySummary(t *testing.T) {
	// ~1km x 1km boundary polygon
	boundaryGJSON := `{"type":"Polygon","coordinates":[[[-97.745,30.265],[-97.7346,30.265],[-97.7346,30.274],[-97.745,30.274],[-97.745,30.265]]]}`

	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			if rt == rtRoads {
				return &db.StatusInfo{ResourceType: rtRoads, FeatureCount: 100}, nil
			}
			return &db.StatusInfo{ResourceType: rt}, nil
		},
		CityMetrics: results.TotalRows(rtRoads, results.Totals{Area: 46452, Features: 100}),
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			return boundaryGJSON, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	ios.SetTTY(true)
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		CityDB: func() (db.Store, error) {
			return store, nil
		},
	}

	cmd := NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "City Summary") {
		t.Errorf("expected City Summary on stdout, got: %s", output)
	}
	if !strings.Contains(output, "City Area:") {
		t.Errorf("expected City Area on stdout, got: %s", output)
	}
	// No combined compute row is stubbed here, so the paved figure is the
	// per-resource sum and carries the double-count asterisk.
	if !strings.Contains(output, "Paved Area*:") {
		t.Errorf("expected Paved Area (fallback, asterisked) on stdout, got: %s", output)
	}
	if !strings.Contains(output, "% Paved*:") {
		t.Errorf("expected %% Paved (fallback, asterisked) on stdout, got: %s", output)
	}
}

// TestRunStatus_ComputeReadErrorFails: a failed read of the compute totals
// fails the command instead of printing rows that look never computed.
func TestRunStatus_ComputeReadErrorFails(t *testing.T) {
	locked := errors.New("database is locked")
	store := &dbtest.MockStore{
		ListCityMetricsFunc: func(context.Context, []string) ([]db.CityMetric, error) { return nil, locked },
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Metric },
		CityDB:     func() (db.Store, error) { return store, nil },
	}
	cmd := NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{"--json", "resource_type,area"})
	if err := cmd.Execute(); !errors.Is(err, locked) {
		t.Errorf("err = %v, want the compute read error", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no rows", stdout.String())
	}
}

// TestRunStatus_CitySummary_PrefersCombined: when the combined
// (de-duplicated) compute row is present, the City Summary paved figure comes
// from it — NOT the per-resource sum, which double-counts buffer overlap.
func TestRunStatus_CitySummary_PrefersCombined(t *testing.T) {
	boundaryGJSON := `{"type":"Polygon","coordinates":[[[-97.745,30.265],[-97.7346,30.265],[-97.7346,30.274],[-97.745,30.274],[-97.745,30.265]]]}`
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return boundaryGJSON, nil },
		// Per-resource areas sum to 90000; the combined row says 60000.
		CityMetrics: append(eachResourceTotals(), results.TotalRows(resource.CombinedAll, results.Totals{Area: 60000})...),
	}
	ios, _, stdout, _ := iostreams.Test()
	ios.SetTTY(true)
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Metric },
		CityDB:     func() (db.Store, error) { return store, nil },
	}
	cmd := NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	// The combined figure (60000 m2 = 0.06 km2 = 6 ha) is used, and no
	// double-count asterisk footnote appears.
	if strings.Contains(out, "*") {
		t.Errorf("combined row present: should not emit double-count footnote, got: %s", out)
	}
	if !strings.Contains(out, "0.06") {
		t.Errorf("expected combined paved area (0.06 km2) in output, got: %s", out)
	}
}

// TestRunStatus_CitySummary_FallsBackToSum pins the fallback: with no combined
// row, the paved figure is the per-resource sum and is flagged with an asterisk
// footnote so it isn't mistaken for the de-duplicated union.
func TestRunStatus_CitySummary_FallsBackToSum(t *testing.T) {
	boundaryGJSON := `{"type":"Polygon","coordinates":[[[-97.745,30.265],[-97.7346,30.265],[-97.7346,30.274],[-97.745,30.274],[-97.745,30.265]]]}`
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return boundaryGJSON, nil },
		// No combined rows in the mock → fallback.
		CityMetrics: eachResourceTotals(),
	}
	ios, _, stdout, _ := iostreams.Test()
	ios.SetTTY(true)
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Metric },
		CityDB:     func() (db.Store, error) { return store, nil },
	}
	cmd := NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "*") {
		t.Errorf("no combined row: expected double-count footnote asterisk, got: %s", out)
	}
}

// TestRunStatus_CitySummary_SingleResourceIgnoresCombined pins the
// single-resource path of TestRunStatus_CitySummary_PrefersCombined: `pvmt <res> status` shows only that
// resource's area (no cross-resource overlap to de-duplicate), so it must NOT
// pull the combined union row (which would mismatch the single-row table) and
// must NOT emit the double-count footnote.
func TestRunStatus_CitySummary_SingleResourceIgnoresCombined(t *testing.T) {
	boundaryGJSON := `{"type":"Polygon","coordinates":[[[-97.745,30.265],[-97.7346,30.265],[-97.7346,30.274],[-97.745,30.274],[-97.745,30.265]]]}`
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return boundaryGJSON, nil },
		// A combined row exists but must be ignored in single-resource mode.
		CityMetrics: append(eachResourceTotals(), results.TotalRows(resource.CombinedAll, results.Totals{Area: 999999})...),
	}
	ios, _, stdout, _ := iostreams.Test()
	ios.SetTTY(true)
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Metric },
		CityDB:     func() (db.Store, error) { return store, nil },
	}
	// Non-nil resource source → single-resource status.
	cmd := NewCmdStatus(f, &resource.Pavement{}, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if strings.Contains(out, "*") {
		t.Errorf("single-resource: should not emit double-count footnote, got: %s", out)
	}
	// The single resource's own area (30000 m2 = 0.03 km2 = 3 ha), not the
	// combined 999999 union (which would render ~100 ha / 1 sq km of paved).
	if !strings.Contains(out, "Paved Area:   3.00 ha") {
		t.Errorf("expected single-resource paved area (3.00 ha), not the combined union, got: %s", out)
	}
}

// TestStatusRow_ExportData_AllFieldsPopulated guards S2: a typo in
// statusRow.ExportData's switch silently drops that field. This asserts the
// full statusFields list round-trips for a fully populated row.
func TestStatusRow_ExportData_AllFieldsPopulated(t *testing.T) {
	r := statusRow{
		City:          "oakland-ca",
		ResourceType:  "parking",
		FeatureCount:  42,
		LastIngestAt:  "2026-04-18T00:00:00Z",
		LastComputeAt: "2026-04-18T01:00:00Z",
		Area:          123.4,
		ParkingSpaces: 7,
		State:         "stale",
		StaleInputs:   []string{"parking"},
	}
	out := r.ExportData(statusFields)
	if len(out) != len(statusFields) {
		t.Fatalf("want %d keys, got %d: %v", len(statusFields), len(out), out)
	}
	for _, f := range statusFields {
		if _, ok := out[f]; !ok {
			t.Errorf("missing field %q", f)
		}
	}
	if out["resource_type"] != "parking" || out["feature_count"] != 42 || out["area"] != 123.4 || out["parking_spaces"] != 7 {
		t.Errorf("unexpected values: %+v", out)
	}
}

// TestStatusRow_ExportData_SubsetFields verifies that requesting a
// subset returns only those keys — the field-filter contract the --json
// flag depends on.
func TestStatusRow_ExportData_SubsetFields(t *testing.T) {
	r := statusRow{ResourceType: "roads", FeatureCount: 42, Area: 1}
	out := r.ExportData([]string{"resource_type"})
	if len(out) != 1 {
		t.Fatalf("want 1 key, got %d: %v", len(out), out)
	}
	if out["resource_type"] != "roads" {
		t.Errorf("unexpected: %+v", out)
	}
}

func TestRunStatus_NonTTY_TabSeparated(t *testing.T) {
	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			if rt == rtRoads {
				return &db.StatusInfo{ResourceType: rtRoads, FeatureCount: 7}, nil
			}
			return &db.StatusInfo{ResourceType: rt}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	// Test() returns isTTY=false by default
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		CityDB: func() (db.Store, error) {
			return store, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdStatus(f, rt, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "\t") {
		t.Errorf("expected tab-separated output for non-TTY, got: %s", output)
	}
	if !strings.Contains(output, "roads") {
		t.Errorf("expected 'roads' in output, got: %s", output)
	}
}

// TestStatusRow_ExportData_OmitsNotApplicable: a resource never ingested or
// computed has no timestamps and no area, and a non-parking resource has no
// space estimate; those keys are omitted rather than emitted as "" or a 0
// sentinel.
func TestStatusRow_ExportData_OmitsNotApplicable(t *testing.T) {
	out := statusRow{ResourceType: "roads"}.ExportData(statusFields)
	for _, f := range []string{"last_ingest_at", "last_compute_at", "area", "parking_spaces"} {
		if v, ok := out[f]; ok {
			t.Errorf("%s = %v; want it omitted", f, v)
		}
	}
	if out["feature_count"] != 0 {
		t.Errorf("the feature count is always present, got %v", out)
	}
}

// TestStatusRow_ExportData_ComputedZeroAreaIsKept: once a resource has been
// computed its area is a real value, so a computed 0 (a city with no
// parking) is emitted, not mistaken for "never computed".
func TestStatusRow_ExportData_ComputedZeroAreaIsKept(t *testing.T) {
	out := statusRow{ResourceType: "parking", LastComputeAt: "2026-04-18T01:00:00Z"}.ExportData(statusFields)
	if v, ok := out["area"]; !ok || v != 0.0 {
		t.Errorf("area = %v (present %v), want a computed 0", v, ok)
	}
}

// TestRunStatus_StaleSnapshotsSayWhy: after a
// settings change, Last Compute reads through the new settings' (empty)
// latest compute and shows blank. Status must say why — with the shared hint —
// and mark which listed snapshots are current, rather than look
// never-computed.
func TestRunStatus_StaleSnapshotsSayWhy(t *testing.T) {
	cfg := &config.Config{}
	city := &config.CityConfig{Name: "Test City"}
	cur := cfg.CityHash(city)
	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			return &db.StatusInfo{ResourceType: rt}, nil
		},
		ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) {
			return []db.SnapshotCity{{ID: 7, ConfigHash: "0ld0ld0ld0ld0ld0", CreatedAt: time.Now()}}, nil
		},
	}
	ios, _, stdout, stderr := iostreams.Test()
	ios.SetTTY(true)
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Imperial },
		CityDB:      func() (db.Store, error) { return store, nil },
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
	}
	cmd := NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out := stderr.String()
	if want := "hint: " + cfg.StaleHint(city); !strings.Contains(out, want) {
		t.Errorf("stderr lacks the shared stale-snapshot hint %q:\n%s", want, out)
	}
	if out := stdout.String(); !strings.Contains(out, "0ld0ld0ld0ld0ld0") || strings.Contains(out, "(current settings)") {
		t.Errorf("snapshot history should list the old hash, unmarked:\n%s", out)
	}

	// Once computed under the current settings, and snapshotted: no hint, and
	// the snapshot is marked.
	store.LatestComputeFunc = func(context.Context, string) ([]db.LatestPart, error) {
		return []db.LatestPart{{Part: "roads", RunID: 1}}, nil
	}
	store.ListSnapshotsFunc = func(context.Context) ([]db.SnapshotCity, error) {
		return []db.SnapshotCity{
			{ID: 8, ConfigHash: cur, CreatedAt: time.Now()},
			{ID: 7, ConfigHash: "0ld0ld0ld0ld0ld0", CreatedAt: time.Now()},
		}, nil
	}
	stderr.Reset()
	stdout.Reset()
	cmd = NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out := stderr.String(); strings.Contains(out, "not under the current settings") {
		t.Errorf("hint printed although the city is computed under the current settings:\n%s", out)
	}
	if out := stdout.String(); !strings.Contains(out, cur+"  (published, current settings)") {
		t.Errorf("newest snapshot not marked published and current:\n%s", out)
	}
}

// TestRunStatus_ConfigErrorsAreNotDatabaseErrors: a
// missing config or an unknown --city is reported as such, before the
// database is opened, rather than prefixed "database:".
func TestRunStatus_ConfigErrorsAreNotDatabaseErrors(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	dbOpened := false
	opts := &Options{
		IO:     ios,
		CityDB: func() (db.Store, error) { dbOpened = true; return &dbtest.MockStore{}, nil },
		Config: func() (*config.Config, error) { return &config.Config{}, nil },
		CurrentCity: func() (*config.CityConfig, error) {
			return nil, cmdutil.FlagErrorf(`--city "nowhere" matches no [[cities]] entry by name or slug`)
		},
		UnitSystem: func() units.System { return units.Metric },
	}
	err := runStatus(context.Background(), opts)
	var fe *cmdutil.FlagError
	if !errors.As(err, &fe) || err.Error() != `--city "nowhere" matches no [[cities]] entry by name or slug` {
		t.Errorf("err = %v, want the unwrapped --city usage error", err)
	}
	if dbOpened {
		t.Error("the database was opened before the city resolved")
	}
}

// TestRunStatus_PartStates: after a roads re-ingest
// and a roads compute alone, status shows each part's state from the latest
// compute (roads and parking current, the combined total stale, sidewalks
// not computed), footnotes the stale combined Paved Area instead
// of passing it off as accurate, says what is not yet snapshotted, and marks
// the published snapshot. --json carries state and stale_inputs.
func TestRunStatus_PartStates(t *testing.T) {
	cfg := &config.Config{}
	city := &config.CityConfig{Name: "Test City"}
	at := time.Date(2026, 11, 14, 10, 2, 0, 0, time.UTC)
	boundaryGJSON := `{"type":"Polygon","coordinates":[[[-97.745,30.265],[-97.7346,30.265],[-97.7346,30.274],[-97.745,30.274],[-97.745,30.265]]]}`
	latest := []db.LatestPart{
		{Part: "roads", RunID: 5, Inputs: map[string]string{db.InputBoundary: "b", "roads": "r2"}},
		{Part: "parking", RunID: 2, Inputs: map[string]string{db.InputBoundary: "b", "parking": "p"}},
		{Part: "combined", RunID: 3, Inputs: map[string]string{db.InputBoundary: "b", "roads": "r1", "parking": "p", "sidewalks": ""}},
	}
	store := &dbtest.MockStore{
		GetBoundaryFunc:   func(context.Context) (string, error) { return boundaryGJSON, nil },
		CityMetrics:       append(eachResourceTotals(), results.TotalRows(resource.CombinedAll, results.Totals{Area: 60000})...),
		LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) { return latest, nil },
		InputStatesFunc: func(context.Context) ([]db.InputState, error) {
			return []db.InputState{
				{Input: db.InputBoundary, Digest: "b"},
				{Input: "parking", Digest: "p"},
				{Input: "roads", Digest: "r2", IngestedAt: at},
			}, nil
		},
		ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) {
			return []db.SnapshotCity{{ID: 4, Name: "v4", ConfigHash: cfg.CityHash(city), CreatedAt: at, Parts: []db.SnapshotPart{
				{Part: "roads", RunID: 1}, {Part: "parking", RunID: 2}, {Part: "combined", RunID: 3},
			}}}, nil
		},
	}
	run := func(args ...string) string {
		t.Helper()
		ios, _, stdout, _ := iostreams.Test()
		ios.SetTTY(true)
		f := &cmdutil.Factory{
			IOStreams:   ios,
			UnitSystem:  func() units.System { return units.Metric },
			CityDB:      func() (db.Store, error) { return store, nil },
			Config:      func() (*config.Config, error) { return cfg, nil },
			CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		}
		cmd := NewCmdStatus(f, nil, nil)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return stdout.String()
	}

	table := run()
	for _, want := range []string{"State", "current", "not computed"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	for _, want := range []string{
		// Every section after the table names the city: they share stdout
		// with it, apart from the stderr city header.
		"=== test-city: Other parts ===",
		"combined       stale: roads re-ingested 2026-11-14 10:02 UTC",
		"census         not computed",
		"test-city: 1 part not yet snapshotted (roads); run 'pvmt snapshots create <name>' to publish",
		"=== test-city: City Summary ===",
		"=== test-city: Snapshots ===",
		"Paved Area*:",
		"* the combined total is stale (roads re-ingested 2026-11-14 10:02 UTC since it was computed)",
		"  v4  ",
		"(published, current settings)",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("stdout lacks %q:\n%s", want, table)
		}
	}

	out := run("--json", "resource_type,state,stale_inputs")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		want := "current"
		if r["resource_type"] == "sidewalks" {
			want = "not_computed"
		}
		if r["state"] != want {
			t.Errorf("%v state = %v, want %s", r["resource_type"], r["state"], want)
		}
		if _, ok := r["stale_inputs"]; ok {
			t.Errorf("%v has stale_inputs although it is not stale", r["resource_type"])
		}
	}
}

// TestRunStatus_StaleResourceRow: a resource row computed from inputs
// ingested again since reads stale, naming the input, in both outputs.
func TestRunStatus_StaleResourceRow(t *testing.T) {
	cfg := &config.Config{}
	city := &config.CityConfig{Name: "Test City"}
	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			return &db.StatusInfo{ResourceType: rt}, nil
		},
		LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) {
			return []db.LatestPart{{Part: "roads", RunID: 1, Inputs: map[string]string{db.InputBoundary: "b", "roads": "r1"}}}, nil
		},
		InputStatesFunc: func(context.Context) ([]db.InputState, error) {
			return []db.InputState{{Input: db.InputBoundary, Digest: "b"}, {Input: "roads", Digest: "r2"}}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
	}
	cmd := NewCmdStatus(f, &resource.Pavement{}, nil)
	cmd.SetArgs([]string{"--json", "resource_type,state,stale_inputs"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["state"] != "stale" || fmt.Sprint(rows[0]["stale_inputs"]) != "[roads]" {
		t.Errorf("--json = %v, want roads stale on its roads input", rows)
	}
}

// TestPartOf: a facility or census status reports the part computed from
// those features.
func TestPartOf(t *testing.T) {
	for in, want := range map[resource.Type]string{
		resource.TypeRoads:            "roads",
		resource.FacilityType("fire"): db.ServiceProducer("fire"),
		resource.TypeCensusBlocks:     db.ProducerCensus,
	} {
		if got := partOf(in); got != want {
			t.Errorf("partOf(%s) = %q, want %q", in, got, want)
		}
	}
}

// TestStatus_EveryConfiguredCity: without --city, status reports every
// configured city, each row naming its city, and --json writes them all as
// one document.
func TestStatus_EveryConfiguredCity(t *testing.T) {
	root, err := db.Open(context.Background(), filepath.Join(t.TempDir(), db.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := &config.Config{ConfigID: "test", Cities: []config.CityConfig{{Name: "Alpha, CA"}, {Name: "Beta, CA"}}}
	run := func(args ...string) (string, string) {
		t.Helper()
		ios, _, stdout, stderr := iostreams.Test()
		f := &cmdutil.Factory{
			IOStreams:   ios,
			RootDB:      func() (*db.RootStore, error) { return root, nil },
			UnitSystem:  func() units.System { return units.Metric },
			Config:      func() (*config.Config, error) { return cfg, nil },
			CurrentCity: func() (*config.CityConfig, error) { return &cfg.Cities[0], nil },
			CityFlagSet: func() bool { return false },
		}
		cmd := NewCmdStatus(f, &resource.Pavement{}, nil)
		cmd.SetArgs(args)
		cmd.SilenceUsage = true
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return stdout.String(), stderr.String()
	}

	table, chatter := run()
	for _, slug := range []string{"alpha-ca", "beta-ca"} {
		if !strings.Contains(table, slug+"\troads") {
			t.Errorf("table lacks a roads row for %s:\n%s", slug, table)
		}
	}
	if !strings.Contains(chatter, "=== Beta, CA ===") {
		t.Errorf("stderr lacks the per-city header:\n%s", chatter)
	}

	out, _ := run("--json", "city,resource_type")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(rows) != 2 || rows[0]["city"] != "alpha-ca" || rows[1]["city"] != "beta-ca" {
		t.Errorf("rows = %v, want one per city in config order", rows)
	}
}

// TestRunStatus_FacilityRowIsALayer: a status over a non-pavement input names
// it as a layer by its public name, never by its stored id, and so do its
// stale inputs.
func TestRunStatus_FacilityRowIsALayer(t *testing.T) {
	cfg := &config.Config{}
	city := &config.CityConfig{Name: "Test City"}
	fire := string(resource.FacilityType("fire"))
	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			return &db.StatusInfo{ResourceType: rt}, nil
		},
		LatestComputeFunc: func(context.Context, string) ([]db.LatestPart, error) {
			return []db.LatestPart{{Part: db.ServiceProducer("fire"), RunID: 1, Inputs: map[string]string{fire: "f1"}}}, nil
		},
		InputStatesFunc: func(context.Context) ([]db.InputState, error) {
			return []db.InputState{{Input: fire, Digest: "f2"}}, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
	}
	cmd := NewCmdStatus(f, &resource.Facility{Service: "fire"}, nil)
	cmd.SetArgs([]string{"--json", "layer,state,stale_inputs"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["layer"] != "fire_stations" || fmt.Sprint(rows[0]["stale_inputs"]) != "[fire_stations]" {
		t.Errorf("--json = %v, want layer fire_stations, stale on fire_stations", rows)
	}
	cmd = NewCmdStatus(f, &resource.Facility{Service: "fire"}, nil)
	cmd.SetArgs([]string{"--json", "resource_type"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Error("--json resource_type on a facility status was accepted, want an unknown-field error")
	}
}
