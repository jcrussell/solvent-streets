package forecast

import (
	"math"
	"testing"
)

// The yearly PCI response to spend is continuous and monotone across the
// spend == need boundary. It used to freeze a funded
// cohort and let any surplus close surplus/need of its gap in one year, so a
// budget 10% above the year-1 need ended at PCI 100 while spending half the
// need.

// testNeed is the hold-steady need the response tests spend against.
const testNeed = 1000.0

// responseAt is a cohort's PCI after one year at spend against testNeed,
// starting at 75 and decaying to 72 with no spend.
func responseAt(spend float64, strategy Strategy) float64 {
	st := cohortState{currentPCI: 75}
	applyCohortSpend(&st, 72, spend, testNeed, strategy)
	return st.currentPCI
}

func TestApplyCohortSpend_ContinuousAtNeed(t *testing.T) {
	const need = testNeed
	for _, s := range []Strategy{StrategyWorstFirst, StrategyPreventiveFirst} {
		below, at, above := responseAt(need-1e-6, s), responseAt(need, s), responseAt(need+1e-6, s)
		if math.Abs(below-at) > 1e-6 || math.Abs(above-at) > 1e-6 {
			t.Errorf("%s: response jumps at need: %v / %v / %v", s, below, at, above)
		}
		if at != 75 {
			t.Errorf("%s: funding the need exactly = PCI %v, want held at 75", s, at)
		}
		if got := responseAt(0, s); got != 72 {
			t.Errorf("%s: no spend = PCI %v, want the decayed 72", s, got)
		}
	}
}

func TestApplyCohortSpend_MonotoneAndBounded(t *testing.T) {
	const need = testNeed
	for _, s := range []Strategy{StrategyWorstFirst, StrategyPreventiveFirst} {
		prev := -1.0
		for spend := 0.0; spend <= 40*need; spend += need / 20 {
			got := responseAt(spend, s)
			if got < prev-1e-12 || got > maxPCI {
				t.Fatalf("%s: response at spend %v = %v after %v; want non-decreasing and <= %v", s, spend, got, prev, maxPCI)
			}
			prev = got
		}
	}
	// A second need's worth closes about 1/12 of the gap, not all of it.
	if got := responseAt(2*need, StrategyWorstFirst); got > 75+25.0/12+1e-9 {
		t.Errorf("twice the need = PCI %v, want at most %v", got, 75+25.0/12)
	}
	// Worst-first buys less per partial dollar than preventive-first.
	if wf, pf := responseAt(need/2, StrategyWorstFirst), responseAt(need/2, StrategyPreventiveFirst); wf >= pf {
		t.Errorf("half funding: worst-first %v >= preventive-first %v", wf, pf)
	}
}

// TestSimulate_NoCliffAboveNeed: end PCI and spend rise smoothly with the
// budget through the year-1 need, and final backlog never rises.
func TestSimulate_NoCliffAboveNeed(t *testing.T) {
	p := NewParams(0, nil, 12, 1)
	cohorts := []Cohort{
		{Classification: "residential", Area: 3_000_000, DecayRate: 0.04, InitialPCI: 75},
		{Classification: "primary", Area: 700_000, DecayRate: 0.025, InitialPCI: 75},
	}
	const years = 20
	y1 := Simulate(Scenario{Strategy: StrategyDoNothing}, cohorts, years, p).Years[0].AnnualNeed
	run := func(frac float64) ScenarioResult {
		return Simulate(Scenario{Strategy: StrategyWorstFirst, AnnualBudget: y1 * frac}, cohorts, years, p)
	}
	prevPCI, prevBacklog, prevSpend := -1.0, math.Inf(1), -1.0
	for frac := 0.0; frac <= 1.5+1e-9; frac += 0.05 {
		r := run(frac)
		last := r.Years[years-1]
		var spend float64
		for _, y := range r.Years {
			spend += y.AnnualSpend
		}
		if last.PCI < prevPCI-1e-9 || last.DeferredBacklog > prevBacklog+1e-6 || spend < prevSpend-1e-6 {
			t.Errorf("budget %.0f%%: end PCI %v, backlog %v, spend %v not monotone after %v / %v / %v",
				frac*100, last.PCI, last.DeferredBacklog, spend, prevPCI, prevBacklog, prevSpend)
		}
		prevPCI, prevBacklog, prevSpend = last.PCI, last.DeferredBacklog, spend
	}
	at, over := run(1.0).Years[years-1].PCI, run(1.1).Years[years-1].PCI
	if math.Abs(at-75) > 1e-6 {
		t.Errorf("funding the year-1 need = end PCI %v, want held at 75", at)
	}
	if over >= 95 {
		t.Errorf("110%% of need = end PCI %v; a 10%% surplus must not drive the network to ~100", over)
	}
}

