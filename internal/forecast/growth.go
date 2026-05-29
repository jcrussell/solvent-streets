package forecast

// LinearGrowthEstimator models pavement growth as a fixed annual percentage.
type LinearGrowthEstimator struct {
	AnnualGrowthRate float64 // e.g. 0.01 for 1% annual growth
}

func (g *LinearGrowthEstimator) EstimateGrowth(currentArea float64, years int) []float64 {
	rate := g.AnnualGrowthRate
	if rate <= 0 {
		rate = 0
	}
	result := make([]float64, years)
	area := currentArea
	for i := range years {
		area *= (1 + rate)
		result[i] = area
	}
	return result
}
