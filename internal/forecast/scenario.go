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
	AreaSqM         float64 `json:"area_sqm"`
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
// cohortState tracks per-cohort PCI during simulation.
type cohortState struct {
	forecaster *ExponentialPCIForecaster
	currentPCI float64
	areaFrac   float64 // fraction of total area
}

func initCohortStates(cohorts []Cohort) ([]cohortState, float64) {
	var totalArea float64
	for _, c := range cohorts {
		totalArea += c.AreaSqM
	}
	states := make([]cohortState, len(cohorts))
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
	return states, totalArea
}

const maxPCI = 100.0

// distributeBudget distributes totalSpend across cohorts proportional to need,
// applies PCI recovery, and returns the actual total spend.
func distributeBudget(states []cohortState, cohortDecayed, cohortNeed []float64,
	totalNeed, totalSpend float64, strategy Strategy,
	cohortSpendAcc, cohortDeficitAcc []float64) float64 {
	actualSpend := 0.0
	for j := range states {
		decayedPCI := cohortDecayed[j]
		cohortSpend := 0.0
		if totalNeed > 0 {
			cohortSpend = totalSpend * (cohortNeed[j] / totalNeed)
		}

		actual := applyCohortSpend(&states[j], decayedPCI, cohortSpend, cohortNeed[j], strategy)
		actualSpend += actual
		cohortSpendAcc[j] += actual
		cohortDeficitAcc[j] += math.Max(0, cohortNeed[j]-actual)
	}
	return actualSpend
}

func applyCohortSpend(st *cohortState, decayedPCI, spend, need float64, strategy Strategy) float64 {
	if spend <= 0 || need <= 0 {
		st.currentPCI = decayedPCI
		return 0
	}
	if spend >= need {
		actual := need
		surplus := spend - need
		if surplus > 0 {
			usableSurplus := math.Min(surplus, need)
			improveFraction := usableSurplus / need
			st.currentPCI = math.Min(maxPCI, st.currentPCI+(maxPCI-st.currentPCI)*improveFraction)
			actual += usableSurplus
		}
		return actual
	}
	spendRatio := spend / need
	efficiency := 1.0
	switch strategy {
	case StrategyDoNothing:
		// efficiency stays 1.0; spend will be 0 for do-nothing scenarios
	case StrategyPreventiveFirst:
		// 1.2× efficiency: preventive maintenance yields better cost-effectiveness
		// per FHWA-HIF-12-042 "Pavement Preservation" (Fig. 3) — every $1 of
		// preventive work avoids $6-10 in future reconstruction.
		efficiency = 1.2
	case StrategyWorstFirst:
		// 0.8× efficiency: worst-first reconstruction is less cost-effective
		// per unit spend because it addresses pavement already past its
		// economic service life (FHWA-HIF-12-042, Section 3.2).
		efficiency = 0.8
	}
	recovery := (st.currentPCI - decayedPCI) * spendRatio * efficiency
	st.currentPCI = math.Min(maxPCI, decayedPCI+recovery)
	return spend
}

func blendedPCI(states []cohortState) float64 {
	var pci float64
	for j := range states {
		pci += states[j].currentPCI * states[j].areaFrac
	}
	return pci
}

// Simulate runs one scenario over the given number of years with per-cohort
// decay rates. Each cohort decays independently; budget is distributed
// proportional to need; PCI is area-weighted blended.
func Simulate(s Scenario, cohorts []Cohort, years int,
	cost *TieredCostProjector, growth *LinearGrowthEstimator) ScenarioResult {
	states, totalArea := initCohortStates(cohorts)
	n := len(cohorts)
	areaValues := growth.EstimateGrowth(totalArea, years)

	result := ScenarioResult{
		Scenario: s,
		Years:    make([]ScenarioYear, years),
	}

	var deferredBacklog float64
	cohortSpendAcc := make([]float64, n)
	cohortDeficitAcc := make([]float64, n)

	for i := range years {
		area := areaValues[i]

		cohortDecayed := make([]float64, n)
		cohortNeed := make([]float64, n)
		var totalNeed float64

		for j := range states {
			decayed := states[j].forecaster.Forecast(states[j].currentPCI, 1)
			cohortDecayed[j] = decayed[0]
			need := cost.ProjectCost(area*states[j].areaFrac, cohortDecayed[j])
			cohortNeed[j] = need
			totalNeed += need
		}

		var totalSpend float64
		switch s.Strategy {
		case StrategyDoNothing:
			totalSpend = 0
		case StrategyWorstFirst, StrategyPreventiveFirst:
			if s.FullFunding {
				totalSpend = totalNeed
			} else {
				totalSpend = s.AnnualBudget
			}
		}

		totalSpend = distributeBudget(states, cohortDecayed, cohortNeed,
			totalNeed, totalSpend, s.Strategy, cohortSpendAcc, cohortDeficitAcc)

		deferredBacklog += math.Max(0, totalNeed-totalSpend)

		result.Years[i] = ScenarioYear{
			Year:            i + 1,
			PCI:             blendedPCI(states),
			AreaSqM:         area,
			AnnualNeed:      totalNeed,
			AnnualSpend:     totalSpend,
			DeferredBacklog: deferredBacklog,
			CostTier:        TierForPCI(blendedPCI(states)),
		}
	}

	result.FinalCohorts = make([]CohortSummary, n)
	for j, c := range cohorts {
		result.FinalCohorts[j] = CohortSummary{
			Classification: c.Classification,
			EndPCI:         states[j].currentPCI,
			AreaSqM:        c.AreaSqM,
			DecayRate:      c.DecayRate,
			TotalSpend:     cohortSpendAcc[j],
			TotalDeficit:   cohortDeficitAcc[j],
		}
	}

	return result
}
