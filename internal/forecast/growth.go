package forecast

type StubGrowthEstimator struct{}

func (s *StubGrowthEstimator) EstimateGrowth(currentAreaSqFt float64, years int) []float64 {
	result := make([]float64, years)
	for i := range result {
		result[i] = currentAreaSqFt
	}
	return result
}
