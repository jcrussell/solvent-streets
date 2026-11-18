package forecast

import "math"

// MaterialTier describes the physical makeup of the treatment applied to
// pavement in one PCI band. All quantities are per square meter of treated
// pavement. This is the material-side parallel to CostTier: where CostTier
// answers "dollars per m^2 at this condition," MaterialTier answers "asphalt
// mix (and its binder/aggregate split) per m^2."
//
// A material tier is chosen by condition, never by a cost tier's label: a
// city may name and band its cost tiers however its pavement program does
// (Good/Fair/Poor/Failed), but the treatment a road at PCI 55 gets, and so
// the asphalt it takes, does not follow the name (solvent-streets-ern4.25).
type MaterialTier struct {
	Label       string  `json:"label"`          // treatment name shown in the Materials tab
	MinPCI      float64 `json:"min_pci"`        // inclusive lower bound of the band
	MaxPCI      float64 `json:"max_pci"`        // exclusive upper bound (MaxTierPCI for the top band)
	MixKgPerSqM float64 `json:"mix_kg_per_sqm"` // applied hot-mix asphalt (or slurry) mass per m^2
	BinderShare float64 `json:"binder_share"`   // asphalt-cement (bitumen) fraction of mix mass, 0..1
}

// DefaultMaterialTiers are representative per-tier material intensities for
// flexible (asphalt) pavement, banded like DefaultCostTiers.
// MixKgPerSqM is derived as compacted lift thickness x hot-mix density
// (~2400 kg/m^3 dense-graded HMA, Asphalt Institute MS-2) for the overlay and
// reconstruction tiers, and as a slurry application rate for the preventive
// tier; BinderShare is typical dense-graded asphalt-cement content (Asphalt
// Institute MS-2, 4.5-6% by mass; microsurfacing residual binder ~6.5%, MS-19).
//
// These are planning-grade estimates; a municipality can refine them against
// its own mix designs.
//
// Sidewalks are concrete, not asphalt, and consume no bitumen at all. Because
// these tiers are attached to the seed unconditionally while the scenario areas
// they multiply sum every resource, the export NETS the concrete area out
// rather than pricing it as hot-mix: resource.Source.AsphaltSurfaced drives an
// asphalt_area_share on the forecast seed, and the Materials tab scales each
// year's treated area by it. A future DefaultSidewalkMaterialTiers
// (cement/aggregate) would REPLACE that share with a real blend, not stack on
// top of it — the way DefaultSidewalkCostTiers parallels DefaultCostTiers.
var DefaultMaterialTiers = []MaterialTier{
	{Label: "preventive", MinPCI: 70, MaxPCI: MaxTierPCI, MixKgPerSqM: 30, BinderShare: 0.065}, // microsurfacing/slurry seal, ~2.0 kg/m^2 binder
	{Label: "rehab", MinPCI: 40, MaxPCI: 70, MixKgPerSqM: 120, BinderShare: 0.055},             // ~50 mm mill & overlay (0.05 m x 2400), ~6.6 kg/m^2 binder
	{Label: "reconstruction", MinPCI: 0, MaxPCI: 40, MixKgPerSqM: 480, BinderShare: 0.05},      // ~200 mm full-depth asphalt (0.20 m x 2400), ~24 kg/m^2 binder
}

// BarrelsPerTonBinder converts asphalt-cement (bitumen) tonnage to
// crude-oil-equivalent barrels. Bitumen density ~1010 kg/m^3 and one barrel is
// 0.159 m^3, so 1 tonne occupies ~0.990 m^3 => ~6.23 bbl. This treats binder
// volume as oil-equivalent volume (bitumen is a petroleum residuum); it is a
// first-order "how much oil" figure, not a refinery yield calculation.
const BarrelsPerTonBinder = 6.23

// MaterialQuantities is the material demand to treat a given area at one tier.
// Masses are in metric tonnes; OilBarrels is crude-oil-equivalent barrels.
type MaterialQuantities struct {
	MixTonnes       float64 `json:"mix_tonnes"`
	BinderTonnes    float64 `json:"binder_tonnes"`
	AggregateTonnes float64 `json:"aggregate_tonnes"`
	OilBarrels      float64 `json:"oil_barrels"`
}

// MaterialTierForPCI returns the tier whose band contains pci, or, in a gap
// between bands or outside them, the nearest band (ties to the earlier tier),
// the way TierIndexForPCI resolves cost tiers. ok is false only for an empty
// set.
func MaterialTierForPCI(tiers []MaterialTier, pci float64) (MaterialTier, bool) {
	best, bestDist := -1, math.Inf(1)
	for i, t := range tiers {
		var d float64
		switch {
		case pci < t.MinPCI:
			d = t.MinPCI - pci
		case pci >= t.MaxPCI:
			d = pci - t.MaxPCI
		default:
			return t, true
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	if best < 0 {
		return MaterialTier{}, false
	}
	return tiers[best], true
}

// MaterialsForArea returns the material demand to treat area (m^2) at one tier.
func MaterialsForArea(area float64, t MaterialTier) MaterialQuantities {
	// Floor negative area at 0 (house style: clamp-in-place, not error-return):
	// a negative area would otherwise yield negative tonnages/barrels.
	if area < 0 {
		area = 0
	}
	mixTonnes := area * t.MixKgPerSqM / 1000
	binderTonnes := mixTonnes * t.BinderShare
	return MaterialQuantities{
		MixTonnes:       mixTonnes,
		BinderTonnes:    binderTonnes,
		AggregateTonnes: mixTonnes - binderTonnes,
		OilBarrels:      binderTonnes * BarrelsPerTonBinder,
	}
}

// WithMaterials returns r with every year's Materials filled in: the demand to
// retreat that year's share of the network. Each year area/cycleYears is
// treated (the eligible fraction the cost model bills), asphaltShare of it is
// asphalt, and the year's blended PCI picks the material tier
// (MaterialTierForPCI). r's own Years slice is left untouched.
func WithMaterials(r ScenarioResult, tiers []MaterialTier, cycleYears, asphaltShare float64) ScenarioResult {
	cycle := ResolveCycleYears(cycleYears)
	years := make([]ScenarioYear, len(r.Years))
	for i, y := range r.Years {
		tier, _ := MaterialTierForPCI(tiers, y.PCI)
		q := MaterialsForArea(y.Area*asphaltShare/cycle, tier)
		y.Materials = &q
		y.MaterialTier = tier.Label
		years[i] = y
	}
	r.Years = years
	return r
}
