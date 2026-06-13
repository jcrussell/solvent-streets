package forecast

// LinearGrowthEstimator models pavement growth as a fixed annual increment of
// the current (year-zero) area: year t area = currentArea * (1 + rate*t). The
// rate is a fraction of the starting area added each year, so 0.01 = +1% of the
// starting area per year (linear, not compounding).
type LinearGrowthEstimator struct {
	AnnualGrowthRate float64 // e.g. 0.01 for 1% annual growth
}

func (g *LinearGrowthEstimator) EstimateGrowth(currentArea float64, years int) []float64 {
	// Negative rates model a shrinking network and are honored (config
	// validation permits growth_rate in [-0.5, 0.5]). The factor is floored
	// at zero so a steep, long decline can never produce negative area.
	rate := g.AnnualGrowthRate
	result := make([]float64, years)
	for i := range years {
		factor := 1 + rate*float64(i+1)
		if factor < 0 {
			factor = 0
		}
		result[i] = currentArea * factor
	}
	return result
}
