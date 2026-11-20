package forecast_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// The tests in this file assert the mechanism of forecast.Simulate — the
// relations between its outputs that scenario.go defines — over a grid of
// strategies, budgets, cohort sets and growth rates, rather than snapshotting
// one run's numbers. A legitimate recalibration (decay rates, efficiencies,
// tier prices) leaves every one of them standing; a change to how spend,
// backlog, area or condition are accounted breaks them. The model's headline
// output end to end is snapshotted by integration's
// TestExportForecast_SummaryGolden.
//
// internal/forecast/bridge.Run calls the same Simulate, so these hold for the
// WASM forecast as well as the CLI and export.

const invariantHorizon = 15

// relTol absorbs float reassociation: distribute sums per-cohort shares of the
// budget, which can land a few ULPs either side of the budget itself.
const relTol = 1e-9

func near(got, want float64) bool {
	return math.Abs(got-want) <= relTol*math.Max(1, math.Max(math.Abs(got), math.Abs(want)))
}

// invariantTiers is a custom schedule with labels of its own, so a CostTier
// that came from DefaultCostTiers instead of the configured set shows up.
var invariantTiers = []forecast.CostTier{
	{MinPCI: 0, MaxPCI: 40, CostPerSqM: 50, Label: "reconstruction"},
	{MinPCI: 40, MaxPCI: 70, CostPerSqM: 15, Label: "rehabilitation"},
	{MinPCI: 70, MaxPCI: 101, CostPerSqM: 2, Label: "preventive"},
}

type cohortSet struct {
	name    string
	cohorts []forecast.Cohort
	tiers   []forecast.CostTier // nil = DefaultCostTiers
}

func invariantCohortSets() []cohortSet {
	return []cohortSet{
		{
			name: "hand-written",
			cohorts: []forecast.Cohort{
				{Classification: "primary", Area: 250_000, DecayRate: 0.05, InitialPCI: 85},
				{Classification: "secondary", Area: 500_000, DecayRate: 0.04, InitialPCI: 75},
				{Classification: "residential", Area: 750_000, DecayRate: 0.03, InitialPCI: 65},
			},
			tiers: invariantTiers,
		},
		{
			// The production path: per-class cohorts spread into condition
			// bands, one sidewalk class priced at its own scale.
			name: "spread",
			cohorts: forecast.SpreadCohorts([]forecast.RawCohort{
				{Classification: "primary", Area: 300_000, DecayRate: 0.025},
				{Classification: "residential", Area: 900_000, DecayRate: 0.04},
				{Classification: "sidewalks", Area: 200_000, DecayRate: 0.025,
					CostScale: forecast.ResourceCostScale("sidewalks")},
			}, 62),
		},
	}
}

func totalArea(cohorts []forecast.Cohort) float64 {
	var a float64
	for _, c := range cohorts {
		a += c.Area
	}
	return a
}

// yearOneNeed is the do-nothing year-1 need, the yardstick budgets are set
// against. Year-1 need does not depend on the scenario: it is priced on the
// decayed condition before any spend.
func yearOneNeed(cohorts []forecast.Cohort, p *forecast.Params) float64 {
	return forecast.Simulate(forecast.Scenario{Strategy: forecast.StrategyDoNothing}, cohorts, 1, p).Years[0].AnnualNeed
}

// budgetMultiples spans zero, underfunded, about funded, over need, and past
// surplus saturation (4 needs: need plus surplusSaturationNeeds).
var budgetMultiples = []float64{0, 0.1, 0.25, 0.5, 0.75, 1, 1.25, 2, 4, 8}

var fundedStrategies = []forecast.Strategy{forecast.StrategyWorstFirst, forecast.StrategyPreventiveFirst}

// checkInvariants asserts every relation that holds for any single run.
func checkInvariants(t *testing.T, s forecast.Scenario, cohorts []forecast.Cohort, growth float64, p *forecast.Params, r forecast.ScenarioResult) {
	t.Helper()
	if len(r.Years) != invariantHorizon {
		t.Fatalf("len(Years) = %d, want horizon %d", len(r.Years), invariantHorizon)
	}
	if len(r.FinalCohorts) != len(cohorts) {
		t.Fatalf("len(FinalCohorts) = %d, want one per cohort (%d)", len(r.FinalCohorts), len(cohorts))
	}
	checkYears(t, s, totalArea(cohorts), growth, p, r.Years)
	checkFinalCohorts(t, cohorts, r)
}

