// Package exporttest builds the deterministic export fixture shared by the
// internal/export contract tests, the JS-spec fixture generator and the
// integration tier's summary golden. It builds the store and config in
// memory rather than reading a captured golden, and returns
// parts rather than an export.CityEntry so that export's own in-package
// tests can import it without an import cycle.
package exporttest

import (
	"maps"
	"slices"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// AssetFiles returns small stand-ins for the site-root browser files, for
// export.NewAssets. Unit tests use these instead of the real embedded WASM,
// which only exists after `make wasm`; the real assets are exercised in
// integration/. Each call returns fresh slices.
func AssetFiles() map[string][]byte {
	return map[string][]byte{
		"pvmt.wasm":    []byte("\x00asm\x01\x00\x00\x00 fake forecast wasm"),
		"wasm_exec.js": []byte("// fake wasm_exec.js\n"),
		"app.js":       []byte("// fake app.js\n"),
		"game.js":      []byte("// fake game.js\n"),
		"selectors.js": []byte("// fake selectors.js\n"),
		"common.js":    []byte("// fake common.js\n"),
	}
}

// CityName and Slug identify the fixture city.
const (
	CityName = "Golden City"
	Slug     = "golden-city"
)

// Store returns a mock store with hand-picked compute results per resource
// for both the bbox and :city scopes, so the export builders exercise the
// dual-scope branch, plus per-class cohort stats for each.
func Store() *dbtest.MockStore {
	totals := map[resource.Type]results.Totals{
		resource.TypeRoads:                              {Area: 1_500_000, Features: 800},
		resource.TypeRoads.With(resource.ScopeCity):     {Area: 900_000, Features: 480},
		resource.TypeParking:                            {Area: 200_000, Features: 120},
		resource.TypeParking.With(resource.ScopeCity):   {Area: 150_000, Features: 90},
		resource.TypeSidewalks:                          {Area: 100_000, Features: 250},
		resource.TypeSidewalks.With(resource.ScopeCity): {Area: 80_000, Features: 200},
	}
	// Per-class cohort stats per resource and scope. BuildScenariosData and
	// BuildForecastsForCity build their lines from these multi-cohort seeds
	// (the same source as the interactive WASM line), so the fixture must supply
	// them to exercise that path rather than the single-synthetic-cohort
	// fallback. Class areas are split so they roughly sum to the matching
	// compute total above; the road classes carry distinct decay rates so
	// the blended (multi-cohort) curve differs from a single-rate cohort.
	cohorts := map[resource.Type][]results.Cohort{
		resource.TypeRoads: {
			{Class: "primary", Area: 500_000, Features: 200},
			{Class: "residential", Area: 1_000_000, Features: 600},
		},
		resource.TypeRoads.With(resource.ScopeCity): {
			{Class: "primary", Area: 300_000, Features: 120},
			{Class: "residential", Area: 600_000, Features: 360},
		},
		resource.TypeParking: {
			{Class: "parking", Area: 200_000, Features: 120},
		},
		resource.TypeParking.With(resource.ScopeCity): {
			{Class: "parking", Area: 150_000, Features: 90},
		},
		resource.TypeSidewalks: {
			{Class: "sidewalks", Area: 100_000, Features: 250},
		},
		resource.TypeSidewalks.With(resource.ScopeCity): {
			{Class: "sidewalks", Area: 80_000, Features: 200},
		},
	}
	store := &dbtest.MockStore{}
	for _, t := range slices.Sorted(maps.Keys(totals)) {
		store.CityMetrics = append(store.CityMetrics, results.TotalRows(t, totals[t])...)
		store.CityMetrics = append(store.CityMetrics, results.CohortRows(t, cohorts[t])...)
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
