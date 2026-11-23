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
	"github.com/jcrussell/solvent-streets/internal/geo"
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
	cityArea, err := geo.BoundaryArea(context.Background(), boundaryGJSON)
	if err != nil {
		t.Fatal(err)
	}
	const paved = 46452.0
	// The summary is a section of its own after the table, holding the
	// boundary's area and the paved figures computed from it.
	summary := sectionWith(statusSections(output), units.FormatAreaLarge(cityArea, units.Imperial))
	if summary == nil {
		t.Fatalf("no section on stdout holds the city area %s:\n%s", units.FormatAreaLarge(cityArea, units.Imperial), output)
	}
	// No combined compute row is stubbed here, so the paved figures are the
	// per-resource sum, and each one's label carries the double-count
	// asterisk.
	for _, value := range []string{
		units.FormatAreaLarge(paved, units.Imperial),
		fmt.Sprintf("%.1f%%", paved/cityArea*100),
	} {
		line := lineWith(summary.lines, value)
		if line == "" {
			t.Errorf("summary lacks the paved figure %s:\n%s", value, output)
			continue
		}
		if label, _, _ := strings.Cut(line, value); !strings.Contains(label, "*") {
			t.Errorf("fallback figure %s is not asterisked: %q", value, line)
		}
	}
}

// TestRunStatus_NoBoundaryNoSummary: without a stored boundary there is no
// city area to compare against, so no section follows the table.
func TestRunStatus_NoBoundaryNoSummary(t *testing.T) {
	store := &dbtest.MockStore{CityMetrics: eachResourceTotals()}
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
	if secs := statusSections(stdout.String()); len(secs) != 0 {
		t.Errorf("sections without a boundary: %+v", secs)
	}
}

// statusSection is one "=== [<slug>: ]<title> ===" block of status's stdout.
type statusSection struct {
	header string // the text between the markers
	lines  []string
}

// statusSections returns the sections that follow status's table, in order.
// Each section is headed "=== <slug>: <title> ===".
func statusSections(out string) []statusSection {
	var secs []statusSection
	for line := range strings.SplitSeq(out, "\n") {
		if h, ok := strings.CutPrefix(line, "=== "); ok && strings.HasSuffix(h, " ===") {
			secs = append(secs, statusSection{header: strings.TrimSuffix(h, " ===")})
			continue
		}
		if len(secs) > 0 && strings.TrimSpace(line) != "" {
			last := &secs[len(secs)-1]
			last.lines = append(last.lines, line)
		}
	}
	return secs
}

// sectionWith returns the first section with a line containing value, so a
// test finds a section by the data it holds, not by its title.
func sectionWith(secs []statusSection, value string) *statusSection {
	for i := range secs {
		if lineWith(secs[i].lines, value) != "" {
			return &secs[i]
		}
	}
	return nil
}

// lineWith returns the first line containing value, or "".
func lineWith(lines []string, value string) string {
	for _, l := range lines {
		if strings.Contains(l, value) {
			return l
		}
	}
	return ""
}

// rowFor returns the first line whose first field is key, or "".
func rowFor(lines []string, key string) string {
	for _, l := range lines {
		if f := strings.Fields(l); len(f) > 0 && f[0] == key {
			return l
		}
	}
	return ""
}

