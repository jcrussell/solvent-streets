package forecast

import (
	"math"
	"testing"
)

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

func TestMaterialsForArea(t *testing.T) {
	// rehab tier: 120 kg/m^2 mix at 5.5% binder over 1000 m^2.
	tier := MaterialTier{Label: "rehab", MixKgPerSqM: 120, BinderShare: 0.055}
	got := MaterialsForArea(1000, tier)

	// mix = 1000 * 120 / 1000 = 120 t; binder = 120 * 0.055 = 6.6 t.
	if !approxEqual(got.MixTonnes, 120) {
		t.Errorf("MixTonnes = %v, want 120", got.MixTonnes)
	}
	if !approxEqual(got.BinderTonnes, 6.6) {
		t.Errorf("BinderTonnes = %v, want 6.6", got.BinderTonnes)
	}
	// oil = binder tonnes * BarrelsPerTonBinder.
	if !approxEqual(got.OilBarrels, 6.6*BarrelsPerTonBinder) {
		t.Errorf("OilBarrels = %v, want %v", got.OilBarrels, 6.6*BarrelsPerTonBinder)
	}
}

func TestMaterialsForAreaZero(t *testing.T) {
	got := MaterialsForArea(0, DefaultMaterialTiers[0])
	if got != (MaterialQuantities{}) {
		t.Errorf("zero area = %+v, want all zero", got)
	}
}

func TestMaterialsForAreaNegative(t *testing.T) {
	// A negative area is floored at 0 (house style: clamp-in-place) so it can
	// never yield negative tonnages or oil barrels.
	got := MaterialsForArea(-1000, DefaultMaterialTiers[1])
	if got != (MaterialQuantities{}) {
		t.Errorf("negative area = %+v, want all zero", got)
	}
}

func TestDefaultMaterialTiersAlignWithCostTiers(t *testing.T) {
	// The default material bands are the default cost bands, label for label,
	// so the Materials tab names the treatment the cost model prices.
	if len(DefaultMaterialTiers) != len(DefaultCostTiers) {
		t.Fatalf("%d material tiers, %d cost tiers", len(DefaultMaterialTiers), len(DefaultCostTiers))
	}
	for _, ct := range DefaultCostTiers {
		mt, _ := MaterialTierForPCI(DefaultMaterialTiers, ct.MinPCI)
		if mt.Label != ct.Label || mt.MinPCI != ct.MinPCI || mt.MaxPCI != ct.MaxPCI {
			t.Errorf("cost tier %q [%v,%v) resolves to material tier %q [%v,%v)",
				ct.Label, ct.MinPCI, ct.MaxPCI, mt.Label, mt.MinPCI, mt.MaxPCI)
		}
	}
	// Material intensity falls as condition improves.
	prev, _ := MaterialTierForPCI(DefaultMaterialTiers, 85)
	rehab, _ := MaterialTierForPCI(DefaultMaterialTiers, 55)
	recon, _ := MaterialTierForPCI(DefaultMaterialTiers, 20)
	if !(prev.MixKgPerSqM < rehab.MixKgPerSqM && rehab.MixKgPerSqM < recon.MixKgPerSqM) {
		t.Errorf("mix intensity not monotonic: %v %v %v", prev.MixKgPerSqM, rehab.MixKgPerSqM, recon.MixKgPerSqM)
	}
}

func TestMaterialTierForPCI(t *testing.T) {
	for _, tc := range []struct {
		pci  float64
		want string
	}{
		{0, "reconstruction"}, {39.9, "reconstruction"}, {40, "rehab"}, {69.9, "rehab"},
		{70, "preventive"}, {100, "preventive"}, {-5, "reconstruction"}, {150, "preventive"},
	} {
		if got, ok := MaterialTierForPCI(DefaultMaterialTiers, tc.pci); !ok || got.Label != tc.want {
			t.Errorf("PCI %v: got %q (ok %v), want %q", tc.pci, got.Label, ok, tc.want)
		}
	}
	// A gap resolves to the nearest band.
	gappy := []MaterialTier{{Label: "low", MinPCI: 0, MaxPCI: 30}, {Label: "high", MinPCI: 60, MaxPCI: MaxTierPCI}}
	if got, _ := MaterialTierForPCI(gappy, 50); got.Label != "high" {
		t.Errorf("PCI 50 in a gap: got %q, want the nearer high", got.Label)
	}
	if _, ok := MaterialTierForPCI(nil, 50); ok {
		t.Errorf("ok = true for empty tier set, want false")
	}
}

