package forecast

import (
	"math"
	"testing"
)

const cycleTestArea = 100_000.0

// cycleCohorts builds a single underfunded road cohort for treatment-cycle tests.
func cycleCohorts(initialPCI, decay float64) []Cohort {
	return []Cohort{{
		Classification: "residential",
		Area:           cycleTestArea,
		DecayRate:      decay,
		InitialPCI:     initialPCI,
	}}
}

// TestResolveCycleYears_GuardsZero proves the load-bearing divide-by-zero guard:
// an unset (0) or negative cycle resolves to the default rather than producing
// 1/0 = +Inf need. Without this, the WASM/test paths (which build Params via
// NewParams without config normalization) would poison every simulation.
func TestResolveCycleYears_GuardsZero(t *testing.T) {
	for _, in := range []float64{0, -5, -0.0001} {
		if got := ResolveCycleYears(in); got != DefaultTreatmentCycleYears {
			t.Errorf("ResolveCycleYears(%g) = %g, want default %g", in, got, DefaultTreatmentCycleYears)
		}
	}
	if got := ResolveCycleYears(8); got != 8 {
		t.Errorf("ResolveCycleYears(8) = %g, want 8", got)
	}

	// End-to-end: a zero-cycle Params must not yield NaN/Inf in the output.
	res := Simulate(Scenario{Strategy: StrategyDoNothing}, cycleCohorts(70, 0.05), 5, NewParams(0.01, nil, 0))
	for _, y := range res.Years {
		if math.IsInf(y.AnnualNeed, 0) || math.IsNaN(y.AnnualNeed) {
			t.Fatalf("year %d AnnualNeed is Inf/NaN (%g) — zero-cycle guard failed", y.Year, y.AnnualNeed)
		}
	}
}

// TestSimulate_AnnualNeedGatedByCycle proves the core gating: annual need scales
// as 1/N because only one cycle-slice of the network is scheduled per year.
func TestSimulate_AnnualNeedGatedByCycle(t *testing.T) {
	cohorts := cycleCohorts(70, 0.05)
	dn := Scenario{Strategy: StrategyDoNothing}

	one := Simulate(dn, cohorts, 20, NewParams(0.01, nil, 1))
	twelve := Simulate(dn, cohorts, 20, NewParams(0.01, nil, 12))

	for i := range one.Years {
		want := one.Years[i].AnnualNeed / 12
		got := twelve.Years[i].AnnualNeed
		if math.Abs(got-want) > 1e-6*math.Abs(want) {
			t.Fatalf("year %d: gated need %g, want need(N=1)/12 = %g", i+1, got, want)
		}
	}
}

// TestSimulate_DollarsScaleButPCIIdentical is the heart of the rescaling
// argument: at a fixed budget scaled by 1/N, the PCI trajectory is bit-identical
// across cycle lengths while the dollar columns scale by 1/N. This is what makes
// break_even(N) == break_even(1)/N exact. (A naive "full funding holds steady"
// test would be a tautology — full funding freezes PCI at any N.)
func TestSimulate_DollarsScaleButPCIIdentical(t *testing.T) {
	cohorts := cycleCohorts(70, 0.05)
	const budget1 = 600_000.0
	strategy := StrategyWorstFirst

	one := Simulate(Scenario{AnnualBudget: budget1, Strategy: strategy}, cohorts, 20, NewParams(0.01, nil, 1))
	// Scale BOTH need (via N) and budget by 1/12; the PCI path must not move.
	twelve := Simulate(Scenario{AnnualBudget: budget1 / 12, Strategy: strategy}, cohorts, 20, NewParams(0.01, nil, 12))

	for i := range one.Years {
		if math.Abs(one.Years[i].PCI-twelve.Years[i].PCI) > 1e-9 {
			t.Fatalf("year %d: PCI diverged across cycle lengths: N=1 %.12g vs N=12 %.12g",
				i+1, one.Years[i].PCI, twelve.Years[i].PCI)
		}
		want := one.Years[i].DeferredBacklog / 12
		if got := twelve.Years[i].DeferredBacklog; math.Abs(got-want) > 1e-6*math.Abs(want)+1e-9 {
			t.Fatalf("year %d: backlog %g, want backlog(N=1)/12 = %g", i+1, got, want)
		}
	}
}

