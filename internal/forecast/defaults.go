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
		{Name: "fund-100pct", Label: "Full Funding", AnnualBudget: 0, Strategy: StrategyWorstFirst},
		// Strategy comparison at 50% budget
		{Name: "worst-first-50", Label: "Worst First (50%)", AnnualBudget: year1Need * 0.50, Strategy: StrategyWorstFirst},
		{Name: "preventive-first-50", Label: "Preventive First (50%)", AnnualBudget: year1Need * 0.50, Strategy: StrategyPreventiveFirst},
	}
}

// GroupedComparisons returns scenarios organized into comparison groups.
func GroupedComparisons(year1Need float64, areaSqFt, initialPCI float64, years int,
	pci *ExponentialPCIForecaster, cost *TieredCostProjector,
	growth *LinearGrowthEstimator) []Comparison {

	scenarios := DefaultComparisons(year1Need)

	fundingResults := make([]ScenarioResult, 3)
	for i, s := range scenarios[:3] {
		fundingResults[i] = Simulate(s, areaSqFt, initialPCI, years, pci, cost, growth)
	}

	strategyResults := make([]ScenarioResult, 2)
	for i, s := range scenarios[3:5] {
		strategyResults[i] = Simulate(s, areaSqFt, initialPCI, years, pci, cost, growth)
	}

	return []Comparison{
		{Title: "Funding Levels", Scenarios: fundingResults},
		{Title: "Maintenance Strategy (50% Budget)", Scenarios: strategyResults},
	}
}