// WithMaterials prices each slice the year's spending treated at the tier of
// its own condition, takes the asphalt share of it and splits the mix by
// tier. It leaves its input alone.
func TestWithMaterials(t *testing.T) {
	in := ScenarioResult{Years: []ScenarioYear{
		{Year: 1, treated: []treatedSlice{{pci: 55, area: 6000}, {pci: 85, area: 4000}}},
		{Year: 2},
	}}
	const share = 0.75
	got := WithMaterials(in, DefaultMaterialTiers, share)

	if in.Years[0].Materials != nil {
		t.Fatal("WithMaterials wrote to its input's years")
	}
	rehab, _ := MaterialTierForPCI(DefaultMaterialTiers, 55)
	prev, _ := MaterialTierForPCI(DefaultMaterialTiers, 85)
	r, pv := MaterialsForArea(6000*share, rehab), MaterialsForArea(4000*share, prev)
	y := got.Years[0]
	if y.Materials == nil || math.Abs(y.Materials.MixTonnes-(r.MixTonnes+pv.MixTonnes)) > 1e-9 ||
		math.Abs(y.Materials.BinderTonnes-(r.BinderTonnes+pv.BinderTonnes)) > 1e-9 {
		t.Errorf("year 1: got %+v, want the rehab %+v plus the preventive %+v", y.Materials, r, pv)
	}
	want := []TierMix{{Tier: prev.Label, MixTonnes: pv.MixTonnes}, {Tier: rehab.Label, MixTonnes: r.MixTonnes}}
	if len(y.MaterialsByTier) != 2 {
		t.Fatalf("year 1 by tier: %+v, want %+v", y.MaterialsByTier, want)
	}
	for _, w := range want {
		found := false
		for _, g := range y.MaterialsByTier {
			if g.Tier == w.Tier && math.Abs(g.MixTonnes-w.MixTonnes) < 1e-9 {
				found = true
			}
		}
		if !found {
			t.Errorf("year 1 by tier: %+v lacks %+v", y.MaterialsByTier, w)
		}
	}
	// A year that treated nothing lays nothing.
	if z := got.Years[1]; z.Materials == nil || *z.Materials != (MaterialQuantities{}) || len(z.MaterialsByTier) != 0 {
		t.Errorf("year 2 treated nothing, yet laid %+v by tier %+v", z.Materials, z.MaterialsByTier)
	}
}

// TestWithMaterials_FollowsSpending: material is what the spending lays. Doing
// nothing lays none; full funding lays its whole scheduled slice, spread over
// the conditions it is in; a part-funded run lays less than full funding.
// A city whose cost tiers are named and banded its own way (Los Angeles
// County's Good/Fair/Poor/Failed) still gets material by condition, not
// every year at the heaviest tier (Agoura Hills once read 53,655 t/yr).
func TestWithMaterials_FollowsSpending(t *testing.T) {
	tiers := []CostTier{
		{MinPCI: 0, MaxPCI: 25, CostPerSqM: 200, Label: "Failed"},
		{MinPCI: 25, MaxPCI: 50, CostPerSqM: 120, Label: "Poor"},
		{MinPCI: 50, MaxPCI: 70, CostPerSqM: 60, Label: "Fair"},
		{MinPCI: 70, MaxPCI: 100, CostPerSqM: 15, Label: "Good"},
	}
	p := NewParams(0, tiers, 12, 1)
	cohorts := SpreadCohorts([]RawCohort{{Classification: "residential", Area: 1_000_000, DecayRate: 0.04}}, 85)
	run := func(s Scenario) ScenarioResult {
		return WithMaterials(Simulate(s, cohorts, 30, p), DefaultMaterialTiers, 1)
	}
	none := run(Scenario{Strategy: StrategyDoNothing})
	full := run(Scenario{Strategy: StrategyWorstFirst, FullFunding: true})
	half := run(Scenario{Strategy: StrategyWorstFirst, AnnualBudget: full.Years[0].AnnualNeed / 2})

	for _, y := range none.Years {
		if y.Materials.MixTonnes != 0 {
			t.Fatalf("Do Nothing year %d lays %v t", y.Year, y.Materials.MixTonnes)
		}
	}
	slice := 1_000_000.0 / 12
	lightest, heaviest := slice*30/1000, slice*480/1000
	if got := full.Years[0].Materials.MixTonnes; got <= lightest || got >= heaviest/2 {
		t.Errorf("full funding year 1 at PCI ~85: %v t, want between the all-preventive %v t and half the all-reconstruction %v t", got, lightest, heaviest)
	}
	if h, f := half.Years[0].Materials.MixTonnes, full.Years[0].Materials.MixTonnes; h <= 0 || h >= f {
		t.Errorf("half funding year 1 lays %v t against full funding's %v t", h, f)
	}
}

