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
	if !approxEqual(got.AggregateTonnes, 120-6.6) {
		t.Errorf("AggregateTonnes = %v, want %v", got.AggregateTonnes, 120-6.6)
	}
	// binder + aggregate must reconstitute the mix exactly.
	if !approxEqual(got.BinderTonnes+got.AggregateTonnes, got.MixTonnes) {
		t.Errorf("binder+aggregate = %v, want mix %v", got.BinderTonnes+got.AggregateTonnes, got.MixTonnes)
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

// WithMaterials treats one cycle-slice of the asphalt share of each year's
// area at the tier of the year's blended PCI, whatever the cost tier is
// called, and leaves its input alone.
func TestWithMaterials(t *testing.T) {
	in := ScenarioResult{Years: []ScenarioYear{
		{Year: 1, Area: 120000, PCI: 55, CostTier: "rehab"},
		{Year: 2, Area: 120000, PCI: 85, CostTier: "Good"},
	}}
	const cycle, share = 12.0, 0.75
	got := WithMaterials(in, DefaultMaterialTiers, cycle, share)

	if in.Years[0].Materials != nil {
		t.Fatal("WithMaterials wrote to its input's years")
	}
	rehab, _ := MaterialTierForPCI(DefaultMaterialTiers, 55)
	prev, _ := MaterialTierForPCI(DefaultMaterialTiers, 85)
	wants := []MaterialQuantities{
		MaterialsForArea(120000*share/cycle, rehab),
		MaterialsForArea(120000*share/cycle, prev),
	}
	for i, want := range wants {
		if got.Years[i].Materials == nil || *got.Years[i].Materials != want {
			t.Errorf("year %d: got %+v, want %+v", i+1, got.Years[i].Materials, want)
		}
	}
	if wants[0].MixTonnes != 900 { // 7500 m^2 at 120 kg/m^2
		t.Errorf("rehab year mix = %v t, want 900", wants[0].MixTonnes)
	}

	// An unset cycle is the default cycle, as it is for costs.
	unset := WithMaterials(in, DefaultMaterialTiers, 0, share)
	byDefault := WithMaterials(in, DefaultMaterialTiers, DefaultTreatmentCycleYears, share)
	if *unset.Years[0].Materials != *byDefault.Years[0].Materials {
		t.Errorf("cycle 0: got %+v, want the default cycle's %+v", unset.Years[0].Materials, byDefault.Years[0].Materials)
	}
}

// TestWithMaterials_CustomCostTierLabels: a city whose cost tiers are named
// and banded its own way (Los Angeles County's Good/Fair/Poor/Failed) gets
// material demand that follows condition, so it varies across years and
// scenarios. Matching by label sent every year of every scenario to the
// heaviest tier: Agoura Hills at 53,655 t/yr instead of ~3,300.
func TestWithMaterials_CustomCostTierLabels(t *testing.T) {
	tiers := []CostTier{
		{MinPCI: 0, MaxPCI: 25, CostPerSqM: 200, Label: "Failed"},
		{MinPCI: 25, MaxPCI: 50, CostPerSqM: 120, Label: "Poor"},
		{MinPCI: 50, MaxPCI: 70, CostPerSqM: 60, Label: "Fair"},
		{MinPCI: 70, MaxPCI: 100, CostPerSqM: 15, Label: "Good"},
	}
	p := NewParams(0, tiers, 12, 1)
	cohorts := []Cohort{{Classification: "residential", Area: 1_000_000, DecayRate: 0.04, InitialPCI: 85}}
	none := WithMaterials(Simulate(Scenario{Strategy: StrategyDoNothing}, cohorts, 30, p), DefaultMaterialTiers, 12, 1)
	full := WithMaterials(Simulate(Scenario{Strategy: StrategyWorstFirst, FullFunding: true}, cohorts, 30, p), DefaultMaterialTiers, 12, 1)

	slice := 1_000_000.0 / 12
	if got, want := full.Years[0].Materials.MixTonnes, slice*30/1000; math.Abs(got-want) > 1e-6 {
		t.Errorf("year 1 at PCI ~85: mix %v t, want the preventive %v t", got, want)
	}
	if none.Years[0].Materials.MixTonnes == none.Years[29].Materials.MixTonnes {
		t.Errorf("Do Nothing mix is flat across 30 years (%v t): it no longer follows condition", none.Years[0].Materials.MixTonnes)
	}
	if none.Years[29].Materials.MixTonnes == full.Years[29].Materials.MixTonnes {
		t.Errorf("year 30: Do Nothing and Full Funding take the same mix (%v t)", none.Years[29].Materials.MixTonnes)
	}
}
