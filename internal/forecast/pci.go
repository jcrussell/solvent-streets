package forecast

type StubPCIForecaster struct{}

func (s *StubPCIForecaster) Forecast(currentPCI float64, years int) []float64 {
	result := make([]float64, years)
	for i := range result {
		result[i] = currentPCI
	}
	return result
}
