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
	Strategy     Strategy `json:"strategy"`
}

// ScenarioYear holds the projected state for a single year within a scenario.
type ScenarioYear struct {
	Year            int     `json:"year"`
	PCI             float64 `json:"pci"`
	AreaSqFt        float64 `json:"area_sqft"`
	AnnualNeed      float64 `json:"annual_need"`
	AnnualSpend     float64 `json:"annual_spend"`
	DeferredBacklog float64 `json:"deferred_backlog"`
	CostTier        string  `json:"cost_tier"`
}

// ScenarioResult is the full output of running a scenario simulation.
type ScenarioResult struct {
	Scenario Scenario       `json:"scenario"`
	Years    []ScenarioYear `json:"years"`
}

// Simulate runs one scenario over the given number of years.
// It reuses the existing forecaster components for PCI decay, cost projection,
// and area growth estimation.
func Simulate(s Scenario, areaSqFt, initialPCI float64, years int,
	pci *ExponentialPCIForecaster, cost *TieredCostProjector,
	growth *LinearGrowthEstimator) ScenarioResult {

	areaValues := growth.EstimateGrowth(areaSqFt, years)

	result := ScenarioResult{
		Scenario: s,
		Years:    make([]ScenarioYear, years),
	}

	var deferredBacklog float64
	currentPCI := initialPCI

	for i := range years {
		// Decay from current (possibly recovered) PCI — one year of decay
		decayed := pci.Forecast(currentPCI, 1)
		decayedPCI := decayed[0]

		area := areaValues[i]
		annualNeed := cost.ProjectCost(area, decayedPCI)
		tier := TierForPCI(decayedPCI)

		var spend float64
		switch s.Strategy {
		case StrategyDoNothing:
			spend = 0
		default:
			if s.AnnualBudget <= 0 {
				spend = annualNeed
			} else {
				spend = math.Min(s.AnnualBudget, annualNeed)
			}
		}

		// PCI recovery proportional to spend, with strategy-dependent efficiency
		if spend > 0 && annualNeed > 0 {
			if spend >= annualNeed {
				// Full funding: recover fully regardless of strategy
				currentPCI = initialPCI
			} else {
				spendRatio := spend / annualNeed
				efficiency := 1.0
				if s.Strategy == StrategyPreventiveFirst {
					efficiency = 1.2
				} else if s.Strategy == StrategyWorstFirst {
					efficiency = 0.8
				}
				recovery := (initialPCI - decayedPCI) * spendRatio * efficiency
				currentPCI = math.Min(initialPCI, decayedPCI+recovery)
			}
		} else {
			currentPCI = decayedPCI
		}

		deferredBacklog += annualNeed - spend

		result.Years[i] = ScenarioYear{
			Year:            i + 1,
			PCI:             currentPCI,
			AreaSqFt:        area,
			AnnualNeed:      annualNeed,
			AnnualSpend:     spend,
			DeferredBacklog: deferredBacklog,
			CostTier:        tier,
		}
	}

	return result
}