// checkYears asserts the per-year relations: numbering, growth, PCI bounds,
// the tier label, spend and the backlog recurrence.
func checkYears(t *testing.T, s forecast.Scenario, a0, growth float64, p *forecast.Params, years []forecast.ScenarioYear) {
	t.Helper()
	var backlog float64
	for i, y := range years {
		if y.Year != i+1 {
			t.Errorf("Years[%d].Year = %d, want %d", i, y.Year, i+1)
		}
		// Linear growth on the starting area: year t is A0*(1 + g*t).
		if want := a0 * (1 + growth*float64(i+1)); !near(y.Area, want) {
			t.Errorf("year %d: Area = %g, want A0*(1+%g*%d) = %g", y.Year, y.Area, growth, y.Year, want)
		}
		if y.PCI < 0 || y.PCI > 100 || math.IsNaN(y.PCI) {
			t.Errorf("year %d: PCI %g outside [0,100]", y.Year, y.PCI)
		}
		if want := p.Cost.TierForPCI(y.PCI); y.CostTier != want {
			t.Errorf("year %d: CostTier %q, want %q (the configured tier of PCI %g)", y.Year, y.CostTier, want, y.PCI)
		}
		if y.AnnualNeed <= 0 {
			t.Errorf("year %d: AnnualNeed = %g, want > 0 for a network with area", y.Year, y.AnnualNeed)
		}
		checkSpend(t, s, y)

		// The backlog accumulates each year's unmet need, and nothing else:
		// surplus spend never pays it down.
		backlog += math.Max(0, y.AnnualNeed-y.AnnualSpend)
		if y.DeferredBacklog != backlog {
			t.Errorf("year %d: DeferredBacklog = %g, want running sum of max(0, need-spend) = %g",
				y.Year, y.DeferredBacklog, backlog)
		}
	}
}

// checkSpend asserts one year's spend from what the strategy hands distribute.
func checkSpend(t *testing.T, s forecast.Scenario, y forecast.ScenarioYear) {
	t.Helper()
	switch {
	case s.Strategy == forecast.StrategyDoNothing:
		if y.AnnualSpend != 0 {
			t.Errorf("year %d: do-nothing spent %g, want 0", y.Year, y.AnnualSpend)
		}
	case s.FullFunding:
		if !near(y.AnnualSpend, y.AnnualNeed) {
			t.Errorf("year %d: full funding spent %g, want need %g", y.Year, y.AnnualSpend, y.AnnualNeed)
		}
	default:
		// The budget splits in proportion to need, so every cohort sees the
		// same budget/need ratio: all of it is spent up to need, and above
		// need each cohort uses at most three more needs of surplus
		// (surplusSaturationNeeds).
		want := math.Min(s.AnnualBudget, 4*y.AnnualNeed)
		if !near(y.AnnualSpend, want) {
			t.Errorf("year %d: spent %g of budget %g at need %g, want min(budget, 4*need) = %g",
				y.Year, y.AnnualSpend, s.AnnualBudget, y.AnnualNeed, want)
		}
		if y.AnnualSpend > s.AnnualBudget*(1+relTol) {
			t.Errorf("year %d: spent %g, over budget %g", y.Year, y.AnnualSpend, s.AnnualBudget)
		}
	}
}

// checkFinalCohorts asserts that the per-cohort summaries add up to the
// network figures of the years.
func checkFinalCohorts(t *testing.T, cohorts []forecast.Cohort, r forecast.ScenarioResult) {
	t.Helper()
	a0 := totalArea(cohorts)
	last := r.Years[len(r.Years)-1]
	var areaSum, pciArea, cohortSpend, cohortDeficit, spendSum float64
	for _, y := range r.Years {
		spendSum += y.AnnualSpend
	}
	for j, c := range r.FinalCohorts {
		if c.EndPCI < 0 || c.EndPCI > 100 || math.IsNaN(c.EndPCI) {
			t.Errorf("cohort %d (%s): EndPCI %g outside [0,100]", j, c.Classification, c.EndPCI)
		}
		if c.TotalSpend < 0 || c.TotalDeficit < 0 {
			t.Errorf("cohort %d (%s): negative spend %g or deficit %g", j, c.Classification, c.TotalSpend, c.TotalDeficit)
		}
		if c.Classification != cohorts[j].Classification || c.DecayRate != cohorts[j].DecayRate {
			t.Errorf("cohort %d: summary (%s, %g) does not match input (%s, %g)",
				j, c.Classification, c.DecayRate, cohorts[j].Classification, cohorts[j].DecayRate)
		}
		// Each cohort keeps its share of the grown network.
		if want := last.Area * cohorts[j].Area / a0; !near(c.Area, want) {
			t.Errorf("cohort %d (%s): Area %g, want its share of the final area %g", j, c.Classification, c.Area, want)
		}
		areaSum += c.Area
		pciArea += c.EndPCI * c.Area
		cohortSpend += c.TotalSpend
		cohortDeficit += c.TotalDeficit
	}
	if !near(areaSum, last.Area) {
		t.Errorf("cohort areas sum to %g, want the final-year area %g", areaSum, last.Area)
	}
	if got := pciArea / areaSum; !near(got, last.PCI) {
		t.Errorf("area-weighted cohort EndPCI = %g, want the final blended PCI %g", got, last.PCI)
	}
	if !near(cohortSpend, spendSum) {
		t.Errorf("cohort TotalSpend sums to %g, want the years' spend %g", cohortSpend, spendSum)
	}
	// Every cohort shares one funding ratio, so either all are short of need
	// or none is, and the per-cohort deficits add up to the network backlog.
	if !near(cohortDeficit, last.DeferredBacklog) {
		t.Errorf("cohort TotalDeficit sums to %g, want the final backlog %g", cohortDeficit, last.DeferredBacklog)
	}
}

