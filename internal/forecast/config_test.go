package forecast

import (
	"math"
	"testing"
)

// TestDefaultSidewalkTiersMatchRatio pins the invariant that
// DefaultSidewalkCostTiers is exactly SidewalkCostRatio× DefaultCostTiers in
// every band. If the default tiers are retuned, this keeps SidewalkCostRatio
// (used to derive sidewalk tiers from custom road tiers) honest.
func TestDefaultSidewalkTiersMatchRatio(t *testing.T) {
	if len(DefaultSidewalkCostTiers) != len(DefaultCostTiers) {
		t.Fatalf("tier count mismatch: road %d, sidewalk %d", len(DefaultCostTiers), len(DefaultSidewalkCostTiers))
	}
	for i := range DefaultCostTiers {
		want := DefaultCostTiers[i].CostPerSqM * SidewalkCostRatio
		got := DefaultSidewalkCostTiers[i].CostPerSqM
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("band %d: sidewalk cost %.4f, want %.4f (road %.2f × %.2f)",
				i, got, want, DefaultCostTiers[i].CostPerSqM, SidewalkCostRatio)
		}
	}
}

func TestSidewalkCostTiers_NoCustom_UsesDefaults(t *testing.T) {
	got := sidewalkCostTiers(nil)
	if len(got) != len(DefaultSidewalkCostTiers) {
		t.Fatalf("got %d tiers, want %d", len(got), len(DefaultSidewalkCostTiers))
	}
	for i := range got {
		if got[i].CostPerSqM != DefaultSidewalkCostTiers[i].CostPerSqM {
			t.Errorf("band %d: got %.2f, want default %.2f", i, got[i].CostPerSqM, DefaultSidewalkCostTiers[i].CostPerSqM)
		}
	}
}

// TestSidewalkCostTiers_CustomScaledNotFullPrice pins the LA/Boston fix: custom
// config cost_tiers (a road schedule) must be scaled by SidewalkCostRatio for
// sidewalks, not applied at full road price.
func TestSidewalkCostTiers_CustomScaledNotFullPrice(t *testing.T) {
	custom := []CostTier{
		{MinPCI: 70, MaxPCI: 100, CostPerSqM: 15.0, Label: "Good"},
		{MinPCI: 50, MaxPCI: 70, CostPerSqM: 60.0, Label: "Fair"},
		{MinPCI: 0, MaxPCI: 50, CostPerSqM: 200.0, Label: "Failed"},
	}
	got := sidewalkCostTiers(custom)
	for i := range custom {
		want := custom[i].CostPerSqM * SidewalkCostRatio
		if math.Abs(got[i].CostPerSqM-want) > 1e-9 {
			t.Errorf("band %d: got %.2f, want %.2f (scaled)", i, got[i].CostPerSqM, want)
		}
		if got[i].CostPerSqM == custom[i].CostPerSqM {
			t.Errorf("band %d: sidewalk priced at full road cost %.2f (the bug)", i, custom[i].CostPerSqM)
		}
	}
	// input must not be mutated
	if custom[2].CostPerSqM != 200.0 {
		t.Errorf("input tiers mutated: %.2f", custom[2].CostPerSqM)
	}
}

// TestNewParamsForResource_SidewalkEffIsDiscounted is the end-to-end check:
// with custom road tiers, the sidewalk projected cost at any PCI is
// SidewalkCostRatio× the road projected cost — i.e. eff $/m² ratio ≈ 0.6, the
// value the sidewalk discount stands for.
func TestNewParamsForResource_SidewalkEffIsDiscounted(t *testing.T) {
	custom := []CostTier{
		{MinPCI: 70, MaxPCI: 100, CostPerSqM: 15.0, Label: "Good"},
		{MinPCI: 50, MaxPCI: 70, CostPerSqM: 60.0, Label: "Fair"},
		{MinPCI: 25, MaxPCI: 50, CostPerSqM: 120.0, Label: "Poor"},
		{MinPCI: 0, MaxPCI: 25, CostPerSqM: 200.0, Label: "Failed"},
	}
	road := NewParams(0.0, custom, 1, 1)
	side := NewParamsForResource("sidewalks", 0.0, custom, 1, 1)
	for _, pci := range []float64{20, 45, 62, 85} {
		r := road.Cost.ProjectCost(1000, pci)
		s := side.Cost.ProjectCost(1000, pci)
		if r == 0 {
			t.Fatalf("road cost zero at pci %.0f", pci)
		}
		ratio := s / r
		if math.Abs(ratio-SidewalkCostRatio) > 1e-9 {
			t.Errorf("pci %.0f: sidewalk/road eff ratio %.4f, want %.2f", pci, ratio, SidewalkCostRatio)
		}
	}
}

// A sidewalk cohort costs the same whether it is simulated on its own under
// NewParamsForResource or blended with other resources under NewParams with
// its ResourceCostScale: one sidewalk rate, applied once.
func TestResourceCostScale_BlendMatchesPerResourcePricing(t *testing.T) {
	const years = 10
	doNothing := Scenario{Name: "baseline", Strategy: StrategyDoNothing}
	custom := []CostTier{
		{MinPCI: 70, MaxPCI: 101, CostPerSqM: 8, Label: "preventive"},
		{MinPCI: 40, MaxPCI: 70, CostPerSqM: 60, Label: "rehab"},
		{MinPCI: 0, MaxPCI: 40, CostPerSqM: 200, Label: "reconstruction"},
	}
	for name, tiers := range map[string][]CostTier{"default tiers": nil, "custom tiers": custom} {
		road := RawCohort{Classification: "residential", Area: 5000, DecayRate: 0.04}
		walk := RawCohort{Classification: "sidewalks", Area: 2000, DecayRate: 0.025}

		roadAlone := Simulate(doNothing, SpreadCohorts([]RawCohort{road}, 70), years, NewParamsForResource("roads", 0, tiers, 12, 1.3))
		walkAlone := Simulate(doNothing, SpreadCohorts([]RawCohort{walk}, 70), years, NewParamsForResource("sidewalks", 0, tiers, 12, 1.3))

		walk.CostScale = ResourceCostScale("sidewalks")
		road.CostScale = ResourceCostScale("roads")
		blend := Simulate(doNothing, SpreadCohorts([]RawCohort{road, walk}, 70), years, NewParams(0, tiers, 12, 1.3))

		for y := range years {
			want := roadAlone.Years[y].AnnualNeed + walkAlone.Years[y].AnnualNeed
			if got := blend.Years[y].AnnualNeed; math.Abs(got-want) > 1e-6*want {
				t.Errorf("%s, year %d: blended need %.4f, per-resource sum %.4f", name, y+1, got, want)
			}
		}

		// Unscaled, the blend prices the sidewalk at road cost: the figure the
		// per-resource forecast never showed.
		walk.CostScale = 0
		unscaled := Simulate(doNothing, SpreadCohorts([]RawCohort{road, walk}, 70), years, NewParams(0, tiers, 12, 1.3))
		if unscaled.Years[0].AnnualNeed <= blend.Years[0].AnnualNeed {
			t.Errorf("%s: an unscaled sidewalk cohort should cost more than a scaled one", name)
		}
	}
}

func TestResourceCostScale(t *testing.T) {
	for rt, want := range map[string]float64{"sidewalks": SidewalkCostRatio, "roads": 1, "parking": 1, "": 1} {
		if got := ResourceCostScale(rt); got != want {
			t.Errorf("ResourceCostScale(%q) = %v, want %v", rt, got, want)
		}
	}
}
