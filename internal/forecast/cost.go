package forecast

import (
	"math"
	"sort"
	"sync"
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
	// Stable sort so equal-midpoint tiers keep their input order deterministically.
	sort.SliceStable(anchors, func(i, j int) bool {
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

	// Overhead multiplies every tier's cost to turn a BARE construction price
	// into the LOADED program cost a municipality actually budgets: ADA
	// curb-ramp compliance, design/inspection/project management, and
	// contingency (docs/validation.md §3). It is a separate knob from Tiers
	// because the two vary independently — regional construction pricing
	// belongs in the tiers, while the overhead stack is policy-driven and
	// roughly structural everywhere.
	//
	// A value <= 0 means 1.0 (bare), NOT the config default. Two reasons this
	// matters and must not be "helpfully" changed to default to
	// DefaultCostOverhead here:
	//
	//  1. The zero value of a freshly-constructed projector would silently
	//     acquire whatever the config default happens to be, so any
	//     construction site that forgot to set it would move dollars rather
	//     than fail. That the default is currently 1.0 (making the divergence
	//     zero today) is exactly why the rule has to be structural: it would
	//     otherwise look safe to drop right up until the default moves.
	//  2. Tests and the parity golden build Params directly, without going
	//     through config. Defaulting in this package would move
	//     testdata/parity_output.json and every export golden.
	//
	// The default is injected by config resolution
	// (config.ForecastConfig.CostOverhead, currently
	// config.DefaultCostOverhead = 1.0). This package never defaults it.
	Overhead float64

	// anchors is memoized from Tiers on first use. buildAnchors was previously
	// re-run on every ProjectCost call (once per cohort per year in Simulate);
	// the tier set is immutable for a projector's lifetime, so build it once.
	anchorsOnce sync.Once
	anchors     []costAnchor
}

// resolvedOverhead is the effective multiplier: Overhead when positive, else
// 1.0. Non-finite values are also treated as 1.0 — a NaN would otherwise turn
// every dollar on the page into NaN, which renders as blank rather than wrong,
// and is the harder failure to trace.
func (p *TieredCostProjector) resolvedOverhead() float64 {
	if p.Overhead <= 0 || math.IsNaN(p.Overhead) || math.IsInf(p.Overhead, 0) {
		return 1
	}
	return p.Overhead
}

// resolvedAnchors returns the memoized anchor set, building it lazily from Tiers
// (falling back to DefaultCostTiers when Tiers is empty) on first call.
// Overhead is folded into the memoized anchors rather than applied at each
// ProjectCost call: it is immutable for a projector's lifetime exactly like
// Tiers, and applying it once here means every consumer of the anchors gets it
// by construction rather than by remembering to multiply.
func (p *TieredCostProjector) resolvedAnchors() []costAnchor {
	p.anchorsOnce.Do(func() {
		tiers := p.Tiers
		if len(tiers) == 0 {
			tiers = DefaultCostTiers
		}
		anchors := buildAnchors(tiers)
		if oh := p.resolvedOverhead(); oh != 1 {
			for i := range anchors {
				anchors[i].cost *= oh
			}
		}
		p.anchors = anchors
	})
	return p.anchors
}

func (p *TieredCostProjector) ProjectCost(area float64, pci float64) float64 {
	return area * interpolateCost(p.resolvedAnchors(), pci)
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