// snapshotMarks returns the marks the snapshot history puts after hash on
// its line ("(a, b)" is two), or nil when the line is unmarked. ok is false
// when no line lists hash.
func snapshotMarks(out, hash string) (marks []string, ok bool) {
	line := lineWith(strings.Split(out, "\n"), hash)
	if line == "" {
		return nil, false
	}
	_, rest, _ := strings.Cut(line, hash)
	rest = strings.Trim(strings.TrimSpace(rest), "()")
	if rest == "" {
		return nil, true
	}
	return strings.Split(rest, ", "), true
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
	if want := units.FormatAreaLarge(30000, units.Metric); !strings.Contains(out, want) {
		t.Errorf("expected single-resource paved area (%s), not the combined union, got: %s", want, out)
	}
	if union := units.FormatAreaLarge(999999, units.Metric); strings.Contains(out, union) {
		t.Errorf("single-resource summary shows the combined union %s: %s", union, out)
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
	// The only snapshot is the newest, so it carries that one mark and not
	// the current-settings one.
	newestMarks, ok := snapshotMarks(stdout.String(), "0ld0ld0ld0ld0ld0")
	if !ok || len(newestMarks) != 1 {
		t.Fatalf("snapshot history should list the old hash with one mark (newest), got %q:\n%s", newestMarks, stdout.String())
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
	if out := stderr.String(); strings.Contains(out, "hint:") {
		t.Errorf("hint printed although the city is computed under the current settings:\n%s", out)
	}
	// The newest snapshot is under the current hash: two marks. The older
	// one is neither newest nor current: none.
	// The first of the two is the mark the lone old snapshot carried above,
	// the newest's; the second, which that one lacked, is the
	// current-settings mark.
	if marks, ok := snapshotMarks(stdout.String(), cur); !ok || len(marks) != 2 || marks[0] != newestMarks[0] || marks[1] == newestMarks[0] {
		t.Errorf("newest snapshot under the current hash has marks %q, want %q and the current-settings mark:\n%s", marks, newestMarks[0], stdout.String())
	}
	if marks, ok := snapshotMarks(stdout.String(), "0ld0ld0ld0ld0ld0"); !ok || len(marks) != 0 {
		t.Errorf("older snapshot under other settings has marks %q, want none:\n%s", marks, stdout.String())
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
	secs := statusSections(table)
	// Every section after the table names the city: they share stdout
	// with it, apart from the stderr city header.
	if len(secs) != 3 {
		t.Fatalf("got %d sections, want other parts, city summary and snapshots:\n%s", len(secs), table)
	}
	for _, s := range secs {
		if !strings.HasPrefix(s.header, "test-city: ") {
			t.Errorf("section %q does not name the city", s.header)
		}
	}
	// Each section names the city it covers.
	if !strings.Contains(table, "=== test-city: City Summary ===") {
		t.Errorf("stdout lacks the City Summary header:\n%s", table)
	}

	// The table's State column tells the computed rows from the one that
	// never was: sidewalks reads differently from roads and parking.
	tableText, _, _ := strings.Cut(table, "\n===")
	tableLines := strings.Split(tableText, "\n")
	stateOf := func(rt string) string {
		row := lineWith(tableLines, rt)
		if row == "" {
			t.Fatalf("table has no %s row:\n%s", rt, table)
		}
		cells := strings.Split(strings.TrimRight(row, " "), "  ")
		return strings.TrimSpace(cells[len(cells)-1])
	}
	if roads, parking, sidewalks := stateOf("roads"), stateOf("parking"), stateOf("sidewalks"); roads != parking || roads == sidewalks {
		t.Errorf("states roads=%q parking=%q sidewalks=%q, want sidewalks alone not computed", roads, parking, sidewalks)
	}

	const reingested = "2026-11-14 10:02 UTC"
	// The other parts: the combined total is stale on the roads re-ingest,
	// and census, never computed, says something else.
	parts := sectionWith(secs, "combined")
	if parts == nil {
		t.Fatalf("no section lists the combined part:\n%s", table)
	}
	combined, census := rowFor(parts.lines, "combined"), rowFor(parts.lines, "census")
	if !strings.Contains(combined, "roads") || !strings.Contains(combined, reingested) {
		t.Errorf("combined row = %q, want it stale on roads re-ingested %s", combined, reingested)
	}
	if census == "" || strings.Contains(census, "roads") || strings.Contains(census, reingested) {
		t.Errorf("census row = %q, want a part of its own, not stale", census)
	}

	// The paved figure comes from that stale combined total, so it is
	// asterisked and the footnote names the input and the fix.
	pavedArea := units.FormatAreaLarge(60000, units.Metric)
	summary := sectionWith(secs, pavedArea)
	if summary == nil {
		t.Fatalf("no section shows the combined paved area %s:\n%s", pavedArea, table)
	}
	paved := lineWith(summary.lines, pavedArea)
	if label, _, _ := strings.Cut(paved, pavedArea); !strings.Contains(label, "*") {
		t.Errorf("stale combined figure is not asterisked: %q", paved)
	}
	note := lineWith(summary.lines, "'pvmt all compute'")
	if !strings.Contains(note, "roads") || !strings.Contains(note, reingested) || !strings.HasPrefix(strings.TrimSpace(note), "*") {
		t.Errorf("footnote = %q, want the roads re-ingest and 'pvmt all compute'", note)
	}

	// Roads run 5 is newer than the snapshot's run 1: one part to publish.
	unsnap := lineWith(strings.Split(table, "\n"), "'pvmt snapshots create <name>'")
	if !strings.HasPrefix(unsnap, "test-city: 1 ") || !strings.Contains(unsnap, "(roads)") {
		t.Errorf("unsnapshotted line = %q, want test-city's 1 part (roads)", unsnap)
	}

	// The snapshot is the newest and under the current settings: two marks.
	snaps := sectionWith(secs, cfg.CityHash(city))
	if snaps == nil || rowFor(snaps.lines, "v4") == "" {
		t.Fatalf("no section lists snapshot v4 with its hash:\n%s", table)
	}
	if marks, _ := snapshotMarks(table, cfg.CityHash(city)); len(marks) != 2 {
		t.Errorf("snapshot v4 has marks %q, want newest and current settings", marks)
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
	// Each city is announced on stderr (cmdutil.ForEachCity owns the banner's
	// shape), so stdout stays the table alone.
	for _, name := range []string{"Alpha, CA", "Beta, CA"} {
		if !strings.Contains(chatter, name) || strings.Contains(table, name) {
			t.Errorf("city %q should be announced on stderr only:\nstderr: %s\nstdout: %s", name, chatter, table)
		}
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
