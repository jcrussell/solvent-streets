package forecast

import (
	"math"
	"testing"
)

// TestOverhead_ScalesLinearlyAndExactlyOnce is the load-bearing property: the
// multiplier must be applied to every priced dollar, and applied once. A path
// that misses it prices at bare (the truth divided by the overhead); a path
// that applies it twice prices at overhead squared. Both are plausible-looking
// numbers, so neither would be caught by eyeballing an export.
func TestOverhead_ScalesLinearlyAndExactlyOnce(t *testing.T) {
	tiers := []CostTier{
		{MinPCI: 70, MaxPCI: 101, CostPerSqM: 5, Label: "preventive"},
		{MinPCI: 40, MaxPCI: 70, CostPerSqM: 50, Label: "rehab"},
		{MinPCI: 0, MaxPCI: 40, CostPerSqM: 150, Label: "reconstruction"},
	}
	const area = 1_000_000

	for _, pci := range []float64{15, 45, 55, 85, 100} {
		bare := (&TieredCostProjector{Tiers: tiers, Overhead: 1}).ProjectCost(area, pci)
		for _, oh := range []float64{1.5, 2, 2.5} {
			got := (&TieredCostProjector{Tiers: tiers, Overhead: oh}).ProjectCost(area, pci)
			want := bare * oh
			if math.Abs(got-want) > 1e-6*math.Abs(want) {
				t.Errorf("PCI %g overhead %g: cost = %g, want %g (bare %g x %g)", pci, oh, got, want, bare, oh)
			}
		}
	}
}

// TestOverhead_NonPositiveMeansBareNotDefault pins the deliberate asymmetry with
// config: this package resolves 0 to 1.0, NOT to config.DefaultCostOverhead.
//
// Changing it to default here would break two things at once. A projector built
// without going through config — which is what every test and the parity golden
// do — would silently acquire the config default. And a zero-value projector
// would price at that default rather than failing loudly, so a construction
// site that forgot to pass the overhead would move dollars instead of erroring.
//
// The default is 1.0 today, so both divergences currently measure zero. That is
// not a reason to relax the rule: it makes the breakage invisible until the
// default next moves off 1.0, which is precisely when it would matter.
func TestOverhead_NonPositiveMeansBareNotDefault(t *testing.T) {
	tiers := []CostTier{{MinPCI: 0, MaxPCI: 101, CostPerSqM: 10, Label: "flat"}}
	want := (&TieredCostProjector{Tiers: tiers, Overhead: 1}).ProjectCost(1000, 50)

	for _, oh := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		got := (&TieredCostProjector{Tiers: tiers, Overhead: oh}).ProjectCost(1000, 50)
		if got != want {
			t.Errorf("overhead %v: cost = %g, want %g (bare)", oh, got, want)
		}
	}
}

// TestOverhead_ComposesWithSidewalkRatio: ADA compliance, design and
// contingency load a sidewalk project the same way they load a road project, so
// the overhead applies to both. It commutes with SidewalkCostRatio, so the
// order the two are applied in must not matter.
func TestOverhead_ComposesWithSidewalkRatio(t *testing.T) {
	tiers := []CostTier{{MinPCI: 0, MaxPCI: 101, CostPerSqM: 100, Label: "flat"}}
	const oh = 1.5

	road := NewParams(0, tiers, 12, oh)
	walk := NewParamsForResource("sidewalks", 0, tiers, 12, oh)

	roadCost := road.Cost.ProjectCost(1000, 50)
	walkCost := walk.Cost.ProjectCost(1000, 50)

	want := roadCost * SidewalkCostRatio
	if math.Abs(walkCost-want) > 1e-9*math.Abs(want) {
		t.Errorf("sidewalk cost = %g, want %g (road %g x %g)", walkCost, want, roadCost, SidewalkCostRatio)
	}
	// And it is loaded, not bare — a sidewalk path that dropped the overhead
	// would still satisfy the ratio above against a bare road cost.
	bareWalk := NewParamsForResource("sidewalks", 0, tiers, 12, 1).Cost.ProjectCost(1000, 50)
	if math.Abs(walkCost-bareWalk*oh) > 1e-9*walkCost {
		t.Errorf("sidewalk cost = %g, want %g (bare sidewalk %g x %g)", walkCost, bareWalk*oh, bareWalk, oh)
	}
}

// TestOverhead_DefaultTiersUnchanged: the multiplier must not mutate the shared
// package-level tier tables. They are package vars, so scaling in place would
// leak across every projector in the process and compound per construction.
func TestOverhead_DefaultTiersUnchanged(t *testing.T) {
	before := make([]float64, len(DefaultCostTiers))
	for i, tier := range DefaultCostTiers {
		before[i] = tier.CostPerSqM
	}

	for range 3 {
		(&TieredCostProjector{Overhead: 2}).ProjectCost(1000, 50)
	}

	for i, tier := range DefaultCostTiers {
		if tier.CostPerSqM != before[i] {
			t.Errorf("DefaultCostTiers[%d].CostPerSqM = %g, want %g — the overhead mutated the shared table",
				i, tier.CostPerSqM, before[i])
		}
	}
}
