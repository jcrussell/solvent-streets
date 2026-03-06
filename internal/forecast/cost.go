package forecast

type StubCostProjector struct{}

func (s *StubCostProjector) ProjectCost(areaSqFt float64, pci float64) float64 {
	return 0
}
