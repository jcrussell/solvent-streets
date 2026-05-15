package cities

import (
	"context"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func TestNewCmdCities_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, UnitSystem: func() units.System { return units.Imperial }}

	called := false
	cmd := NewCmdCities(f, func(opts *Options) error {
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
	statsFor := func(slug string) func(context.Context, string) (*db.StatusInfo, error) {
		return func(_ context.Context, rt string) (*db.StatusInfo, error) {
			if slug == "austin-tx" && rt == "roads" {
				return &db.StatusInfo{ResourceType: rt, FeatureCount: 42, TotalAreaSqM: 1000}, nil
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

// TestCityRow_ExportData_AllFieldsPopulated guards S2: typo-catching
// coverage for the handwritten switch in cityRow.ExportData.
func TestCityRow_ExportData_AllFieldsPopulated(t *testing.T) {
	r := cityRow{
		Slug:         "austin-tx",
		Name:         "Austin, TX",
		Features:     map[string]int{"roads": 42},
		TotalAreaSqM: 1000,
		LastIngest:   "2026-04-18T00:00:00Z",
		LastCompute:  "2026-04-18T01:00:00Z",
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

func TestRunCities_EmptyDatabase(t *testing.T) {
	root := &dbtest.MockRootStore{
		ListCitiesFunc: func(_ context.Context) ([]db.City, error) {
			return nil, nil
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
	if !strings.Contains(stdout.String(), "No cities") {
		t.Errorf("expected empty-db hint, got: %s", stdout.String())
	}
}
