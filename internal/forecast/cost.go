package forecast

import "sort"

// Default cost tiers ($/sq ft) based on PCI ranges.
// Based on FHWA treatment selection guidelines.
type CostTier struct {
	MinPCI      float64 // inclusive
	MaxPCI      float64 // exclusive; last tier uses 101 as sentinel so PCI=100 matches via pci < 101
	CostPerSqFt float64
	Label       string
}

var DefaultCostTiers = []CostTier{
	{MinPCI: 70, MaxPCI: 101, CostPerSqFt: 3.5, Label: "preventive"},    // $2-5/sqft
	{MinPCI: 40, MaxPCI: 70, CostPerSqFt: 11.5, Label: "rehab"},         // $8-15/sqft
	{MinPCI: 0, MaxPCI: 40, CostPerSqFt: 30.0, Label: "reconstruction"}, // $20-40/sqft
}

type StubCostProjector struct{}

func (s *StubCostProjector) ProjectCost(areaSqFt float64, pci float64) float64 {
	return 0
}

// costAnchor is a PCI→cost anchor point derived from a tier's midpoint.
type costAnchor struct {
	pci  float64
	cost float64
}

// buildAnchors creates sorted (descending PCI) anchor points from tier midpoints.
// MaxPCI sentinel of 101 is capped to 100 for midpoint calculation.
func buildAnchors(tiers []CostTier) []costAnchor {
	anchors := make([]costAnchor, len(tiers))
	for i, t := range tiers {
		maxPCI := t.MaxPCI
		if maxPCI > 100 {
			maxPCI = 100
		}
		anchors[i] = costAnchor{
			pci:  (t.MinPCI + maxPCI) / 2,
			cost: t.CostPerSqFt,
		}
	}
	sort.Slice(anchors, func(i, j int) bool {
		return anchors[i].pci > anchors[j].pci
	})
	return anchors
}

// interpolateCost returns the cost-per-sqft for a given PCI by linearly
// interpolating between tier midpoint anchors.
func interpolateCost(anchors []costAnchor, pci float64) float64 {
	if len(anchors) == 0 {
		return 0
	}
	// Clamp at extremes
	if pci >= anchors[0].pci {
		return anchors[0].cost
	}
	last := anchors[len(anchors)-1]
	if pci <= last.pci {
		return last.cost
	}
	// Find the two anchors that bracket this PCI (anchors sorted descending)
	for i := 0; i < len(anchors)-1; i++ {
		hi := anchors[i]
		lo := anchors[i+1]
		if pci <= hi.pci && pci >= lo.pci {
			t := (hi.pci - pci) / (hi.pci - lo.pci)
			return hi.cost + t*(lo.cost-hi.cost)
		}
	}
	return last.cost
}

// TieredCostProjector uses PCI-based cost tiers to estimate treatment costs.
// Cost-per-sqft is linearly interpolated between tier midpoints to produce
// smooth cost curves instead of step-function jumps at tier boundaries.
type TieredCostProjector struct {
	Tiers []CostTier // if nil, uses DefaultCostTiers
}

func (p *TieredCostProjector) ProjectCost(areaSqFt float64, pci float64) float64 {
	tiers := p.Tiers
	if len(tiers) == 0 {
		tiers = DefaultCostTiers
	}
	anchors := buildAnchors(tiers)
	return areaSqFt * interpolateCost(anchors, pci)
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
