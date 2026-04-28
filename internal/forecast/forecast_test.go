package forecast

import (
	"math"
	"testing"
)

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
	// Preventive midpoint: (70+100)/2 = 85 → $5.00/sqm
	cost := p.ProjectCost(10000, 85)
	if math.Abs(cost-50000) > 10 {
		t.Errorf("PCI 85 (preventive midpoint): expected $50000, got %f", cost)
	}
	// Rehab midpoint: (40+70)/2 = 55 → $50.00/sqm
	cost = p.ProjectCost(10000, 55)
	if math.Abs(cost-500000) > 10 {
		t.Errorf("PCI 55 (rehab midpoint): expected $500000, got %f", cost)
	}
	// Reconstruction midpoint: (0+40)/2 = 20 → $150.00/sqm
	cost = p.ProjectCost(10000, 20)
	if math.Abs(cost-1500000) > 10 {
		t.Errorf("PCI 20 (reconstruction midpoint): expected $1500000, got %f", cost)
	}

	// Between midpoints, cost is interpolated (no step-function jump)
	// PCI 80: between anchors 85→$5.00 and 55→$50.00
	// t = (85-80)/(85-55) = 5/30; cost = 5.00 + (5/30)*45.00 = ~$12.50/sqm → ~$125000
	cost = p.ProjectCost(10000, 80)
	if math.Abs(cost-125000) > 1000 {
		t.Errorf("PCI 80 (interpolated): expected ~$125000, got %f", cost)
	}

	// At tier boundary (PCI 70), cost transitions smoothly
	// Between anchors 85→$5.00 and 55→$50.00
	// t = (85-70)/(85-55) = 15/30 = 0.5; cost = 5.00 + 0.5*45.00 = $27.50/sqm
	cost = p.ProjectCost(10000, 70)
	if math.Abs(cost-275000) > 1000 {
		t.Errorf("PCI 70 (boundary): expected ~$275000, got %f", cost)
	}

	// Above highest midpoint, clamped to preventive rate
	cost = p.ProjectCost(10000, 100)
	if math.Abs(cost-50000) > 10 {
		t.Errorf("PCI 100 (clamped): expected $50000, got %f", cost)
	}

	// Below lowest midpoint, clamped to reconstruction rate
	cost = p.ProjectCost(10000, 5)
	if math.Abs(cost-1500000) > 10 {
		t.Errorf("PCI 5 (clamped): expected $1500000, got %f", cost)
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
