package forecast

// DefaultComparisons returns three default funding-level scenarios.
// year1Need is the estimated annual treatment cost in year 1, used to
// set budget levels for constrained scenarios.
func DefaultComparisons(year1Need float64) []Scenario {
	return []Scenario{
		// Funding levels (baseline/do-nothing is created separately by export)
		{Name: "fund-25pct", Label: "25% Funding", AnnualBudget: year1Need * 0.25, Strategy: StrategyWorstFirst},
		{Name: "fund-50pct", Label: "50% Funding", AnnualBudget: year1Need * 0.50, Strategy: StrategyWorstFirst},
		{Name: "fund-100pct", Label: "Full Funding", FullFunding: true, Strategy: StrategyWorstFirst},
	}
}

// SimulateDefaults runs the default funding-level scenarios against the
// supplied cohorts and returns the resulting ScenarioResults.
func SimulateDefaults(year1Need float64, cohorts []Cohort, years int, p *Params) []ScenarioResult {
	scenarios := DefaultComparisons(year1Need)
	results := make([]ScenarioResult, len(scenarios))
	for i, s := range scenarios {
		results[i] = Simulate(s, cohorts, years, p)
	}
	return results
}

// ReferenceScenarios is the comparison set the Financials charts draw: the
// do-nothing baseline followed by the default funding levels, each sized off
// the baseline's year-1 need. The export (scenarios.json) and the browser
// bridge both build it here, so a reference line re-simulated in the browser
// for a subset of the network matches the exported one at equal inputs.
func ReferenceScenarios(cohorts []Cohort, years int, p *Params) []ScenarioResult {
	baseline := Simulate(
		Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: StrategyDoNothing},
		cohorts, years, p,
	)
	return append([]ScenarioResult{baseline}, SimulateDefaults(baseline.Years[0].AnnualNeed, cohorts, years, p)...)
}
