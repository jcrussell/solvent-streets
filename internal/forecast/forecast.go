package forecast

type PCIForecaster interface {
	Forecast(currentPCI float64, years int) []float64
}

type CostProjector interface {
	ProjectCost(areaSqFt float64, pci float64) float64
}

type GrowthEstimator interface {
	EstimateGrowth(currentAreaSqFt float64, years int) []float64
}
