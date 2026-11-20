package export

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/export/exporttest"
	"github.com/jcrussell/solvent-streets/internal/forecast/bridge"
)

// The fixture city's forecast seed, sent to the bridge the way the dashboard
// sends it (app.js forecastInput: the seed's settings, its cohorts and a
// budget), simulates. A fixture setting outside the bounds the bridge
// validates against would leave every JS spec running a seed the real WASM
// refuses.
func TestFixtureSeed_RunsThroughTheBridge(t *testing.T) {
	fc := exporttest.ForecastConfig()
	seed, err := BuildForecastSeed(context.Background(), &fc, exporttest.Store())
	if err != nil {
		t.Fatalf("BuildForecastSeed: %v", err)
	}
	var s ForecastSeedJSON
	if err := json.Unmarshal([]byte(seed), &s); err != nil {
		t.Fatalf("unmarshal seed: %v", err)
	}
	if len(s.Cohorts) == 0 {
		t.Fatal("the fixture seed has no cohorts")
	}
	cohorts := make([]bridge.Cohort, len(s.Cohorts))
	for i, c := range s.Cohorts {
		cohorts[i] = bridge.Cohort(c)
	}
	for _, in := range []bridge.Input{
		// The page's interactive line: the seed's cohorts at today's budget.
		{Area: s.MaintainedArea, InitialPCI: s.InitialPCI, DecayRate: s.DecayRate, GrowthRate: s.GrowthRate,
			HorizonYears: s.HorizonYears, CycleYears: s.TreatmentCycleYears, CostTiers: s.CostTiers,
			CostMultiplier: s.CostMultiplier, AnnualBudget: fc.CurrentBudget, Strategy: "worst_first",
			Cohorts: cohorts, Solvency: true},
		// A fresh database: no cohorts, so the bridge spreads the seed's
		// single decay rate over the area.
		{Area: s.MaintainedArea, InitialPCI: s.InitialPCI, DecayRate: s.DecayRate, GrowthRate: s.GrowthRate,
			HorizonYears: s.HorizonYears, CycleYears: s.TreatmentCycleYears, CostTiers: s.CostTiers,
			CostMultiplier: s.CostMultiplier, Strategy: "do_nothing"},
	} {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bridge.Run(raw); err != nil {
			t.Errorf("bridge.Run(fixture seed, %d cohorts): %v", len(in.Cohorts), err)
		}
	}
}
