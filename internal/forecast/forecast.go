package forecast

type PCIForecaster interface {
	Forecast(currentPCI float64, years int) []float64
}

type CostProjector interface {
	ProjectCost(areaSqM float64, pci float64) float64
}

type GrowthEstimator interface {
	EstimateGrowth(currentAreaSqM float64, years int) []float64
}
