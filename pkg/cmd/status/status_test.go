package status

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

var rtRoads = resource.TypeRoads

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
				return &db.StatusInfo{
					ResourceType: rtRoads,
					FeatureCount: 100,
					TotalArea:    46452,
				}, nil
			}
			return &db.StatusInfo{ResourceType: rt}, nil
		},
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			return boundaryGJSON, nil
		},
	}
	ios, _, _, stderr := iostreams.Test()
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
	output := stderr.String()
	if !strings.Contains(output, "City Summary") {
		t.Errorf("expected City Summary in stderr, got: %s", output)
	}
	if !strings.Contains(output, "City Area:") {
		t.Errorf("expected City Area in stderr, got: %s", output)
	}
	// No combined compute row is stubbed here, so the paved figure is the
	// per-resource sum and carries the double-count asterisk (yvlv.37).
	if !strings.Contains(output, "Paved Area*:") {
		t.Errorf("expected Paved Area (fallback, asterisked) in stderr, got: %s", output)
	}
	if !strings.Contains(output, "% Paved*:") {
		t.Errorf("expected %% Paved (fallback, asterisked) in stderr, got: %s", output)
	}
}

// TestRunStatus_CitySummary_PrefersCombined pins yvlv.37: when the combined
// (de-duplicated) compute row is present, the City Summary paved figure comes
// from it — NOT the per-resource sum, which double-counts buffer overlap.
func TestRunStatus_CitySummary_PrefersCombined(t *testing.T) {
	boundaryGJSON := `{"type":"Polygon","coordinates":[[[-97.745,30.265],[-97.7346,30.265],[-97.7346,30.274],[-97.745,30.274],[-97.745,30.265]]]}`
	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			// Per-resource areas sum to 90000; the combined row says 60000.
			return &db.StatusInfo{ResourceType: rt, TotalArea: 30000}, nil
		},
		GetBoundaryFunc: func(_ context.Context) (string, error) { return boundaryGJSON, nil },
		LatestComputeResultFunc: func(_ context.Context, rt resource.Type) (*db.ComputeResult, error) {
			if rt == resource.CombinedAll {
				return &db.ComputeResult{ResourceType: rt, TotalArea: 60000}, nil
			}
			return nil, sql.ErrNoRows
		},
	}
	ios, _, _, stderr := iostreams.Test()
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
	out := stderr.String()
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
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			return &db.StatusInfo{ResourceType: rt, TotalArea: 30000}, nil
		},
		GetBoundaryFunc: func(_ context.Context) (string, error) { return boundaryGJSON, nil },
		// No LatestComputeResultFunc → mock returns sql.ErrNoRows → fallback.
	}
	ios, _, _, stderr := iostreams.Test()
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
	out := stderr.String()
	if !strings.Contains(out, "*") {
		t.Errorf("no combined row: expected double-count footnote asterisk, got: %s", out)
	}
}

// TestRunStatus_CitySummary_SingleResourceIgnoresCombined pins the
// single-resource path of yvlv.37: `pvmt <res> status` shows only that
// resource's area (no cross-resource overlap to de-duplicate), so it must NOT
// pull the combined union row (which would mismatch the single-row table) and
// must NOT emit the double-count footnote.
func TestRunStatus_CitySummary_SingleResourceIgnoresCombined(t *testing.T) {
	boundaryGJSON := `{"type":"Polygon","coordinates":[[[-97.745,30.265],[-97.7346,30.265],[-97.7346,30.274],[-97.745,30.274],[-97.745,30.265]]]}`
	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			return &db.StatusInfo{ResourceType: rt, TotalArea: 30000}, nil
		},
		GetBoundaryFunc: func(_ context.Context) (string, error) { return boundaryGJSON, nil },
		LatestComputeResultFunc: func(_ context.Context, rt resource.Type) (*db.ComputeResult, error) {
			// A combined row exists but must be ignored in single-resource mode.
			if rt == resource.CombinedAll {
				return &db.ComputeResult{ResourceType: rt, TotalArea: 999999}, nil
			}
			return nil, sql.ErrNoRows
		},
	}
	ios, _, _, stderr := iostreams.Test()
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
	out := stderr.String()
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
		ResourceType:    "parking",
		FeatureCount:    42,
		LastIngestAt:    "2026-04-18T00:00:00Z",
		LastComputeAt:   "2026-04-18T01:00:00Z",
		Area:            123.4,
		EstimatedSpaces: 7,
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
	if out["resource_type"] != "parking" || out["feature_count"] != 42 || out["area"] != 123.4 || out["estimated_spaces"] != 7 {
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
// sentinel (solvent-streets-ern4.9).
func TestStatusRow_ExportData_OmitsNotApplicable(t *testing.T) {
	out := statusRow{ResourceType: "roads"}.ExportData(statusFields)
	for _, f := range []string{"last_ingest_at", "last_compute_at", "area", "estimated_spaces"} {
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

// TestRunStatus_StaleSnapshotsSayWhy is solvent-streets-ern4.12 (b): after a
// settings change, Last Compute reads through the new settings' (missing)
// snapshot and shows blank. Status must say why — with export's hint — and mark
// which listed snapshots are current, rather than look never-computed.
func TestRunStatus_StaleSnapshotsSayWhy(t *testing.T) {
	cfg := &config.Config{}
	city := &config.CityConfig{Name: "Test City"}
	cur := cfg.CityHash(city)
	store := &dbtest.MockStore{
		StatsFunc: func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			return &db.StatusInfo{ResourceType: rt}, nil
		},
		ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) {
			return []db.Snapshot{{ID: 7, ConfigHash: "0ld0ld0ld0ld0ld0", CreatedAt: time.Now()}}, nil
		},
	}
	ios, _, _, stderr := iostreams.Test()
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
	if want := config.StaleSnapshotsHint("test-city", cur); !strings.Contains(out, want) {
		t.Errorf("stderr lacks the shared stale-snapshot hint %q:\n%s", want, out)
	}
	if !strings.Contains(out, "0ld0ld0ld0ld0ld0") || strings.Contains(out, "(current settings)") {
		t.Errorf("snapshot history should list the old hash, unmarked:\n%s", out)
	}

	// Once a snapshot under the current settings exists: no hint, and it is marked.
	store.ListSnapshotsFunc = func(context.Context) ([]db.Snapshot, error) {
		return []db.Snapshot{
			{ID: 8, ConfigHash: cur, CreatedAt: time.Now()},
			{ID: 7, ConfigHash: "0ld0ld0ld0ld0ld0", CreatedAt: time.Now()},
		}, nil
	}
	stderr.Reset()
	cmd = NewCmdStatus(f, nil, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	out = stderr.String()
	if strings.Contains(out, "not under the current settings") {
		t.Errorf("hint printed although a current snapshot exists:\n%s", out)
	}
	if !strings.Contains(out, cur+"  (current settings)") {
		t.Errorf("current snapshot not marked:\n%s", out)
	}
}