// TestApplyCohortSpend_SurplusNeverReaches100: however large the surplus, one
// year closes at most eff/12 of the gap and records at most the saturation
// spend, so a cohort approaches PCI 100 without reaching it.
// It used to treat the whole cohort once the
// surplus reached 12/eff needs, then hold it at 100 for the rest of the run.
func TestApplyCohortSpend_SurplusNeverReaches100(t *testing.T) {
	for _, s := range []Strategy{StrategyWorstFirst, StrategyPreventiveFirst} {
		eff := surplusEfficiency(s)
		for _, mult := range []float64{10, 40, 1000, 1e9} {
			st := cohortState{currentPCI: 75}
			spent := applyCohortSpend(&st, 72, mult*testNeed, testNeed, s)
			ceiling := 75 + 25*eff/surplusGapYears
			if st.currentPCI >= maxPCI || st.currentPCI > ceiling+1e-9 {
				t.Errorf("%s at %vx need: PCI %v, want < %v (one year's ceiling)", s, mult, st.currentPCI, ceiling)
			}
			if want := testNeed * (1 + surplusSaturationNeeds); spent > want+1e-9 {
				t.Errorf("%s at %vx need: spent %v, want at most %v", s, mult, spent, want)
			}
		}
		// Thirty years of a huge surplus still leave the cohort below 100.
		st := cohortState{currentPCI: 75}
		for range 30 {
			applyCohortSpend(&st, st.currentPCI*math.Exp(-0.04), 1e6*testNeed, testNeed, s)
		}
		if st.currentPCI >= maxPCI {
			t.Errorf("%s: 30 years of surplus reached PCI %v", s, st.currentPCI)
		}
	}
}

// TestSimulate_SurplusSpendStopsAtSaturation: a budget far above need spends
// no more than each cohort can use, so total spend stops growing with the
// budget and the network stays below 100.
func TestSimulate_SurplusSpendStopsAtSaturation(t *testing.T) {
	p := NewParams(0, nil, 12, 1)
	cohorts := []Cohort{{Classification: "residential", Area: 3_000_000, DecayRate: 0.04, InitialPCI: 78}}
	const years = 30
	y1 := Simulate(Scenario{Strategy: StrategyDoNothing}, cohorts, years, p).Years[0].AnnualNeed
	total := func(r ScenarioResult) (spend float64) {
		for _, y := range r.Years {
			spend += y.AnnualSpend
		}
		return spend
	}
	for _, s := range []Strategy{StrategyWorstFirst, StrategyPreventiveFirst} {
		big := Simulate(Scenario{Strategy: s, AnnualBudget: 100 * y1}, cohorts, years, p)
		huge := Simulate(Scenario{Strategy: s, AnnualBudget: 1e6 * y1}, cohorts, years, p)
		if a, b := total(big), total(huge); math.Abs(a-b) > 1e-6*a {
			t.Errorf("%s: 100x need spends %v, 1e6x spends %v; want the same saturated spend", s, a, b)
		}
		for _, y := range huge.Years {
			if y.PCI >= maxPCI {
				t.Fatalf("%s: year %d PCI %v, want below %v", s, y.Year, y.PCI, maxPCI)
			}
			if y.AnnualSpend > 1e6*y1 {
				t.Fatalf("%s: year %d spend %v above the budget", s, y.Year, y.AnnualSpend)
			}
		}
	}
}
