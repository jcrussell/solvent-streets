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
// the same Simulate). Every float is rounded to 6 significant digits first,
// so the golden moves when a number moves, not when a sum is reassociated.
// There is no -update flag: a mismatch prints the diff and the `cp -f` that
// accepts it (internal/goldentest); read the diff line by line before
// running it.
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

	result := roundSig6(forecast.Simulate(scenario, cohorts, 10, params))

	got, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')

	goldentest.Check(t, filepath.Join("testdata", "parity_output.json"), got)
}

// roundSig6 rounds every float in r to 6 significant digits.
func roundSig6(r forecast.ScenarioResult) forecast.ScenarioResult {
	r.Scenario.AnnualBudget = goldentest.Sig6(r.Scenario.AnnualBudget)
	for i := range r.Years {
		y := &r.Years[i]
		y.PCI = goldentest.Sig6(y.PCI)
		y.Area = goldentest.Sig6(y.Area)
		y.AnnualNeed = goldentest.Sig6(y.AnnualNeed)
		y.AnnualSpend = goldentest.Sig6(y.AnnualSpend)
		y.DeferredBacklog = goldentest.Sig6(y.DeferredBacklog)
	}
	for i := range r.FinalCohorts {
		c := &r.FinalCohorts[i]
		c.EndPCI = goldentest.Sig6(c.EndPCI)
		c.Area = goldentest.Sig6(c.Area)
		c.DecayRate = goldentest.Sig6(c.DecayRate)
		c.TotalSpend = goldentest.Sig6(c.TotalSpend)
		c.TotalDeficit = goldentest.Sig6(c.TotalDeficit)
	}
	return r
}
