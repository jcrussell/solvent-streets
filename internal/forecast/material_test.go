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
	tier := MaterialTier{Label: "rehab", MixKgPerSqM: 120, BinderPct: 0.055}
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
	// Every default cost tier must have a same-labeled material tier so a
	// per-year cost_tier always resolves to a material intensity.
	for _, ct := range DefaultCostTiers {
		if _, ok := MaterialTierFor(DefaultMaterialTiers, ct.Label); !ok {
			t.Errorf("no MaterialTier for cost tier %q", ct.Label)
		}
	}
	// Material intensity should be monotonic with treatment severity:
	// preventive < rehab < reconstruction.
	prev, _ := MaterialTierFor(DefaultMaterialTiers, "preventive")
	rehab, _ := MaterialTierFor(DefaultMaterialTiers, "rehab")
	recon, _ := MaterialTierFor(DefaultMaterialTiers, "reconstruction")
	if !(prev.MixKgPerSqM < rehab.MixKgPerSqM && rehab.MixKgPerSqM < recon.MixKgPerSqM) {
		t.Errorf("mix intensity not monotonic: %v %v %v", prev.MixKgPerSqM, rehab.MixKgPerSqM, recon.MixKgPerSqM)
	}
}

func TestMaterialTierForFallback(t *testing.T) {
	// Unknown label falls back to the highest-intensity tier, ok=false.
	got, ok := MaterialTierFor(DefaultMaterialTiers, "no-such-tier")
	if ok {
		t.Errorf("ok = true for unknown label, want false")
	}
	if got.Label != "reconstruction" {
		t.Errorf("fallback tier = %q, want reconstruction (highest MixKgPerSqM)", got.Label)
	}

	if _, ok := MaterialTierFor(nil, "rehab"); ok {
		t.Errorf("ok = true for empty tier set, want false")
	}
}
