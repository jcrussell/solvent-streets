package forecast_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/goldentest"
)

// TestSimulateParity locks down the output of forecast.Simulate against a
// committed golden so a refactor that changes simulation semantics breaks
// both the CLI and the WASM forecast (internal/forecast/bridge.Run calls
// the same Simulate). There is no -update flag: a mismatch prints the diff
// and the `cp -f` that accepts it (internal/goldentest); read the diff line
// by line before running it.
func TestSimulateParity(t *testing.T) {
	scenario := forecast.Scenario{
		Name:         "parity-scenario",
		Label:        "Parity Test",
		AnnualBudget: 1_000_000,
		Strategy:     forecast.StrategyWorstFirst,
	}
	cohorts := []forecast.Cohort{
		{Classification: "primary", Area: 250_000, DecayRate: 0.05, InitialPCI: 85},
		{Classification: "secondary", Area: 500_000, DecayRate: 0.04, InitialPCI: 75},
		{Classification: "residential", Area: 750_000, DecayRate: 0.03, InitialPCI: 65},
	}
	costTiers := []forecast.CostTier{
		{MinPCI: 0, MaxPCI: 40, CostPerSqM: 50, Label: "reconstruction"},
		{MinPCI: 40, MaxPCI: 70, CostPerSqM: 15, Label: "rehabilitation"},
		{MinPCI: 70, MaxPCI: 100, CostPerSqM: 2, Label: "preventive"},
	}
	// N=12 matches the production default an un-customized browser sends (the
	// seed ships ResolveCycleYears(fc), 12 when unset), so this golden pins the
	// real WASM/CLI contract under treatment-cycle gating.
	params := forecast.NewParams(0.01, costTiers, 12, 1)

	result := forecast.Simulate(scenario, cohorts, 10, params)

	got, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')

	goldentest.Check(t, filepath.Join("testdata", "parity_output.json"), got)
}