// TestWithMaterials_Smooth: a network sliding through the tier bands at part
// funding lays material that moves year to year as its condition does. Priced
// at the network-average PCI, it quadrupled in the year the average crossed
// 40 (Livermore, year 18).
func TestWithMaterials_Smooth(t *testing.T) {
	p := NewParams(0, nil, 12, 1)
	cohorts := SpreadCohorts([]RawCohort{{Classification: "residential", Area: 1_000_000, DecayRate: 0.04}}, 75)
	need := Simulate(Scenario{Strategy: StrategyWorstFirst, FullFunding: true}, cohorts, 1, p).Years[0].AnnualNeed
	r := WithMaterials(Simulate(Scenario{Strategy: StrategyWorstFirst, AnnualBudget: need / 10}, cohorts, 30, p), DefaultMaterialTiers, 1)
	if last := r.Years[29].PCI; last >= 40 {
		t.Fatalf("fixture: the run ends at PCI %v, never crossing into reconstruction", last)
	}
	for i := 1; i < len(r.Years); i++ {
		a, b := r.Years[i-1].Materials.MixTonnes, r.Years[i].Materials.MixTonnes
		if a > 0 && (b/a > 1.25 || b/a < 0.8) {
			t.Errorf("year %d to %d: mix %v t to %v t", i, i+1, a, b)
		}
	}
}

// TestWithMaterials_TierFollowsShownPCI: the site shows PCI as a whole
// number, so a slice at 69.995 shows "70" and takes the preventive tier whose
// band starts there, and one at 69.4 shows "69" and stays rehab.
func TestWithMaterials_TierFollowsShownPCI(t *testing.T) {
	in := ScenarioResult{Years: []ScenarioYear{
		{Year: 1, treated: []treatedSlice{{pci: 69.995, area: 1000}}},
		{Year: 2, treated: []treatedSlice{{pci: 69.4, area: 1000}}},
	}}
	got := WithMaterials(in, DefaultMaterialTiers, 1)
	tierOf := func(y ScenarioYear) string {
		if len(y.MaterialsByTier) != 1 {
			t.Fatalf("year %d by tier %+v, want one tier", y.Year, y.MaterialsByTier)
		}
		return y.MaterialsByTier[0].Tier
	}
	if tier := tierOf(got.Years[0]); tier != TierPreventive {
		t.Errorf("PCI 69.995 (shown as 70): tier %q, want %q", tier, TierPreventive)
	}
	prev, _ := MaterialTierForPCI(DefaultMaterialTiers, 70)
	if want := MaterialsForArea(1000, prev); *got.Years[0].Materials != want {
		t.Errorf("PCI 69.995: materials %+v, want the preventive %+v", *got.Years[0].Materials, want)
	}
	if tier := tierOf(got.Years[1]); tier != TierRehab {
		t.Errorf("PCI 69.4 (shown as 69): tier %q, want %q", tier, TierRehab)
	}
}
