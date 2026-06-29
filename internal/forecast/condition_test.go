package forecast

import (
	"math"
	"testing"
)

// regIncBeta against closed-form / known values pins the Beta numerics.
func TestRegIncBeta(t *testing.T) {
	cases := []struct {
		x, a, b, want float64
	}{
		{0.3, 1, 1, 0.3},              // I_x(1,1) = x (uniform)
		{0.5, 2, 2, 0.5},              // symmetric Beta(2,2)
		{0.5, 2, 3, 0.6875},           // closed-form integral
		{0.0, 2, 5, 0.0},              //
		{1.0, 2, 5, 1.0},              //
		{0.2, 5, 1, math.Pow(0.2, 5)}, // I_x(a,1) = x^a
	}
	for _, c := range cases {
		got := regIncBeta(c.x, c.a, c.b)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("regIncBeta(%g,%g,%g) = %.10f, want %.10f", c.x, c.a, c.b, got, c.want)
		}
	}
}

// betaQuantile must invert regIncBeta.
func TestBetaQuantileInverts(t *testing.T) {
	for _, p := range []float64{0.1, 0.25, 0.5, 0.75, 0.9} {
		x := betaQuantile(p, 2.4, 1.6)
		if got := regIncBeta(x, 2.4, 1.6); math.Abs(got-p) > 1e-9 {
			t.Errorf("quantile(%g) round-trip = %g", p, got)
		}
	}
}

// ApplyConditionSpread must preserve the area-weighted mean PCI exactly (the
// whole point of the Beta-conditional-mean discretization) and split area into
// exactly K bands per cohort.
func TestApplyConditionSpreadMeanPreserved(t *testing.T) {
	for _, mu := range []float64{40, 55, 67, 78, 85} {
		in := []Cohort{{Classification: "roads", Area: 1000, DecayRate: 0.04, InitialPCI: mu}}
		out := ApplyConditionSpread(in)
		if len(out) != conditionBands {
			t.Fatalf("mu=%g: got %d sub-cohorts, want %d", mu, len(out), conditionBands)
		}
		var area, areaPCI float64
		for _, c := range out {
			area += c.Area
			areaPCI += c.InitialPCI * c.Area
			if c.Classification != "roads" || c.DecayRate != 0.04 {
				t.Errorf("sub-cohort lost class/decay: %+v", c)
			}
		}
		if math.Abs(area-1000) > 1e-6 {
			t.Errorf("mu=%g: area not conserved: %g", mu, area)
		}
		if math.Abs(areaPCI/area-mu) > 1e-6 {
			t.Errorf("mu=%g: mean PCI not preserved: %g", mu, areaPCI/area)
		}
	}
}

// Degenerate means (0 or 100) have no spread and pass through unchanged.
func TestApplyConditionSpreadDegenerate(t *testing.T) {
	for _, mu := range []float64{0, 100} {
		in := []Cohort{{Classification: "x", Area: 10, InitialPCI: mu, DecayRate: 0.04}}
		out := ApplyConditionSpread(in)
		if len(out) != 1 || out[0].InitialPCI != mu {
			t.Errorf("mu=%g: expected passthrough, got %+v", mu, out)
		}
	}
	if ApplyConditionSpread(nil) != nil {
		t.Error("nil input should return nil")
	}
}

// Calibration guard: the deployed correction is the Simulate year-1 AnnualNeed
// uplift. This pins conditionConcentration=4: a conservative *partial* recovery
// of the validation.md §4 ~32–37% gap (a unimodal Beta cannot reproduce the real
// barbell, and §4 is itself a lower bound). It also guards the high-μ regime
// against a Gaussian-style explosion. If ν changes, these bands flag it.
func TestConditionSpreadCalibration(t *testing.T) {
	params := NewParams(0, DefaultCostTiers, 0)
	decay := DecayRateForClass("roads")
	uplift := func(mu float64) float64 {
		base := []Cohort{{Classification: "roads", Area: 1e6, DecayRate: decay, InitialPCI: mu}}
		b := Simulate(Scenario{Strategy: StrategyDoNothing}, base, 20, params)
		s := Simulate(Scenario{Strategy: StrategyDoNothing}, ApplyConditionSpread(base), 20, params)
		return s.Years[0].AnnualNeed/b.Years[0].AnnualNeed - 1
	}
	// Representative mid-range (most shipped cities are PCI 56–78): a material
	// but conservative correction.
	for _, tc := range []struct{ mu, lo, hi float64 }{
		{55, 0.12, 0.24},
		{67, 0.16, 0.28},
		{78, 0.22, 0.40},
	} {
		if u := uplift(tc.mu); u < tc.lo || u > tc.hi {
			t.Errorf("uplift(mu=%g) = %.1f%%, want in [%.0f%%,%.0f%%]", tc.mu, u*100, tc.lo*100, tc.hi*100)
		}
	}
	// High-μ guard: bounded, not a Gaussian-style >100% explosion.
	if u := uplift(85); u <= 0 || u >= 1.0 {
		t.Errorf("uplift(mu=85) = %.1f%%, want bounded (0,100%%)", u*100)
	}
	// Direction: spreading must never reduce cost (Jensen).
	if u := uplift(60); u <= 0 {
		t.Errorf("uplift(mu=60) = %.1f%%, must be positive", u*100)
	}
}

func TestAggregateCohortSummariesByClass(t *testing.T) {
	in := []CohortSummary{
		{Classification: "a", EndPCI: 80, Area: 100, DecayRate: 0.03, TotalSpend: 10, TotalDeficit: 1},
		{Classification: "a", EndPCI: 40, Area: 300, DecayRate: 0.03, TotalSpend: 30, TotalDeficit: 3},
		{Classification: "b", EndPCI: 50, Area: 200, DecayRate: 0.05, TotalSpend: 5, TotalDeficit: 2},
	}
	out := AggregateCohortSummariesByClass(in)
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2", len(out))
	}
	a := out[0]
	if a.Classification != "a" || a.Area != 400 || a.TotalSpend != 40 || a.TotalDeficit != 4 {
		t.Errorf("class a aggregate wrong: %+v", a)
	}
	// area-weighted EndPCI = (80*100 + 40*300)/400 = 50
	if math.Abs(a.EndPCI-50) > 1e-9 {
		t.Errorf("class a EndPCI = %g, want 50", a.EndPCI)
	}
	if out[1].Classification != "b" || out[1].Area != 200 {
		t.Errorf("class b aggregate wrong: %+v", out[1])
	}
	// idempotent
	if again := AggregateCohortSummariesByClass(out); len(again) != 2 {
		t.Errorf("not idempotent: %d rows", len(again))
	}
}
