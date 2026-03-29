package forecast

import (
	"math"
	"testing"
)

func TestStubPCIForecaster(t *testing.T) {
	f := &StubPCIForecaster{}
	result := f.Forecast(85.0, 5)
	if len(result) != 5 {
		t.Fatalf("expected 5 values, got %d", len(result))
	}
	for i, v := range result {
		if v != 85.0 {
			t.Errorf("year %d: expected 85.0, got %f", i, v)
		}
	}
}

func TestStubCostProjector(t *testing.T) {
	p := &StubCostProjector{}
	cost := p.ProjectCost(10000, 75)
	if cost != 0 {
		t.Errorf("expected 0, got %f", cost)
	}
}

func TestStubGrowthEstimator(t *testing.T) {
	g := &StubGrowthEstimator{}
	result := g.EstimateGrowth(50000, 3)
	if len(result) != 3 {
		t.Fatalf("expected 3 values, got %d", len(result))
	}
	for i, v := range result {
		if v != 50000 {
			t.Errorf("year %d: expected 50000, got %f", i, v)
		}
	}
}

func TestStubPCIForecaster_ZeroYears(t *testing.T) {
	f := &StubPCIForecaster{}
	result := f.Forecast(85.0, 0)
	if len(result) != 0 {
		t.Errorf("expected empty slice for 0 years, got %d", len(result))
	}
}

func TestExponentialPCIForecaster(t *testing.T) {
	f := &ExponentialPCIForecaster{DecayRate: 0.035}
	result := f.Forecast(100.0, 20)
	if len(result) != 20 {
		t.Fatalf("expected 20 values, got %d", len(result))
	}
	// Year 1: 100 * exp(-0.035) ~ 96.6
	if math.Abs(result[0]-96.56) > 0.1 {
		t.Errorf("year 1: expected ~96.6, got %f", result[0])
	}
	// Year 20: 100 * exp(-0.7) ~ 49.7
	if math.Abs(result[19]-49.66) > 0.2 {
		t.Errorf("year 20: expected ~49.7, got %f", result[19])
	}
	// PCI should be monotonically decreasing
	for i := 1; i < len(result); i++ {
		if result[i] >= result[i-1] {
			t.Errorf("PCI should decrease: year %d (%f) >= year %d (%f)", i+1, result[i], i, result[i-1])
		}
	}
}

func TestTieredCostProjector(t *testing.T) {
	p := &TieredCostProjector{}

	// At tier midpoints, cost equals the tier's exact rate
	// Preventive midpoint: (70+100)/2 = 85 → $4.20/sqm
	cost := p.ProjectCost(10000, 85)
	if math.Abs(cost-42000) > 10 {
		t.Errorf("PCI 85 (preventive midpoint): expected $42000, got %f", cost)
	}
	// Rehab midpoint: (40+70)/2 = 55 → $13.78/sqm
	cost = p.ProjectCost(10000, 55)
	if math.Abs(cost-137800) > 10 {
		t.Errorf("PCI 55 (rehab midpoint): expected $137800, got %f", cost)
	}
	// Reconstruction midpoint: (0+40)/2 = 20 → $35.84/sqm
	cost = p.ProjectCost(10000, 20)
	if math.Abs(cost-358400) > 10 {
		t.Errorf("PCI 20 (reconstruction midpoint): expected $358400, got %f", cost)
	}

	// Between midpoints, cost is interpolated (no step-function jump)
	// PCI 80: between anchors 85→$4.20 and 55→$13.78
	// t = (85-80)/(85-55) = 5/30; cost = 4.20 + (5/30)*9.58 = ~$5.797/sqm → ~$57967
	cost = p.ProjectCost(10000, 80)
	if math.Abs(cost-57967) > 1000 {
		t.Errorf("PCI 80 (interpolated): expected ~$57967, got %f", cost)
	}

	// At tier boundary (PCI 70), cost transitions smoothly
	// Between anchors 85→$4.20 and 55→$13.78
	// t = (85-70)/(85-55) = 15/30 = 0.5; cost = 4.20 + 0.5*9.58 = $8.99/sqm
	cost = p.ProjectCost(10000, 70)
	if math.Abs(cost-89900) > 1000 {
		t.Errorf("PCI 70 (boundary): expected ~$89900, got %f", cost)
	}

	// Above highest midpoint, clamped to preventive rate
	cost = p.ProjectCost(10000, 100)
	if math.Abs(cost-42000) > 10 {
		t.Errorf("PCI 100 (clamped): expected $42000, got %f", cost)
	}

	// Below lowest midpoint, clamped to reconstruction rate
	cost = p.ProjectCost(10000, 5)
	if math.Abs(cost-358400) > 10 {
		t.Errorf("PCI 5 (clamped): expected $358400, got %f", cost)
	}
}

func TestLinearGrowthEstimator(t *testing.T) {
	g := &LinearGrowthEstimator{AnnualGrowthRate: 0.01}
	result := g.EstimateGrowth(100000, 5)
	if len(result) != 5 {
		t.Fatalf("expected 5 values, got %d", len(result))
	}
	// Year 1: 100000 * 1.01 = 101000
	if math.Abs(result[0]-101000) > 1 {
		t.Errorf("year 1: expected 101000, got %f", result[0])
	}
	// Should be increasing
	for i := 1; i < len(result); i++ {
		if result[i] <= result[i-1] {
			t.Errorf("area should increase with positive growth rate")
		}
	}
}

func TestLinearGrowthEstimator_ZeroRate(t *testing.T) {
	g := &LinearGrowthEstimator{AnnualGrowthRate: 0}
	result := g.EstimateGrowth(50000, 3)
	for _, v := range result {
		if v != 50000 {
			t.Errorf("expected 50000 with 0 growth, got %f", v)
		}
	}
}