// TestSimulate_Invariants runs every strategy across zero, underfunded,
// funded, saturated and full-funding budgets, with and without growth, and
// asserts the per-run accounting identities of checkInvariants.
func TestSimulate_Invariants(t *testing.T) {
	for _, set := range invariantCohortSets() {
		for _, growth := range []float64{0, 0.01, -0.02} {
			p := forecast.NewParams(growth, set.tiers, 12, 1)
			need1 := yearOneNeed(set.cohorts, p)

			scenarios := []forecast.Scenario{
				{Name: "do-nothing", Strategy: forecast.StrategyDoNothing},
				// FullFunding is ignored by do-nothing: it still spends nothing.
				{Name: "do-nothing-full", Strategy: forecast.StrategyDoNothing, FullFunding: true},
			}
			for _, st := range fundedStrategies {
				scenarios = append(scenarios, forecast.Scenario{Name: st.String() + "-full", Strategy: st, FullFunding: true})
				for _, m := range budgetMultiples {
					scenarios = append(scenarios, forecast.Scenario{
						Name:         fmt.Sprintf("%s-x%g", st, m),
						Strategy:     st,
						AnnualBudget: m * need1,
					})
				}
			}

			for _, s := range scenarios {
				t.Run(fmt.Sprintf("%s/growth=%g/%s", set.name, growth, s.Name), func(t *testing.T) {
					r := forecast.Simulate(s, set.cohorts, invariantHorizon, p)
					if r.Scenario != s {
						t.Errorf("result.Scenario = %+v, want the input %+v", r.Scenario, s)
					}
					checkInvariants(t, s, set.cohorts, growth, p, r)
				})
			}
		}
	}
}

// TestSimulate_FullFundingHoldsSteady: spending exactly the need holds every
// cohort at its starting condition, so the blended PCI never moves and no
// backlog accrues.
func TestSimulate_FullFundingHoldsSteady(t *testing.T) {
	for _, set := range invariantCohortSets() {
		var pci0 float64
		for _, c := range set.cohorts {
			pci0 += c.InitialPCI * c.Area
		}
		pci0 /= totalArea(set.cohorts)
		for _, st := range fundedStrategies {
			p := forecast.NewParams(0.01, set.tiers, 12, 1)
			r := forecast.Simulate(forecast.Scenario{Strategy: st, FullFunding: true}, set.cohorts, invariantHorizon, p)
			for _, y := range r.Years {
				if !near(y.PCI, pci0) || y.DeferredBacklog != 0 {
					t.Errorf("%s %s year %d: PCI %g backlog %g, want held at %g with no backlog",
						set.name, st, y.Year, y.PCI, y.DeferredBacklog, pci0)
				}
			}
		}
	}
}

// TestSimulate_DoNothingFloor: doing nothing never improves condition, and
// no budget under any strategy leaves the network worse than doing nothing or
// with more backlog.
func TestSimulate_DoNothingFloor(t *testing.T) {
	for _, set := range invariantCohortSets() {
		for _, growth := range []float64{0, 0.01} {
			p := forecast.NewParams(growth, set.tiers, 12, 1)
			dn := forecast.Simulate(forecast.Scenario{Strategy: forecast.StrategyDoNothing}, set.cohorts, invariantHorizon, p)
			for i := 1; i < len(dn.Years); i++ {
				if dn.Years[i].PCI > dn.Years[i-1].PCI {
					t.Errorf("%s growth=%g: do-nothing PCI rose from %g to %g in year %d",
						set.name, growth, dn.Years[i-1].PCI, dn.Years[i].PCI, dn.Years[i].Year)
				}
			}
			need1 := yearOneNeed(set.cohorts, p)
			for _, st := range fundedStrategies {
				for _, m := range budgetMultiples {
					r := forecast.Simulate(forecast.Scenario{Strategy: st, AnnualBudget: m * need1}, set.cohorts, invariantHorizon, p)
					for i := range r.Years {
						if r.Years[i].PCI < dn.Years[i].PCI*(1-relTol) {
							t.Errorf("%s growth=%g %s x%g year %d: PCI %g below do-nothing %g",
								set.name, growth, st, m, i+1, r.Years[i].PCI, dn.Years[i].PCI)
						}
						if r.Years[i].DeferredBacklog > dn.Years[i].DeferredBacklog*(1+relTol) {
							t.Errorf("%s growth=%g %s x%g year %d: backlog %g above do-nothing %g",
								set.name, growth, st, m, i+1, r.Years[i].DeferredBacklog, dn.Years[i].DeferredBacklog)
						}
					}
				}
			}
		}
	}
}

