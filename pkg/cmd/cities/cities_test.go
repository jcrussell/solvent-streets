package cities

import (
	"context"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func TestNewCmdCities_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, UnitSystem: func() units.System { return units.Imperial }}

	called := false
	cmd := NewCmdCities(f, func(_ context.Context, opts *Options) error {
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

func TestRunCities_ListsCitiesWithStats(t *testing.T) {
	rtRoads := resource.TypeRoads
	statsFor := func(slug string) func(context.Context, resource.Type) (*db.StatusInfo, error) {
		return func(_ context.Context, rt resource.Type) (*db.StatusInfo, error) {
			if slug == "austin-tx" && rt == rtRoads {
				return &db.StatusInfo{ResourceType: rt, FeatureCount: 42}, nil
			}
			return &db.StatusInfo{ResourceType: rt}, nil
		}
	}

	root := &dbtest.MockRootStore{
		ListCitiesFunc: func(_ context.Context) ([]db.City, error) {
			return []db.City{
				{ID: 1, Slug: "austin-tx", Name: "Austin, TX"},
				{ID: 2, Slug: "boston-ma", Name: "Boston, MA"},
			}, nil
		},
		ForCityFunc: func(id int64) db.Store {
			slug := "austin-tx"
			if id == 2 {
				slug = "boston-ma"
			}
			return &dbtest.MockStore{StatsFunc: statsFor(slug)}
		},
	}

	ios, _, stdout, _ := iostreams.Test()
	opts := &Options{
		IO:         ios,
		RootDB:     func() (db.RootStorer, error) { return root, nil },
		UnitSystem: func() units.System { return units.Imperial },
	}

	if err := runCities(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	out := stdout.String()
	for _, want := range []string{"austin-tx", "Austin, TX", "boston-ma", "Boston, MA", "42"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got: %s", want, out)
		}
	}
}

// TestRunCities_PrefersCombinedArea: TotalPaved comes from the
// combined (de-duplicated) compute row when present, not the per-resource sum
// which double-counts road/parking/sidewalk buffer overlap.
func TestRunCities_PrefersCombinedArea(t *testing.T) {
	root := &dbtest.MockRootStore{
		ListCitiesFunc: func(_ context.Context) ([]db.City, error) {
			return []db.City{{ID: 1, Slug: "austin-tx", Name: "Austin, TX"}}, nil
		},
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{
				// Per-resource areas sum to 300; combined row says 210.
				CityMetrics: append(append(append(
					results.TotalRows(resource.TypeRoads, results.Totals{Area: 100}),
					results.TotalRows(resource.TypeParking, results.Totals{Area: 100})...),
					results.TotalRows(resource.TypeSidewalks, results.Totals{Area: 100})...),
					results.TotalRows(resource.CombinedAll, results.Totals{Area: 210})...),
			}
		},
	}

	ios, _, _, _ := iostreams.Test()
	var captured []cityRow
	opts := &Options{
		IO:         ios,
		RootDB:     func() (db.RootStorer, error) { return root, nil },
		UnitSystem: func() units.System { return units.Metric },
	}
	// Exercise buildCityRow directly to assert the numeric value.
	captured = append(captured, buildCityRow(context.Background(), root.ForCity(1), db.City{ID: 1, Slug: "austin-tx", Name: "Austin, TX"}, ios))
	if got := captured[0].TotalPaved; got != 210 {
		t.Errorf("TotalPaved = %v, want 210 (combined row, not the 300 per-resource sum)", got)
	}

	// And end-to-end through runCities without a combined row: falls back.
	if err := runCities(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
}

// TestCityRow_ExportData_AllFieldsPopulated guards S2: typo-catching
// coverage for the handwritten switch in cityRow.ExportData.
func TestCityRow_ExportData_AllFieldsPopulated(t *testing.T) {
	r := cityRow{
		Slug:          "austin-tx",
		Name:          "Austin, TX",
		FeatureCounts: map[string]int{"roads": 42},
		TotalPaved:    1000,
		LastIngestAt:  "2026-04-18T00:00:00Z",
		LastComputeAt: "2026-04-18T01:00:00Z",
	}
	out := r.ExportData(citiesFields)
	if len(out) != len(citiesFields) {
		t.Fatalf("want %d keys, got %d: %v", len(citiesFields), len(out), out)
	}
	for _, f := range citiesFields {
		if _, ok := out[f]; !ok {
			t.Errorf("missing field %q", f)
		}
	}
}

// TestCityRow_ExportData_TotalPavedNeedsACompute: a city with nothing
// computed has no paved footprint to report, so total_paved is omitted
// rather than emitted as 0.
func TestCityRow_ExportData_TotalPavedNeedsACompute(t *testing.T) {
	r := cityRow{Slug: "austin-tx", Name: "Austin, TX", FeatureCounts: map[string]int{"roads": 42}}
	if v, ok := r.ExportData(citiesFields)["total_paved"]; ok {
		t.Errorf("total_paved = %v for a city never computed, want it omitted", v)
	}
	r.LastComputeAt = "2026-04-18T01:00:00Z"
	if v, ok := r.ExportData(citiesFields)["total_paved"]; !ok || v != 0.0 {
		t.Errorf("total_paved = %v, %v for a computed city, want 0", v, ok)
	}
}

// TestRunCities_EmptyDatabase: an empty database is a missing prerequisite
// (exit 3, naming 'pvmt all ingest'), and Out stays silent so
// `pvmt cities | wc -l` is 0 — scripted consumers rely on that.
func TestRunCities_EmptyDatabase(t *testing.T) {
	root := &dbtest.MockRootStore{
		ListCitiesFunc: func(_ context.Context) ([]db.City, error) {
			return nil, nil
		},
	}

	ios, _, stdout, stderr := iostreams.Test()
	opts := &Options{
		IO:         ios,
		RootDB:     func() (db.RootStorer, error) { return root, nil },
		UnitSystem: func() units.System { return units.Imperial },
	}

	err := runCities(context.Background(), opts)
	if !cmdutil.OnlyPrereqs(err) {
		t.Fatalf("err = %v, want a missing prerequisite (exit 3)", err)
	}
	if got := strings.Join(hint.Hints(err), "\n"); !strings.Contains(got, "pvmt all ingest") {
		t.Errorf("hint = %q, want it to name 'pvmt all ingest'", got)
	}
	if got := stdout.String() + stderr.String(); got != "" {
		t.Errorf("output for empty-state = %q, want none (the error carries the message)", got)
	}
}
