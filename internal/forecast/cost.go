package forecast

import (
	"math"
	"sort"
)

// Default cost tiers ($/sq m) based on PCI ranges.
// Updated to 2024 median urban municipal bid prices. Previous FHWA guidance
// midpoints ($4.20/$13.78/$35.84) were 3-5x below current urban costs for
// rehab and reconstruction. Preventive costs remain close to the FHWA range.
type CostTier struct {
	MinPCI     float64 `json:"min_pci"` // inclusive
	MaxPCI     float64 `json:"max_pci"` // exclusive; last tier uses 101 as sentinel so PCI=100 matches via pci < 101
	CostPerSqM float64 `json:"cost_per_sqm"`
	Label      string  `json:"label"`
}

var DefaultCostTiers = []CostTier{
	{MinPCI: 70, MaxPCI: 101, CostPerSqM: 5.00, Label: "preventive"},     // microsurfacing + crack seal (~$3-5/sq m)
	{MinPCI: 40, MaxPCI: 70, CostPerSqM: 50.00, Label: "rehab"},          // mill & overlay ($30-72/sq m median)
	{MinPCI: 0, MaxPCI: 40, CostPerSqM: 150.00, Label: "reconstruction"}, // full-depth reconstruction ($96-239/sq m median)
}

var DefaultSidewalkCostTiers = []CostTier{
	{MinPCI: 70, MaxPCI: 101, CostPerSqM: 3.00, Label: "preventive"},    // crack sealing, joint repair
	{MinPCI: 40, MaxPCI: 70, CostPerSqM: 30.00, Label: "rehab"},         // panel replacement
	{MinPCI: 0, MaxPCI: 40, CostPerSqM: 90.00, Label: "reconstruction"}, // full replacement
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
			cost: t.CostPerSqM,
		}
	}
	sort.Slice(anchors, func(i, j int) bool {
		return anchors[i].pci > anchors[j].pci
	})
	return anchors
}

// interpolateCost returns the cost-per-sqm for a given PCI by linearly
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
	for i := range len(anchors) - 1 {
		hi := anchors[i]
		lo := anchors[i+1]
		if pci <= hi.pci && pci >= lo.pci {
			span := hi.pci - lo.pci
			if span == 0 {
				return hi.cost
			}
			t := (hi.pci - pci) / span
			return hi.cost + t*(lo.cost-hi.cost)
		}
	}
	return last.cost
}

// TieredCostProjector uses PCI-based cost tiers to estimate treatment costs.
// Cost-per-sqm is linearly interpolated between tier midpoints to produce
// smooth cost curves instead of step-function jumps at tier boundaries.
type TieredCostProjector struct {
	Tiers []CostTier // if nil, uses DefaultCostTiers
}

func (p *TieredCostProjector) ProjectCost(area float64, pci float64) float64 {
	tiers := p.Tiers
	if len(tiers) == 0 {
		tiers = DefaultCostTiers
	}
	anchors := buildAnchors(tiers)
	return area * interpolateCost(anchors, pci)
}

// TierForPCIIn returns the label of the tier whose PCI band contains pci, or
// "" if none matches. This is the single tier-lookup primitive; callers that
// need a fallback label supply their own.
func TierForPCIIn(tiers []CostTier, pci float64) string {
	for _, t := range tiers {
		if pci >= t.MinPCI && pci < t.MaxPCI {
			return t.Label
		}
	}
	return ""
}

// TierForPCI returns the default-schedule cost tier label for a given PCI value.
func TierForPCI(pci float64) string {
	if label := TierForPCIIn(DefaultCostTiers, pci); label != "" {
		return label
	}
	return "reconstruction"
}

// TierForPCI returns the cost tier label for a PCI value using the projector's
// configured tiers (falling back to DefaultCostTiers exactly as ProjectCost
// does), so the per-year label stays consistent with the dollar math.
func (p *TieredCostProjector) TierForPCI(pci float64) string {
	tiers := p.Tiers
	if len(tiers) == 0 {
		tiers = DefaultCostTiers
	}
	if label := TierForPCIIn(tiers, pci); label != "" {
		return label
	}
	// Operators may leave coverage gaps (config.ForecastConfig.Validate
	// permits them), so a PCI can fall outside every band. Use the nearest
	// band's label, mirroring how interpolateCost clamps to the nearest
	// anchor — never a hardcoded default-schedule label that may not exist in
	// the operator's set.
	return nearestTierLabel(tiers, pci)
}

// nearestTierLabel returns the label of the tier whose PCI band is closest to
// pci. Only used as a coverage-gap fallback.
func nearestTierLabel(tiers []CostTier, pci float64) string {
	best := ""
	bestDist := math.Inf(1)
	for _, t := range tiers {
		var d float64
		switch {
		case pci < t.MinPCI:
			d = t.MinPCI - pci
		case pci >= t.MaxPCI:
			d = pci - t.MaxPCI
		default:
			return t.Label
		}
		if d < bestDist {
			bestDist = d
			best = t.Label
		}
	}
	return best
}
