package forecast

import (
	"testing"
)

func defaultParams() *Params {
	return NewParams(0.035, 0.01, nil)
}

func TestSimulate_DoNothing_DecreasingPCI(t *testing.T) {
	p := defaultParams()
	s := Scenario{Name: "test-dn", Label: "Test", Strategy: StrategyDoNothing}
	result := Simulate(s, 100000, 85.0, 20, p.PCI, p.Cost, p.Growth)

	if len(result.Years) != 20 {
		t.Fatalf("expected 20 years, got %d", len(result.Years))
	}

	for i := 1; i < len(result.Years); i++ {
		if result.Years[i].PCI >= result.Years[i-1].PCI {
			t.Errorf("PCI should decrease: year %d (%.2f) >= year %d (%.2f)",
				result.Years[i].Year, result.Years[i].PCI,
				result.Years[i-1].Year, result.Years[i-1].PCI)
		}
	}

	// Do-nothing should have zero spend
	for _, y := range result.Years {
		if y.AnnualSpend != 0 {
			t.Errorf("year %d: do-nothing should have 0 spend, got %.2f", y.Year, y.AnnualSpend)
		}
	}

	// Deferred backlog should grow
	for i := 1; i < len(result.Years); i++ {
		if result.Years[i].DeferredBacklog <= result.Years[i-1].DeferredBacklog {
			t.Errorf("deferred backlog should grow in do-nothing scenario")
		}
	}
}

func TestSimulate_Unconstrained_PCIRecovery(t *testing.T) {
	p := defaultParams()
	s := Scenario{Name: "full", Label: "Full", AnnualBudget: 0, Strategy: StrategyWorstFirst}
	result := Simulate(s, 100000, 85.0, 10, p.PCI, p.Cost, p.Growth)

	// With full funding, PCI should fully recover to initial value each year
	for _, y := range result.Years {
		if y.PCI != 85.0 {
			t.Errorf("year %d: full funding PCI should equal initial (85.0), got %.2f", y.Year, y.PCI)
		}
	}

	// Deferred backlog should be zero (spending = need)
	for _, y := range result.Years {
		if y.DeferredBacklog > 0.01 {
			t.Errorf("year %d: unconstrained should have ~0 backlog, got %.2f", y.Year, y.DeferredBacklog)
		}
	}
}

func TestSimulate_BudgetConstrained_Intermediate(t *testing.T) {
	p := defaultParams()
	// Get year-1 need to set budget at 50%
	doNothing := Simulate(
		Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing},
		100000, 85.0, 1, p.PCI, p.Cost, p.Growth,
	)
	year1Need := doNothing.Years[0].AnnualNeed

	constrained := Simulate(
		Scenario{Name: "half", Label: "Half", AnnualBudget: year1Need * 0.5, Strategy: StrategyWorstFirst},
		100000, 85.0, 20, p.PCI, p.Cost, p.Growth,
	)

	full := Simulate(
		Scenario{Name: "full", Label: "Full", AnnualBudget: 0, Strategy: StrategyWorstFirst},
		100000, 85.0, 20, p.PCI, p.Cost, p.Growth,
	)

	lastConstrained := constrained.Years[19]
	lastFull := full.Years[19]

	// Budget-constrained should have worse PCI than full funding
	if lastConstrained.PCI >= lastFull.PCI {
		t.Errorf("constrained PCI (%.2f) should be worse than full (%.2f)",
			lastConstrained.PCI, lastFull.PCI)
	}

	// Budget-constrained should have positive deferred backlog
	if lastConstrained.DeferredBacklog <= 0 {
		t.Error("constrained scenario should have deferred backlog > 0")
	}
}

func TestSimulate_PreventiveOutperformsWorstFirst(t *testing.T) {
	p := defaultParams()
	doNothing := Simulate(
		Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing},
		100000, 85.0, 1, p.PCI, p.Cost, p.Growth,
	)
	budget := doNothing.Years[0].AnnualNeed * 0.5

	worst := Simulate(
		Scenario{Name: "worst", Label: "Worst", AnnualBudget: budget, Strategy: StrategyWorstFirst},
		100000, 85.0, 20, p.PCI, p.Cost, p.Growth,
	)

	preventive := Simulate(
		Scenario{Name: "prev", Label: "Prev", AnnualBudget: budget, Strategy: StrategyPreventiveFirst},
		100000, 85.0, 20, p.PCI, p.Cost, p.Growth,
	)

	lastWorst := worst.Years[19]
	lastPreventive := preventive.Years[19]

	if lastPreventive.PCI <= lastWorst.PCI {
		t.Errorf("preventive-first PCI (%.2f) should outperform worst-first (%.2f) at same budget",
			lastPreventive.PCI, lastWorst.PCI)
	}
}

func TestStrategy_StringRoundTrip(t *testing.T) {
	strategies := []Strategy{StrategyDoNothing, StrategyWorstFirst, StrategyPreventiveFirst}
	for _, s := range strategies {
		str := s.String()
		parsed, err := ParseStrategy(str)
		if err != nil {
			t.Errorf("ParseStrategy(%q): %v", str, err)
			continue
		}
		if parsed != s {
			t.Errorf("round-trip failed: %v -> %q -> %v", s, str, parsed)
		}
	}
}

func TestParseStrategy_Invalid(t *testing.T) {
	_, err := ParseStrategy("invalid-strategy")
	if err == nil {
		t.Error("expected error for invalid strategy")
	}
}

func TestDefaultComparisons(t *testing.T) {
	scenarios := DefaultComparisons(1000000)
	if len(scenarios) != 5 {
		t.Fatalf("expected 5 default scenarios, got %d", len(scenarios))
	}

	// First should be 25% funding (do-nothing is created separately as baseline)
	if scenarios[0].Name != "fund-25pct" {
		t.Errorf("first scenario should be fund-25pct, got %s", scenarios[0].Name)
	}

	// Check budget levels
	if scenarios[0].AnnualBudget != 250000 {
		t.Errorf("25%% funding should be 250000, got %.0f", scenarios[0].AnnualBudget)
	}
}
