package forecast_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// TestSimulateParity locks down the output of forecast.Simulate against a
// committed golden so a refactor that changes simulation semantics breaks
// both the CLI and the WASM forecast (internal/forecast/bridge.Run calls
// the same Simulate). Regenerate the golden with `-update` if the change
// is intentional, then read the diff line by line before committing.
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
	params := forecast.NewParams(0.01, costTiers)

	result := forecast.Simulate(scenario, cohorts, 10, params)

	got, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')

	goldenPath := filepath.Join("testdata", "parity_output.json")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden updated: %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Simulate output diverged from %s. If intentional, regenerate with:\n  go test ./internal/forecast -run TestSimulateParity -update", goldenPath)
	}
}

var updateGolden = flag.Bool("update", false, "regenerate the parity golden file")