// TestSimulate_MonotoneInBudget: under one strategy, a larger budget never
// yields a lower PCI or a higher backlog in any year. It follows from the
// mechanism: every cohort sees the same budget/need ratio, each cohort's
// response rises with that ratio and with its own condition, and a better
// condition lowers the need, which raises the ratio the next year.
func TestSimulate_MonotoneInBudget(t *testing.T) {
	for _, set := range invariantCohortSets() {
		for _, growth := range []float64{0, 0.01} {
			p := forecast.NewParams(growth, set.tiers, 12, 1)
			need1 := yearOneNeed(set.cohorts, p)
			for _, st := range fundedStrategies {
				var prev forecast.ScenarioResult
				for k, m := range budgetMultiples {
					r := forecast.Simulate(forecast.Scenario{Strategy: st, AnnualBudget: m * need1}, set.cohorts, invariantHorizon, p)
					if k > 0 {
						for i := range r.Years {
							lo, hi := prev.Years[i], r.Years[i]
							if hi.PCI < lo.PCI*(1-relTol) {
								t.Errorf("%s growth=%g %s year %d: PCI fell from %g to %g as budget rose x%g -> x%g",
									set.name, growth, st, i+1, lo.PCI, hi.PCI, budgetMultiples[k-1], m)
							}
							if hi.DeferredBacklog > lo.DeferredBacklog*(1+relTol)+1e-9 {
								t.Errorf("%s growth=%g %s year %d: backlog rose from %g to %g as budget rose x%g -> x%g",
									set.name, growth, st, i+1, lo.DeferredBacklog, hi.DeferredBacklog, budgetMultiples[k-1], m)
							}
						}
					}
					prev = r
				}
			}
		}
	}
}

// TestSimulate_YearOneByHand pins year 1 of one cohort, computed from the
// model's definitions rather than from a previous run:
//
//   - decay: PCI1 = PCI0 * exp(-k); from 100 at k = 0.05 that is 95.12, above
//     the preventive anchor (the midpoint of [70,100], 85), so it prices at the
//     flat preventive $5/m2.
//   - need: grown area * cost / N = 120000 * 1.01 * 5 / 12 = 50500.
//   - a worst-first budget of need/8 recovers (1/8)^(1/3) = 1/2 of the year's
//     decay, so PCI = decayed + (100 - decayed)/2.
func TestSimulate_YearOneByHand(t *testing.T) {
	cohorts := []forecast.Cohort{{Classification: "residential", Area: 120_000, DecayRate: 0.05, InitialPCI: 100}}
	p := forecast.NewParams(0.01, nil, 12, 1)
	const need = 120_000 * 1.01 * 5 / 12.0
	decayed := 100 * math.Exp(-0.05)

	dn := forecast.Simulate(forecast.Scenario{Strategy: forecast.StrategyDoNothing}, cohorts, 1, p).Years[0]
	if !near(dn.AnnualNeed, need) || !near(dn.PCI, decayed) || !near(dn.DeferredBacklog, need) {
		t.Errorf("do-nothing year 1: need %g PCI %g backlog %g, want need %g PCI %g backlog %g",
			dn.AnnualNeed, dn.PCI, dn.DeferredBacklog, need, decayed, need)
	}

	wf := forecast.Simulate(forecast.Scenario{Strategy: forecast.StrategyWorstFirst, AnnualBudget: need / 8}, cohorts, 1, p).Years[0]
	wantPCI := decayed + (100-decayed)/2
	if !near(wf.AnnualSpend, need/8) || !near(wf.PCI, wantPCI) || !near(wf.DeferredBacklog, need*7/8) {
		t.Errorf("worst-first need/8 year 1: spend %g PCI %g backlog %g, want spend %g PCI %g backlog %g",
			wf.AnnualSpend, wf.PCI, wf.DeferredBacklog, need/8, wantPCI, need*7/8)
	}
}
