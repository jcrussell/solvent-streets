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
	Area            float64 `json:"area"`
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

// cohortState tracks per-cohort PCI during simulation.
type cohortState struct {
	forecaster *ExponentialPCIForecaster
	currentPCI float64
	areaFrac   float64 // fraction of total area
}

// simulator holds the per-cohort state and scratch/accumulator slices for one
// scenario run. decayed and need are scratch, overwritten each simulated year;
// spendAcc and deficitAcc accumulate across years into the final CohortSummary.
type simulator struct {
	states     []cohortState
	decayed    []float64 // per-year scratch: post-decay PCI per cohort
	need       []float64 // per-year scratch: maintenance need per cohort
	spendAcc   []float64 // cumulative actual spend per cohort
	deficitAcc []float64 // cumulative unmet need per cohort
}

func initCohortStates(cohorts []Cohort) ([]cohortState, float64) {
	var totalArea float64
	for _, c := range cohorts {
		totalArea += c.Area
	}
	states := make([]cohortState, len(cohorts))
	for i, c := range cohorts {
		frac := 0.0
		if totalArea > 0 {
			frac = c.Area / totalArea
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

// DefaultTreatmentCycleYears is the assumed pavement treatment cycle N: each
// simulated year roughly 1/N of the network is scheduled for treatment, so the
// annual need is the full-network retreatment cost divided by N. ~12 yr is the
// midpoint of the 10-14 yr band documented in docs/validation.md §5 (Finding B);
// before this gating the model priced the entire network every year (an implicit
// 1-year cycle), overstating break_even 3.6x-9.6x.
const DefaultTreatmentCycleYears = 12.0

// ResolveCycleYears applies DefaultTreatmentCycleYears when the configured value
// is unset (<= 0). This is the load-bearing guard: Params is built directly via
// NewParams on the WASM bridge and in tests without passing through config
// normalization, so resolving here (not in config) guarantees Simulate never
// divides by zero (1/0 = +Inf would poison every cohort's need).
func ResolveCycleYears(years float64) float64 {
	if years <= 0 {
		return DefaultTreatmentCycleYears
	}
	return years
}

// distribute spreads totalSpend across cohorts proportional to need, applies
// PCI recovery, accumulates per-cohort spend and deficit, and returns the
// actual total spend. Reads this year's scratch (decayed, need) and mutates
// states plus the cumulative accumulators.
//
// fullFund selects exact allocation instead of the proportional split: each
// cohort is handed its own need_j rather than totalSpend * (need_j/totalNeed).
// With totalSpend == totalNeed the two are the same number in exact arithmetic,
// but not in floating point, and the difference is not cosmetic — it decides
// which side of applyCohortSpend's `spend >= need` test each cohort lands on,
// and the two sides model different things (full restoration vs. recovering
// only spendRatio*efficiency of the year's decay). Round-tripping through the
// ratio put ~40% of cohort-years on the wrong side, so "full funding" quietly
// under-restored them and the blended PCI moved ~0.5 points purely with the
// cost scale. See solvent-streets-0ulx.
func (sm *simulator) distribute(totalNeed, totalSpend float64, strategy Strategy, fullFund bool) float64 {
	actualSpend := 0.0
	for j := range sm.states {
		cohortSpend := 0.0
		switch {
		case fullFund:
			cohortSpend = sm.need[j]
		case totalNeed > 0:
			cohortSpend = totalSpend * (sm.need[j] / totalNeed)
		}

		actual := applyCohortSpend(&sm.states[j], sm.decayed[j], cohortSpend, sm.need[j], strategy)
		actualSpend += actual
		sm.spendAcc[j] += actual
		sm.deficitAcc[j] += math.Max(0, sm.need[j]-actual)
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
			// The 2x cap (spend beyond 2*need buys nothing) is a deliberate
			// diminishing-returns model, not an oversight, and the surplus it
			// discards is intentionally not redistributed — see
			// solvent-streets-z3fo. For the cap alone, redistribution is a
			// no-op: distribute() allocates spend_j proportional to need_j
			// (spend_j = totalSpend * need_j/totalNeed), so when totalNeed > 0
			// the ratio spend_j/need_j = totalSpend/totalNeed is identical for
			// every cohort. The cap triggers exactly when that shared ratio
			// exceeds 2, so either all cohorts saturate — leaving no
			// unsaturated cohort to receive the remainder — or none do.
			//
			// distribute()'s second allocation path (fullFund: spend_j =
			// need_j exactly) preserves this. There the shared ratio is 1 for
			// every cohort, so it never exceeds 2 and the cap is unreachable —
			// surplus is 0 and this whole block is skipped.
			//
			// Two caveats. (1) The claim is scoped to this cap; it does NOT
			// cover the maxPCI clamp below, where a cohort already at PCI 100
			// still adds its full usableSurplus to actual for zero PCI gain
			// while under-100 cohorts stay under-funded. That is a reachable
			// mixed state in which redistribution would change outcomes.
			// (2) The proof is an invariant of distribute()'s proportional
			// allocation, asserted here at the callee and pinned by no test.
			// Strategy is documented as prioritizing lowest/highest-PCI
			// segments and today only scales an efficiency multiplier; if
			// allocation ever becomes priority-ordered, the uniform ratio —
			// and this proof — dies silently.
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
func Simulate(s Scenario, cohorts []Cohort, years int, p *Params) ScenarioResult {
	// Clamp negative years to 0 (house style: clamp-in-place, not error-return);
	// make([]ScenarioYear, years) panics on a negative length. EstimateGrowth
	// below already self-clamps, so this is the remaining panic site.
	if years < 0 {
		years = 0
	}
	states, totalArea := initCohortStates(cohorts)
	n := len(cohorts)
	sm := &simulator{
		states:     states,
		decayed:    make([]float64, n),
		need:       make([]float64, n),
		spendAcc:   make([]float64, n),
		deficitAcc: make([]float64, n),
	}
	areaValues := p.Growth.EstimateGrowth(totalArea, years)

	result := ScenarioResult{
		Scenario: s,
		Years:    make([]ScenarioYear, years),
	}

	var deferredBacklog float64

	// eligibleFrac gates annual need to one treatment-cycle slice: only ~1/N of
	// the network is scheduled for treatment each year, so the annual need is the
	// full-network retreatment cost / N. Budget-independent and PCI-independent,
	// so it is a pure 1/N rescaling in dollar-space that preserves the
	// PCI trajectory and BreakEvenBudget's monotonicity (solvency.go). MVP uses a
	// single global cycle; per-class cycles would move this inside the loop keyed
	// on cohorts[j].Classification.
	eligibleFrac := 1.0 / ResolveCycleYears(p.CycleYears)

	for i := range years {
		area := areaValues[i]

		var totalNeed float64
		for j := range sm.states {
			sm.decayed[j] = sm.states[j].forecaster.decayOneStep(sm.states[j].currentPCI)
			fullNeed := p.Cost.ProjectCost(area*sm.states[j].areaFrac, sm.decayed[j])
			need := fullNeed * eligibleFrac
			sm.need[j] = need
			totalNeed += need
		}

		var totalSpend float64
		// fullFund is derived here rather than read off s.FullFunding inside
		// distribute: StrategyDoNothing forces totalSpend to 0 even when
		// FullFunding is set, and exact allocation must not resurrect spending
		// on that path.
		fullFund := false
		switch s.Strategy {
		case StrategyDoNothing:
			totalSpend = 0
		case StrategyWorstFirst, StrategyPreventiveFirst:
			if s.FullFunding {
				totalSpend = totalNeed
				fullFund = true
			} else {
				totalSpend = s.AnnualBudget
			}
		}

		totalSpend = sm.distribute(totalNeed, totalSpend, s.Strategy, fullFund)

		deferredBacklog += math.Max(0, totalNeed-totalSpend)

		blended := blendedPCI(sm.states)
		result.Years[i] = ScenarioYear{
			Year:            i + 1,
			PCI:             blended,
			Area:            area,
			AnnualNeed:      totalNeed,
			AnnualSpend:     totalSpend,
			DeferredBacklog: deferredBacklog,
			// Label from the active tier set (custom/sidewalk), not the hardcoded
			// defaults, so the label matches ProjectCost's dollar figures above.
			CostTier: p.Cost.TierForPCI(blended),
		}
	}

	// FinalCohorts reports each cohort's terminal (final-year, grown) area rather
	// than its year-0 area. This is NOT the basis TotalSpend was computed on —
	// spend accumulates against every year's area, so the consistent denominator
	// for a $/m² would be the area-integral mean, (1/N)*Σ_i areaValues[i]*areaFrac
	// (factor 1 + r(N+1)/2 = 1.055*A₀ at r=0.01, N=10). Year-N area is chosen
	// because it is unambiguous and matches EndPCI's terminal semantics, at a
	// known cost: it is the larger denominator (1.10*A₀), so a $/m² derived from
	// this summary is *understated* by ~4.1%, where reporting year-0 (1.00*A₀,
	// the smaller denominator) *overstated* it by ~5.5%. The two errors have
	// opposite sign and are not equal in magnitude.
	//
	// With years == 0 nothing was simulated and areaValues is empty, so fall back
	// to the year-0 area. No production path reaches years == 0 — bridge.
	// validateInput rejects it (bridge.go, in.Years <= 0) and game.go guards
	// horizon <= 0 before calling — so this guard exists to keep Simulate total
	// for tests and direct library use, not to serve a live caller.
	result.FinalCohorts = make([]CohortSummary, n)
	for j, c := range cohorts {
		area := c.Area
		if len(areaValues) > 0 {
			area = areaValues[len(areaValues)-1] * sm.states[j].areaFrac
		}
		result.FinalCohorts[j] = CohortSummary{
			Classification: c.Classification,
			EndPCI:         sm.states[j].currentPCI,
			Area:           area,
			DecayRate:      c.DecayRate,
			TotalSpend:     sm.spendAcc[j],
			TotalDeficit:   sm.deficitAcc[j],
		}
	}

	return result
}
