package forecast

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// budgetedBacktest is testdata/mtc_budgeted_backtest.json: six Bay Area
// cities with a cited roads budget, their MTC PCI in 2019 and 2024, and their
// city-maintained road classes from a compute of examples/bay-area-ca.
type budgetedBacktest struct {
	Cities []struct {
		Slug          string  `json:"slug"`
		PCI2019       float64 `json:"pci_2019"`
		PCI2024       float64 `json:"pci_2024"`
		CurrentBudget float64 `json:"current_budget"`
		GrowthRate    float64 `json:"growth_rate"`
		Cohorts       []struct {
			Classification string  `json:"classification"`
			Area           float64 `json:"area"`
		} `json:"cohorts"`
	} `json:"cities"`
}

// TestBacktest_BudgetedFiveYears runs each city worst-first at its cited
// budget for five years from its 2019 PCI, as the dashboard's "Your budget"
// line would, and compares the result with the audited 2024 PCI
// (docs/validation.md, "Budgeted backtest"). partialEfficiency is set by this
// set at the cited LTPP decay rates, so the gate is in-sample: it catches a
// change to the partial-funding response, not an independent validation.
//
// The tolerance is loose on purpose. Today's budgets stand in for 2019-24
// spending and MTC's PCI is a three-year average, so Oakland and Hayward,
// which improved on budgets the model reads as underfunded, stay about 6 PCI
// low under any setting.
func TestBacktest_BudgetedFiveYears(t *testing.T) {
	raw, err := os.ReadFile("testdata/mtc_budgeted_backtest.json")
	if err != nil {
		t.Fatal(err)
	}
	var bt budgetedBacktest
	if err := json.Unmarshal(raw, &bt); err != nil {
		t.Fatal(err)
	}
	if len(bt.Cities) != 6 {
		t.Fatalf("fixture has %d cities, want 6", len(bt.Cities))
	}
	var sse, bias float64
	for _, c := range bt.Cities {
		inputs := make([]CohortInput, len(c.Cohorts))
		for i, k := range c.Cohorts {
			inputs[i] = CohortInput{Classification: k.Classification, Area: k.Area}
		}
		cohorts := CohortsFromInputs(inputs, c.PCI2019, 0)
		p := NewParams(c.GrowthRate, nil, DefaultTreatmentCycleYears, 1)
		r := Simulate(Scenario{Strategy: StrategyWorstFirst, AnnualBudget: c.CurrentBudget}, cohorts, 5, p)
		dn := Simulate(Scenario{Strategy: StrategyDoNothing}, cohorts, 5, p)
		got, gotDN := r.Years[4].PCI, dn.Years[4].PCI
		resid := got - c.PCI2024
		t.Logf("%-14s 2019 %2.0f -> %5.1f (do-nothing %5.1f), audited %2.0f, residual %+5.1f", c.Slug, c.PCI2019, got, gotDN, c.PCI2024, resid)
		sse += resid * resid
		bias += resid
		if math.Abs(resid) > 7.5 {
			t.Errorf("%s: predicted %.1f vs audited %.0f, off by more than 7.5 PCI", c.Slug, got, c.PCI2024)
		}
		// Do-nothing stays a lower bound on what a maintained network does
		// (validation.md section 2).
		if gotDN >= c.PCI2024 {
			t.Errorf("%s: do-nothing %.1f is not below the audited %.0f", c.Slug, gotDN, c.PCI2024)
		}
	}
	n := float64(len(bt.Cities))
	rmse := math.Sqrt(sse / n)
	t.Logf("RMSE %.2f, mean residual %+.2f", rmse, bias/n)
	if rmse > 4.5 {
		t.Errorf("RMSE %.2f, want at most 4.5 (the shipped response gives about 4.2)", rmse)
	}
}
