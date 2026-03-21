package forecast

// Comparison groups multiple scenarios for side-by-side analysis.
type Comparison struct {
	Title     string           `json:"title"`
	Scenarios []ScenarioResult `json:"scenarios"`
}

// DefaultComparisons returns three scenario comparison groups.
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

// GroupedComparisons returns scenarios organized into comparison groups.
func GroupedComparisons(year1Need float64, areaSqFt, initialPCI float64, years int,
	pci *ExponentialPCIForecaster, cost *TieredCostProjector,
	growth *LinearGrowthEstimator) []Comparison {

	scenarios := DefaultComparisons(year1Need)

	fundingResults := make([]ScenarioResult, len(scenarios))
	for i, s := range scenarios {
		fundingResults[i] = Simulate(s, areaSqFt, initialPCI, years, pci, cost, growth)
	}

	return []Comparison{
		{Title: "Funding Levels", Scenarios: fundingResults},
	}
}
