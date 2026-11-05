// Package exporttest builds the deterministic export fixture shared by the
// internal/export contract tests, the JS-spec fixture generator and the
// integration tier's summary golden. It builds the store and config in
// memory rather than reading a captured golden (byob-testing.8), and returns
// parts rather than an export.CityEntry so that export's own in-package
// tests can import it without an import cycle.
package exporttest

import (
	"context"
	"database/sql"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// CityName and Slug identify the fixture city.
const (
	CityName = "Golden City"
	Slug     = "golden-city"
)

// Store returns a mock store with hand-picked compute results per resource
// for both the bbox and :city scopes, so the export builders exercise the
// dual-scope branch, plus per-class cohort stats for each.
func Store() *dbtest.MockStore {
	results := map[resource.Type]db.ComputeResult{
		resource.TypeRoads:                              {ResourceType: resource.TypeRoads, TotalArea: 1_500_000, FeatureCount: 800},
		resource.TypeRoads.With(resource.ScopeCity):     {ResourceType: resource.TypeRoads.With(resource.ScopeCity), TotalArea: 900_000, FeatureCount: 480},
		resource.TypeParking:                            {ResourceType: resource.TypeParking, TotalArea: 200_000, FeatureCount: 120},
		resource.TypeParking.With(resource.ScopeCity):   {ResourceType: resource.TypeParking.With(resource.ScopeCity), TotalArea: 150_000, FeatureCount: 90},
		resource.TypeSidewalks:                          {ResourceType: resource.TypeSidewalks, TotalArea: 100_000, FeatureCount: 250},
		resource.TypeSidewalks.With(resource.ScopeCity): {ResourceType: resource.TypeSidewalks.With(resource.ScopeCity), TotalArea: 80_000, FeatureCount: 200},
	}
	// Per-class cohort stats per resource and scope. BuildScenariosData and
	// BuildForecastsForCity now build their lines from these multi-cohort seeds
	// (the same source as the interactive WASM line), so the fixture must supply
	// them to exercise that path rather than the single-synthetic-cohort
	// fallback. Class areas are split so they roughly sum to the matching
	// compute TotalArea above; the road classes carry distinct decay rates so
	// the blended (multi-cohort) curve differs from a single-rate cohort.
	cohorts := map[resource.Type][]db.CohortStat{
		resource.TypeRoads: {
			{ResourceType: resource.TypeRoads, Classification: "primary", Area: 500_000, FeatureCount: 200},
			{ResourceType: resource.TypeRoads, Classification: "residential", Area: 1_000_000, FeatureCount: 600},
		},
		resource.TypeRoads.With(resource.ScopeCity): {
			{ResourceType: resource.TypeRoads.With(resource.ScopeCity), Classification: "primary", Area: 300_000, FeatureCount: 120},
			{ResourceType: resource.TypeRoads.With(resource.ScopeCity), Classification: "residential", Area: 600_000, FeatureCount: 360},
		},
		resource.TypeParking: {
			{ResourceType: resource.TypeParking, Classification: "parking", Area: 200_000, FeatureCount: 120},
		},
		resource.TypeParking.With(resource.ScopeCity): {
			{ResourceType: resource.TypeParking.With(resource.ScopeCity), Classification: "parking", Area: 150_000, FeatureCount: 90},
		},
		resource.TypeSidewalks: {
			{ResourceType: resource.TypeSidewalks, Classification: "sidewalks", Area: 100_000, FeatureCount: 250},
		},
		resource.TypeSidewalks.With(resource.ScopeCity): {
			{ResourceType: resource.TypeSidewalks.With(resource.ScopeCity), Classification: "sidewalks", Area: 80_000, FeatureCount: 200},
		},
	}
	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, key resource.Type) (*db.ComputeResult, error) {
			r, ok := results[key]
			if !ok {
				return nil, sql.ErrNoRows
			}
			return &r, nil
		},
		ListCohortStatsFunc: func(_ context.Context, key resource.Type) ([]db.CohortStat, error) {
			return cohorts[key], nil
		},
	}
	return store
}

// ForecastConfig returns the frozen forecast config used with Store. The
// export contract tests assert invariants that depend on it (fund-100pct holds
// PCI at InitialPCI; CurrentBudget exceeds the roads break-even), and a change
// moves the integration summary golden.
//
// Also the basis for the JS specs' fixtures (js_harness_test.go), which
// relabel one tier and lengthen the horizon but otherwise follow this. A change
// here moves what internal/export/templates/__tests__ runs against.
func ForecastConfig() config.ForecastConfig {
	return config.ForecastConfig{
		Years:      10,
		InitialPCI: 85,
		DecayRate:  1.5,
		GrowthRate: 0.01,
		// Pinned explicitly (not left 0-to-default) so a change to
		// DefaultTreatmentCycleYears doesn't silently move the fixture. Gates
		// annual_need/break_even by 1/12; PCI columns are unchanged.
		TreatmentCycleYears: 12,
		// Nonzero so the roads solvency metrics (insolvency_year,
		// break_even_budget, current_budget, funding_gap) in forecast.json
		// serialize to real values — the solvency contract covers nothing
		// otherwise. BuildScenariosData ignores CurrentBudget, so scenarios.json
		// is unaffected.
		CurrentBudget: 20_000_000,
		CostTiers: []config.CostTierCfg{
			{MinPCI: 70, MaxPCI: 100, CostPerSqM: 2.0, Label: "preventive"},
			{MinPCI: 50, MaxPCI: 70, CostPerSqM: 12.0, Label: "rehab"},
			{MinPCI: 0, MaxPCI: 50, CostPerSqM: 60.0, Label: "reconstruct"},
		},
	}
}
