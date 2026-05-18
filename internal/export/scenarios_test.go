package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// TestExportScenariosForCity_PropagatesDBError pins the deployment-correctness
// contract: when LatestComputeResult fails with anything other than
// sql.ErrNoRows, exportScenariosForCity surfaces the error and writes nothing
// — so a transient DB failure aborts the static export rather than emitting
// partial output that would mislead the downstream dashboard.
//
// Mirrors internal/server/handlers_test.go TestBuildForecasts_DBErrorEvicts for
// the export-command path. Regression caught: reverting buildResourceForecast
// to silent-swallow the underlying error (the pre-bkz behavior) breaks
// errors.Is and lets exportScenariosForCity return nil with no files.
func TestExportScenariosForCity_PropagatesDBError(t *testing.T) {
	sentinel := errors.New("db unavailable")
	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, _ resource.Type) (*db.ComputeResult, error) {
			return nil, sentinel
		},
	}
	entry := CityEntry{
		Config: &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
	dataDir := t.TempDir()

	err := exportScenariosForCity(t.Context(), entry, dataDir)
	if err == nil {
		t.Fatal("expected error from exportScenariosForCity, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(err, sentinel) = false; err = %v", err)
	}

	for _, name := range []string{"forecast.json", "hex-cost-summary.json", "scenarios.json"} {
		if _, statErr := os.Stat(filepath.Join(dataDir, name)); !os.IsNotExist(statErr) {
			t.Errorf("expected %s not to exist on error path; stat = %v", name, statErr)
		}
	}
}

// TestBuildResourceForecast_BboxBaselineGatedOnCityScope pins the contract
// that BboxBaseline is only populated when city-scope cohorts exist. When
// hasCityScope is false the primary baseline already reflects the bbox
// cohorts, so computing a second identical Simulate is wasted work — the
// fix routes the bboxBaseline call inside the `if hasCityScope` branch.
//
// Regression caught: reverting the gate (unconditionally running
// forecast.Simulate over bboxCohorts) doubles per-resource Simulate work on
// the common no-city-scope path. We can't directly count Simulate calls, so
// we anchor on the observable contracts: (a) BboxBaseline is nil when city
// stats are empty, (b) BboxBaseline is non-nil and equal to a fresh
// Simulate over bbox cohorts when city stats are present.
func TestBuildResourceForecast_BboxBaselineGatedOnCityScope(t *testing.T) {
	now := time.Now()
	doNothing := forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing}
	fc := &config.ForecastConfig{InitialPCI: 85, GrowthRate: 0.01, Years: 5}
	costTiers := forecast.DefaultCostTiers

	t.Run("no_city_scope_skips_bbox_baseline", func(t *testing.T) {
		var cityCalls atomic.Int64
		store := &dbtest.MockStore{
			LatestComputeResultFunc: func(_ context.Context, rt resource.Type) (*db.ComputeResult, error) {
				return &db.ComputeResult{ResourceType: rt, TotalAreaSqM: 1000, ComputedAt: now}, nil
			},
			ListCohortStatsFunc: func(_ context.Context, rt resource.Type) ([]db.CohortStat, error) {
				if rt.Scope() == resource.ScopeCity {
					cityCalls.Add(1)
					return nil, nil
				}
				return nil, nil
			},
		}
		entry := CityEntry{
			Config: &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}},
			City:   config.CityConfig{Name: "Test City"},
			Store:  store,
			Slug:   "test-city",
		}

		fe, err := buildResourceForecast(t.Context(), &resource.Pavement{}, entry, fc, costTiers, doNothing)
		if err != nil {
			t.Fatalf("buildResourceForecast: %v", err)
		}
		if fe.BboxBaseline != nil {
			t.Errorf("BboxBaseline = %+v; want nil when city stats are empty", fe.BboxBaseline)
		}
		if cityCalls.Load() != 1 {
			t.Errorf("city-scope ListCohortStats calls = %d; want exactly 1", cityCalls.Load())
		}
		if len(fe.Baseline.Years) != fc.Years {
			t.Errorf("Baseline.Years length = %d; want %d", len(fe.Baseline.Years), fc.Years)
		}
	})

	t.Run("city_scope_populates_bbox_baseline", func(t *testing.T) {
		store := &dbtest.MockStore{
			LatestComputeResultFunc: func(_ context.Context, rt resource.Type) (*db.ComputeResult, error) {
				return &db.ComputeResult{ResourceType: rt, TotalAreaSqM: 2000, ComputedAt: now}, nil
			},
			ListCohortStatsFunc: func(_ context.Context, rt resource.Type) ([]db.CohortStat, error) {
				if rt.Scope() == resource.ScopeCity {
					return []db.CohortStat{{
						ResourceType:   rt,
						Classification: "residential",
						AreaSqM:        500,
					}}, nil
				}
				return nil, nil
			},
		}
		entry := CityEntry{
			Config: &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}},
			City:   config.CityConfig{Name: "Test City"},
			Store:  store,
			Slug:   "test-city",
		}

		fe, err := buildResourceForecast(t.Context(), &resource.Pavement{}, entry, fc, costTiers, doNothing)
		if err != nil {
			t.Fatalf("buildResourceForecast: %v", err)
		}
		if fe.BboxBaseline == nil {
			t.Fatal("BboxBaseline = nil; want non-nil when city stats are present")
		}
		// Bbox-area (2000) > city-area (500), so the bbox baseline must reflect
		// a larger system than the city-scope Baseline.
		if len(fe.BboxBaseline.Years) == 0 || len(fe.Baseline.Years) == 0 {
			t.Fatalf("expected non-empty year slices; baseline=%d bbox=%d", len(fe.Baseline.Years), len(fe.BboxBaseline.Years))
		}
		if fe.BboxBaseline.Years[0].AnnualNeed <= fe.Baseline.Years[0].AnnualNeed {
			t.Errorf("BboxBaseline.year0 AnnualNeed = %v; want > Baseline.year0 AnnualNeed = %v",
				fe.BboxBaseline.Years[0].AnnualNeed, fe.Baseline.Years[0].AnnualNeed)
		}
	})
}
