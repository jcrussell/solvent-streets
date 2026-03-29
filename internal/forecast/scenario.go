package forecast

import (
	"fmt"
	"math"
	"strings"
)

// Strategy determines how maintenance budget is allocated.
type Strategy int

const (
	StrategyDoNothing       Strategy = iota
	StrategyWorstFirst               // prioritize lowest-PCI segments (reconstruction first)
	StrategyPreventiveFirst          // prioritize highest-PCI segments (preventive first)
)

func (s Strategy) String() string {
	switch s {
	case StrategyDoNothing:
		return "do-nothing"
	case StrategyWorstFirst:
		return "worst-first"
	case StrategyPreventiveFirst:
		return "preventive-first"
	default:
		return fmt.Sprintf("strategy(%d)", int(s))
	}
}

// ParseStrategy converts a string to a Strategy.
func ParseStrategy(s string) (Strategy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "do-nothing", "donothing":
		return StrategyDoNothing, nil
	case "worst-first", "worstfirst":
		return StrategyWorstFirst, nil
	case "preventive-first", "preventivefirst":
		return StrategyPreventiveFirst, nil
	default:
		return 0, fmt.Errorf("unknown strategy: %q", s)
	}
}

// Scenario describes a maintenance funding scenario.
type Scenario struct {
	Name         string   `json:"name"`
	Label        string   `json:"label"`
	AnnualBudget float64  `json:"annual_budget"`
	FullFunding  bool     `json:"full_funding,omitempty"`
	Strategy     Strategy `json:"strategy"`
}

// ScenarioYear holds the projected state for a single year within a scenario.
type ScenarioYear struct {
	Year            int     `json:"year"`
	PCI             float64 `json:"pci"`
	AreaSqM        float64 `json:"area_sqm"`
	AnnualNeed      float64 `json:"annual_need"`
	AnnualSpend     float64 `json:"annual_spend"`
	DeferredBacklog float64 `json:"deferred_backlog"`
	CostTier        string  `json:"cost_tier"`
}

// ScenarioResult is the full output of running a scenario simulation.
type ScenarioResult struct {
	Scenario     Scenario        `json:"scenario"`
	Years        []ScenarioYear  `json:"years"`
	FinalCohorts []CohortSummary `json:"final_cohorts,omitempty"`
}

// Simulate runs one scenario over the given number of years with per-cohort
// decay rates. Each cohort decays independently; budget is distributed
// proportional to need; PCI is area-weighted blended.
func Simulate(s Scenario, cohorts []Cohort, years int,
	cost *TieredCostProjector, growth *LinearGrowthEstimator) ScenarioResult {

	n := len(cohorts)

	// Total area for growth estimation
	var totalArea float64
	for _, c := range cohorts {
		totalArea += c.AreaSqM
	}
	areaValues := growth.EstimateGrowth(totalArea, years)

	// Per-cohort state
	type cohortState struct {
		forecaster *ExponentialPCIForecaster
		currentPCI float64
		areaFrac   float64 // fraction of total area
	}
	states := make([]cohortState, n)
	for i, c := range cohorts {
		frac := 0.0
		if totalArea > 0 {
			frac = c.AreaSqM / totalArea
		}
		states[i] = cohortState{
			forecaster: &ExponentialPCIForecaster{DecayRate: c.DecayRate},
			currentPCI: c.InitialPCI,
			areaFrac:   frac,
		}
	}

	result := ScenarioResult{
		Scenario: s,
		Years:    make([]ScenarioYear, years),
	}

	var deferredBacklog float64
	cohortSpendAcc := make([]float64, n)
	cohortDeficitAcc := make([]float64, n)

	for i := range years {
		area := areaValues[i]

		// Per-cohort decay and need
		cohortDecayed := make([]float64, n)
		cohortNeed := make([]float64, n)
		var totalNeed float64

		for j := range states {
			decayed := states[j].forecaster.Forecast(states[j].currentPCI, 1)
			cohortDecayed[j] = decayed[0]
			cohortArea := area * states[j].areaFrac
			need := cost.ProjectCost(cohortArea, cohortDecayed[j])
			cohortNeed[j] = need
			totalNeed += need
		}

		// Determine total spend
		var totalSpend float64
		switch s.Strategy {
		case StrategyDoNothing:
			totalSpend = 0
		default:
			if s.FullFunding {
				totalSpend = totalNeed
			} else {
				// Allow spend to exceed need (surplus used for improvement)
				totalSpend = s.AnnualBudget
			}
		}

		// Distribute budget proportional to need and apply recovery per cohort
		const maxPCI = 100.0
		actualSpend := 0.0
		for j := range states {
			decayedPCI := cohortDecayed[j]
			cohortSpend := 0.0
			if totalNeed > 0 {
				cohortSpend = totalSpend * (cohortNeed[j] / totalNeed)
			}

			var actualCohortSpend float64
			if cohortSpend > 0 && cohortNeed[j] > 0 {
				if cohortSpend >= cohortNeed[j] {
					// Full funding for this cohort: restore PCI to pre-decay level.
					// Surplus improves toward 100.
					actualCohortSpend = cohortNeed[j]
					surplus := cohortSpend - cohortNeed[j]
					if surplus > 0 {
						usableSurplus := math.Min(surplus, cohortNeed[j])
						improveFraction := usableSurplus / cohortNeed[j]
						states[j].currentPCI = math.Min(maxPCI, states[j].currentPCI+(maxPCI-states[j].currentPCI)*improveFraction)
						actualCohortSpend += usableSurplus
					}
				} else {
					spendRatio := cohortSpend / cohortNeed[j]
					efficiency := 1.0
					switch s.Strategy {
					case StrategyPreventiveFirst:
						efficiency = 1.2
					case StrategyWorstFirst:
						efficiency = 0.8
					}
					recovery := (states[j].currentPCI - decayedPCI) * spendRatio * efficiency
					states[j].currentPCI = math.Min(maxPCI, decayedPCI+recovery)
					actualCohortSpend = cohortSpend
				}
			} else {
				states[j].currentPCI = decayedPCI
			}
			actualSpend += actualCohortSpend
			cohortSpendAcc[j] += actualCohortSpend
			cohortDeficitAcc[j] += math.Max(0, cohortNeed[j]-actualCohortSpend)
		}
		totalSpend = actualSpend

		// Blended area-weighted PCI
		var blendedPCI float64
		for j := range states {
			blendedPCI += states[j].currentPCI * states[j].areaFrac
		}

		deferredBacklog += math.Max(0, totalNeed-totalSpend)

		result.Years[i] = ScenarioYear{
			Year:            i + 1,
			PCI:             blendedPCI,
			AreaSqM:        area,
			AnnualNeed:      totalNeed,
			AnnualSpend:     totalSpend,
			DeferredBacklog: deferredBacklog,
			CostTier:        TierForPCI(blendedPCI),
		}
	}

	// Build final cohort summaries
	result.FinalCohorts = make([]CohortSummary, n)
	for j, c := range cohorts {
		result.FinalCohorts[j] = CohortSummary{
			Classification: c.Classification,
			EndPCI:         states[j].currentPCI,
			AreaSqM:       c.AreaSqM,
			DecayRate:      c.DecayRate,
			TotalSpend:     cohortSpendAcc[j],
			TotalDeficit:   cohortDeficitAcc[j],
		}
	}

	return result
}
