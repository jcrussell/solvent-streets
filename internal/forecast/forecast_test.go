package forecast

import (
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
