package forecast

// Default cost tiers ($/sq ft) based on PCI ranges.
// Based on FHWA treatment selection guidelines.
type CostTier struct {
	MinPCI float64 // inclusive
	MaxPCI float64 // exclusive; last tier uses 101 as sentinel so PCI=100 matches via pci < 101
	CostPerSqFt float64
	Label  string
}

var DefaultCostTiers = []CostTier{
	{MinPCI: 70, MaxPCI: 101, CostPerSqFt: 3.5, Label: "preventive"},   // $2-5/sqft
	{MinPCI: 40, MaxPCI: 70, CostPerSqFt: 11.5, Label: "rehab"},        // $8-15/sqft
	{MinPCI: 0, MaxPCI: 40, CostPerSqFt: 30.0, Label: "reconstruction"}, // $20-40/sqft
}

type StubCostProjector struct{}

func (s *StubCostProjector) ProjectCost(areaSqFt float64, pci float64) float64 {
	return 0
}

// TieredCostProjector uses PCI-based cost tiers to estimate treatment costs.
type TieredCostProjector struct {
	Tiers []CostTier // if nil, uses DefaultCostTiers
}

func (p *TieredCostProjector) ProjectCost(areaSqFt float64, pci float64) float64 {
	tiers := p.Tiers
	if len(tiers) == 0 {
		tiers = DefaultCostTiers
	}
	for _, t := range tiers {
		if pci >= t.MinPCI && pci < t.MaxPCI {
			return areaSqFt * t.CostPerSqFt
		}
	}
	// Below minimum PCI
	if len(tiers) > 0 {
		return areaSqFt * tiers[len(tiers)-1].CostPerSqFt
	}
	return 0
}

// TierForPCI returns the cost tier label for a given PCI value.
func TierForPCI(pci float64) string {
	for _, t := range DefaultCostTiers {
		if pci >= t.MinPCI && pci < t.MaxPCI {
			return t.Label
		}
	}
	return "reconstruction"
}
