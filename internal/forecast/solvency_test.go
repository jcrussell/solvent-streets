package forecast

import "testing"

// solvencyCohorts builds a single road cohort for solvency tests. A high decay
// rate and low starting PCI keep the network firmly underfunded so the metrics
// have something to measure.
func solvencyCohorts(area, initialPCI, decay float64) []Cohort {
	return []Cohort{{
		Classification: "residential",
		Area:           area,
		DecayRate:      decay,
		InitialPCI:     initialPCI,
	}}
}

func TestInsolvencyYear_DoNothingCrossesYearOne(t *testing.T) {
	cohorts := solvencyCohorts(100_000, 70, 0.08)
	p := NewParams(0.01, nil)
	res := Simulate(Scenario{Strategy: StrategyDoNothing}, cohorts, 20, p)

	year, ok := InsolvencyYear(res)
	if !ok {
		t.Fatal("do-nothing scenario should be insolvent within the horizon")
	}
	if year != 1 {
		t.Fatalf("do-nothing defers the entire year-1 need, so insolvency year should be 1, got %d", year)
	}
}

func TestInsolvencyYear_FullFundingNeverInsolvent(t *testing.T) {
	cohorts := solvencyCohorts(100_000, 70, 0.08)
	p := NewParams(0.01, nil)
	res := Simulate(Scenario{FullFunding: true, Strategy: StrategyWorstFirst}, cohorts, 20, p)

	if year, ok := InsolvencyYear(res); ok {
		t.Fatalf("a fully funded network should stay solvent through the horizon, got insolvency year %d", year)
	}
}

func TestInsolvencyYear_NoYears(t *testing.T) {
	if _, ok := InsolvencyYear(ScenarioResult{}); ok {
		t.Fatal("empty result should report not-ok")
	}
}

func TestBreakEvenBudget_HoldsNetworkSteady(t *testing.T) {
	cohorts := solvencyCohorts(100_000, 70, 0.08)
	years := 20
	p := NewParams(0.01, nil)

	be := BreakEvenBudget(cohorts, years, p, StrategyWorstFirst)
	if be <= 0 {
		t.Fatalf("break-even should be positive for an underfunded network, got %g", be)
	}

	// year-1 need sets the relative tolerance the search targets.
	doNothing := Simulate(Scenario{Strategy: StrategyDoNothing}, cohorts, years, p)
	year1Need := doNothing.Years[0].AnnualNeed
	eps := breakEvenEpsilonFraction * year1Need

	finalAt := func(budget float64) float64 {
		r := Simulate(Scenario{AnnualBudget: budget, Strategy: StrategyWorstFirst}, cohorts, years, p)
		return r.Years[len(r.Years)-1].DeferredBacklog
	}

	if got := finalAt(be); got > eps {
		t.Fatalf("at break-even budget %g, final backlog %g should be <= eps %g", be, got, eps)
	}

	// Infimum check: a budget meaningfully below break-even must leave the
	// network insolvent (final backlog above tolerance). Step down by 5%.
	if got := finalAt(be * 0.95); got <= eps {
		t.Fatalf("break-even %g is not the infimum: 5%% less still holds steady (backlog %g <= eps %g)", be, got, eps)
	}
}

func TestBreakEvenBudget_ExceedsYearOneNeedForGrowingNetwork(t *testing.T) {
	// High decay + steady area growth makes later-year do-nothing need exceed
	// year-1 need, so the hold-steady budget must fund the peak — exceeding
	// year-1 need. This exercises the max-over-horizon upper bound (a year-1
	// upper bound would be provably too low here).
	cohorts := solvencyCohorts(100_000, 60, 0.12)
	years := 25
	p := NewParams(0.03, nil) // 3%/yr growth

	doNothing := Simulate(Scenario{Strategy: StrategyDoNothing}, cohorts, years, p)
	year1Need := doNothing.Years[0].AnnualNeed

	be := BreakEvenBudget(cohorts, years, p, StrategyWorstFirst)
	if be <= year1Need {
		t.Fatalf("for a high-decay growing network, break-even %g should exceed year-1 need %g", be, year1Need)
	}
}

func TestBreakEvenBudget_ZeroAreaReturnsZero(t *testing.T) {
	cohorts := solvencyCohorts(0, 70, 0.08)
	p := NewParams(0.01, nil)
	if be := BreakEvenBudget(cohorts, 20, p, StrategyWorstFirst); be != 0 {
		t.Fatalf("zero-area network needs no funding, break-even should be 0, got %g", be)
	}
}

func TestBreakEvenBudget_ZeroYearsReturnsZero(t *testing.T) {
	cohorts := solvencyCohorts(100_000, 70, 0.08)
	p := NewParams(0.01, nil)
	if be := BreakEvenBudget(cohorts, 0, p, StrategyWorstFirst); be != 0 {
		t.Fatalf("zero-horizon break-even should be 0, got %g", be)
	}
}

func TestFundingGap(t *testing.T) {
	if g := FundingGap(150, 100); g != 0.5 {
		t.Fatalf("expected gap 0.5, got %g", g)
	}
	if g := FundingGap(80, 100); g != -0.2 {
		t.Fatalf("over-funded city should have negative gap -0.2, got %g", g)
	}
	if g := FundingGap(150, 0); g != 0 {
		t.Fatalf("undefined gap (no current budget) should be 0, got %g", g)
	}
}
