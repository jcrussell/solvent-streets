package forecast

// breakEvenEpsilonFraction is the solvency tolerance for break-even search,
// expressed as a fraction of year-1 network need. Backlog spans $M to $B
// across cities, so the threshold must be relative, not an absolute dollar
// amount. A budget whose final DeferredBacklog falls below this fraction of
// year-1 need is treated as "holds the network steady."
const breakEvenEpsilonFraction = 1e-3

// breakEvenRelTol bounds the bisection: it stops once the search interval
// narrows below this fraction of the (provably sufficient) upper bound.
const breakEvenRelTol = 1e-4

// InsolvencyYear reports the first forecast year in which the cumulative
// DeferredBacklog reaches one full treatment cycle of deferred work — i.e. the
// city has deferred an entire network's worth of treatment. With the
// treatment-cycle gating in Simulate, AnnualNeed is one cycle-slice (~1/N of the
// full network), so the threshold is cycleYears * year-1 need, which restores
// the full-network dollar figure but is now *reachable* because backlog accrues
// against the small annual slice rather than tripping on the whole network in
// year 1. Because DeferredBacklog is a monotonically non-decreasing accumulator
// (scenario.go), once crossed it never recovers, so this is the civic
// "unrecoverable" threshold.
//
// A do-nothing network defers a full slice every year and crosses at ~cycleYears
// (not year 1, as in the pre-gating model that saturated Finding D). A city
// funding fraction f of each slice defers (1-f) per year and crosses at
// ~cycleYears/(1-f), so well-funded cities return ok=false ("solvent through
// horizon") and the metric discriminates among the genuinely underfunded.
// Cities near or above break-even rely on funding_gap as the primary metric
// (FundingGap), by design.
//
// year is 1-based (matching ScenarioYear.Year). ok is false when the backlog
// never reaches the threshold within the horizon or when the result has no years
// / a non-positive year-1 need. Note the three-way interaction between cycleYears,
// f, and the forecast horizon: a long cycle on a short horizon can push every
// city to ok=false. Callers should run this on a current-budget, worst-first
// scenario and pass the same cycleYears used to build the run's Params.
func InsolvencyYear(result ScenarioResult, cycleYears float64) (year int, ok bool) {
	if len(result.Years) == 0 {
		return 0, false
	}
	threshold := result.Years[0].AnnualNeed * ResolveCycleYears(cycleYears)
	if threshold <= 0 {
		return 0, false
	}
	for _, y := range result.Years {
		if y.DeferredBacklog >= threshold {
			return y.Year, true
		}
	}
	return 0, false
}

// BreakEvenBudget returns the smallest constant annual budget whose final
// DeferredBacklog falls to within breakEvenEpsilonFraction of year-1 need —
// the "hold the network steady" budget. With Simulate's treatment-cycle gating
// (annual need = full-network cost / N), this is the budget to fund the network's
// annually-scheduled treatment slice, i.e. to keep pace with the cycle. Because
// gating is a pure 1/N rescaling in dollar-space that leaves the PCI trajectory
// identical, break_even scales exactly: break_even(N) == break_even(1) / N (up to
// bisection tolerance). A 1-year cycle reproduces the legacy full-network number.
//
// Final backlog is monotone non-increasing in budget (more budget -> higher
// maintained PCI -> lower future need -> less cumulative backlog; the surplus
// branch in applyCohortSpend only pushes it further down), so the budgets
// achieving the tolerance form an upper interval and we bisect for its
// infimum. The upper bound is the peak do-nothing annual need over the
// horizon, which is provably sufficient: a funded year's need never exceeds
// the do-nothing need for that year (maintained pavement is cheaper to
// treat), so funding the peak fully funds every year -> backlog 0. Using the
// peak (not year-1 need) is what lets break-even exceed year-1 need for
// high-decay / growing networks.
//
// Returns 0 when there is no need to fund (zero years, zero area, or a
// network that already holds steady at $0).
//
// Caveat: bisection requires final backlog to be monotone non-increasing in
// budget. This holds for StrategyWorstFirst with the default (monotone) cost
// tiers — the only configuration the export uses. It is NOT guaranteed for
// StrategyPreventiveFirst (its 1.2x efficiency bonus in applyCohortSpend can
// make a larger budget leave slightly more final backlog) nor for a
// pathological custom cost_tiers curve; either can make bisection land off the
// true infimum. Pass worst-first unless you have verified monotonicity for
// your inputs. Documented in the methodology.
func BreakEvenBudget(cohorts []Cohort, years int, p *Params, strategy Strategy) float64 {
	if years <= 0 {
		return 0
	}
	doNothing := Simulate(Scenario{Strategy: StrategyDoNothing}, cohorts, years, p)
	if len(doNothing.Years) == 0 {
		return 0
	}
	upper := 0.0
	for _, y := range doNothing.Years {
		if y.AnnualNeed > upper {
			upper = y.AnnualNeed
		}
	}
	if upper <= 0 {
		// Zero-area / no-need network: nothing to fund.
		return 0
	}
	eps := breakEvenEpsilonFraction * doNothing.Years[0].AnnualNeed

	final := func(budget float64) float64 {
		r := Simulate(Scenario{AnnualBudget: budget, Strategy: strategy}, cohorts, years, p)
		return r.Years[len(r.Years)-1].DeferredBacklog
	}

	// If $0 already holds steady there is nothing to fund.
	if final(0) <= eps {
		return 0
	}

	// Invariant: final(lo) > eps, final(hi) <= eps. lo=0 satisfies the first
	// (checked above); hi=upper satisfies the second (proven sufficient).
	lo, hi := 0.0, upper
	tol := upper * breakEvenRelTol
	for hi-lo > tol {
		mid := lo + (hi-lo)/2
		if final(mid) <= eps {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

// FundingGap returns (breakEven - current) / current — the share by which the
// hold-steady budget exceeds today's budget. This is the primary discriminating
// leaderboard metric. Negative when the city is over-funded (break-even below
// current budget); not clamped, so an over-funded city ranks below an
// underfunded one. Returns 0 when current is non-positive (gap undefined
// without a configured budget).
func FundingGap(breakEven, current float64) float64 {
	if current <= 0 {
		return 0
	}
	return (breakEven - current) / current
}