// TestBreakEvenBudget_ScalesInverselyWithCycle pins the headline correctness
// property: the hold-steady budget under an N-year cycle is the un-gated budget
// divided by N (within bisection tolerance).
func TestBreakEvenBudget_ScalesInverselyWithCycle(t *testing.T) {
	cohorts := cycleCohorts(65, 0.08)
	years := 20

	be1 := BreakEvenBudget(cohorts, years, NewParams(0.01, nil, 1), StrategyWorstFirst)
	be12 := BreakEvenBudget(cohorts, years, NewParams(0.01, nil, 12), StrategyWorstFirst)
	if be1 <= 0 || be12 <= 0 {
		t.Fatalf("break-even should be positive: be1=%g be12=%g", be1, be12)
	}

	want := be1 / 12
	// Relative tolerance ~1e-3: break-even bisection itself stops at relTol 1e-4,
	// so demand a slightly looser bound than exact equality.
	if math.Abs(be12-want) > 1e-3*want {
		t.Fatalf("break-even did not scale 1/N: be(12)=%g, want be(1)/12=%g", be12, want)
	}
}

// TestBreakEvenBudget_MonotoneInBudget guards the property BreakEvenBudget's
// bisection relies on: with the cycle, final backlog is still non-increasing in
// budget (gating is a positive-constant rescaling, so monotonicity is inherited).
func TestBreakEvenBudget_MonotoneInBudget(t *testing.T) {
	cohorts := cycleCohorts(65, 0.08)
	years := 20
	p := NewParams(0.01, nil, 12)

	dn := Simulate(Scenario{Strategy: StrategyDoNothing}, cohorts, years, p)
	var upper float64
	for _, y := range dn.Years {
		if y.AnnualNeed > upper {
			upper = y.AnnualNeed
		}
	}

	finalAt := func(budget float64) float64 {
		r := Simulate(Scenario{AnnualBudget: budget, Strategy: StrategyWorstFirst}, cohorts, years, p)
		return r.Years[len(r.Years)-1].DeferredBacklog
	}

	prev := math.Inf(1)
	for step := range 21 {
		budget := upper * float64(step) / 20
		got := finalAt(budget)
		if got > prev+1e-6 {
			t.Fatalf("final backlog rose with budget: at budget %g got %g > prev %g", budget, got, prev)
		}
		prev = got
	}
}

// TestInsolvencyYear_DiscriminatesUnderfunding proves the redefined threshold no
// longer saturates at year 2 (Finding D) and orders cities by how underfunded
// they are. Both cities are deeply underfunded so both cross within the horizon;
// the more-underfunded one must cross strictly earlier.
func TestInsolvencyYear_DiscriminatesUnderfunding(t *testing.T) {
	cohorts := cycleCohorts(60, 0.10)
	years := 30
	cycle := 12.0
	p := NewParams(0.01, nil, cycle)

	be := BreakEvenBudget(cohorts, years, p, StrategyWorstFirst)
	if be <= 0 {
		t.Fatalf("expected positive break-even, got %g", be)
	}

	run := func(frac float64) (int, bool) {
		r := Simulate(Scenario{AnnualBudget: be * frac, Strategy: StrategyWorstFirst}, cohorts, years, p)
		return InsolvencyYear(r, cycle)
	}

	badYear, badOK := run(0.10)   // barely funded
	lessYear, lessOK := run(0.40) // still underfunded, but less so
	if !badOK || !lessOK {
		t.Fatalf("both deeply-underfunded cities should go insolvent within horizon: bad=%v less=%v", badOK, lessOK)
	}
	if badYear >= lessYear {
		t.Fatalf("more-underfunded city should go insolvent earlier: bad=%d (10%%) vs less=%d (40%%)", badYear, lessYear)
	}

	// A well-funded city should stay solvent through the horizon (ok=false), so
	// funding_gap — not insolvency_year — discriminates on the funded side.
	if yr, ok := run(1.0); ok {
		t.Fatalf("a city funded at break-even should be solvent through horizon, got insolvency year %d", yr)
	}
}

// TestSimulate_CycleOne_MatchesLegacy is the back-compat guard: at N=1 the gating
// factor is 1, so the year-1 do-nothing need equals the full-network retreatment
// cost computed directly — the pre-gating behavior.
func TestSimulate_CycleOne_MatchesLegacy(t *testing.T) {
	const initialPCI, decay, growth = 70.0, 0.05, 0.01
	p := NewParams(growth, nil, 1)
	res := Simulate(Scenario{Strategy: StrategyDoNothing}, cycleCohorts(initialPCI, decay), 5, p)

	// Year-1: area grows by one step, PCI decays one year, full area is priced.
	year1Area := cycleTestArea * (1 + growth*1)
	decayedPCI := initialPCI * math.Exp(-decay*1)
	want := p.Cost.ProjectCost(year1Area, decayedPCI)

	if got := res.Years[0].AnnualNeed; math.Abs(got-want) > 1e-6*want {
		t.Fatalf("N=1 year-1 need %g should equal full-network cost %g (no gating)", got, want)
	}
}
