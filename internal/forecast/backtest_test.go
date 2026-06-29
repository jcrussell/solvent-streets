package forecast

import (
	"math"
	"testing"
)

// backtestCity is a representative committed fixture approximating a real Bay
// Area city's network scale and condition (validation.md §5). It is NOT exact
// per-segment data — that needs an ingested+computed DB and is out of scope for
// an offline gate. The point is to lock the §5 gap-closure: with the treatment
// cycle, break_even is a realistic multiple of the stated budget (single digits,
// often ~1), not the ~12× overstatement the 1-year-cycle model produced.
type backtestCity struct {
	name          string
	initialPCI    float64
	decay         float64
	areaSqM       float64 // approx paved roadway area
	currentBudget float64 // cited annual pavement budget
}

// fixtures are tuned so their N=1 (un-gated) break_even lands near the
// validation.md §5 "break_even" column, i.e. an order of magnitude above the
// city's actual budget — reproducing the pre-fix overstatement. After 1/12
// gating they should fall into a plausible hold-steady band.
var backtestCities = []backtestCity{
	// Berkeley: PCI ~56, ~$15M/yr budget, MTC hold-steady ~$18M (validation §5).
	{name: "berkeley", initialPCI: 56, decay: 0.045, areaSqM: 3_300_000, currentBudget: 15_000_000},
	// Dublin: good condition (PCI ~78), small network, ~$3.7M/yr.
	{name: "dublin", initialPCI: 78, decay: 0.035, areaSqM: 1_900_000, currentBudget: 3_700_000},
}

const backtestYears = 25 // matches examples/bay-area-ca/pvmt.toml

// TestBacktest_TreatmentCycleClosesSec5Gap is the offline regression gate for the
// solvency-dollar-fidelity epic (qehh). For each representative city it proves:
//  1. gated break_even == un-gated break_even / N (the core fix is applied), and
//  2. gated break_even / current_budget is a realistic single-digit multiple —
//     the §5 overstatement (3.6×–9.6× against reality, and ~12× as a raw ratio)
//     is closed.
//
// If this fails, either the gating regressed (property 1) or a fixture/cost
// default drifted enough to reopen the gap (property 2) — read the logged ratios
// before adjusting.
func TestBacktest_TreatmentCycleClosesSec5Gap(t *testing.T) {
	const cycle = DefaultTreatmentCycleYears

	for _, c := range backtestCities {
		t.Run(c.name, func(t *testing.T) {
			// Spread the mean PCI exactly as the shipped export/CLI/WASM paths do
			// (ApplyConditionSpread), so this gate validates the real pipeline.
			// The 1/N relationship (property 1) factors through the spread
			// unchanged; the gated dollar figure rises by the §4 condition-spread
			// uplift, which property 2's band still tolerates.
			cohorts := ApplyConditionSpread([]Cohort{{
				Classification: "residential",
				Area:           c.areaSqM,
				DecayRate:      c.decay,
				InitialPCI:     c.initialPCI,
			}})

			ungated := BreakEvenBudget(cohorts, backtestYears, NewParams(0.005, nil, 1), StrategyWorstFirst)
			gated := BreakEvenBudget(cohorts, backtestYears, NewParams(0.005, nil, cycle), StrategyWorstFirst)

			// Property 1: exact 1/N relationship (within bisection tolerance).
			want := ungated / cycle
			if math.Abs(gated-want) > 1e-3*want {
				t.Fatalf("%s: gated break_even %g != ungated/N %g — gating not applied as 1/N", c.name, gated, want)
			}

			// Property 2: the §5 overstatement is closed. The un-gated ratio is
			// ~cycle× too high; the gated ratio must be single-digit.
			ungatedRatio := ungated / c.currentBudget
			gatedRatio := gated / c.currentBudget
			t.Logf("%s: break_even ungated $%.1fM (%.1f× budget) -> gated $%.1fM (%.1f× budget); current $%.1fM",
				c.name, ungated/1e6, ungatedRatio, gated/1e6, gatedRatio, c.currentBudget/1e6)

			if ungatedRatio < 4 {
				t.Fatalf("%s: fixture no longer reproduces the §5 overstatement (ungated ratio %.1f < 4); retune the fixture", c.name, ungatedRatio)
			}
			if gatedRatio > 5 {
				t.Errorf("%s: gated break_even is still %.1f× the budget — §5 gap not closed (expected single-digit, ~1)", c.name, gatedRatio)
			}

			// Property 3 (cost×cycle calibration gate, epic g98u): break_even per m²
			// must track the cited real hold-steady spend, pinning the otherwise-
			// degenerate cost×N balance (break_even(N)==break_even(1)/N exactly, so
			// cost and cycle trade off freely; only an external anchor fixes them).
			// Berkeley's StreetSaver PMP implies ~$5.6/m²·yr ($18.3M hold-steady ÷
			// 3.3M m²). The independent degeneracy-breaker is the *physical* paving
			// cadence (miles, not dollars): 31.9 of 448.7 lane-mi/yr ≈ 14.1yr at the
			// current $15M, so the hold-steady cadence is 14.1×15/18.3 ≈ 11.6yr ≈ the
			// model's N=12. Bare cost tiers × N=12 reproduce $5.6; loaded tiers (§3,
			// ~2.5×) would need an un-physical N≈29. This bracket trips if a future
			// cost-regime or cycle change breaks that validated balance. Berkeley is
			// the only city with a clean {budget, hold-steady $/m²·yr, cycle} triplet
			// (§5), so the per-m² anchor is asserted for it alone.
			if c.name == "berkeley" {
				perM2 := gated / c.areaSqM
				t.Logf("berkeley: break_even = $%.2f/m²·yr (cited real hold-steady ~$5.6)", perM2)
				if perM2 < 4.5 || perM2 > 6.5 {
					t.Errorf("berkeley break_even = $%.2f/m²·yr, outside [$4.5,$6.5] bracketing the cited $5.6/m²·yr reality yardstick — the bare-cost × N=12 calibration drifted", perM2)
				}
			}
		})
	}
}
