package forecast

// MaterialTier describes the physical makeup of one treatment tier's applied
// pavement layer, keyed to a CostTier by Label. All quantities are per square
// meter of treated pavement. This is the material-side parallel to CostTier:
// where CostTier answers "dollars per m^2 at this condition," MaterialTier
// answers "asphalt mix (and its binder/aggregate split) per m^2."
type MaterialTier struct {
	Label       string  `json:"label"`          // matches CostTier.Label (preventive/rehab/reconstruction)
	MixKgPerSqM float64 `json:"mix_kg_per_sqm"` // applied hot-mix asphalt (or slurry) mass per m^2
	BinderPct   float64 `json:"binder_pct"`     // asphalt-cement (bitumen) fraction of mix mass, 0..1
}

// DefaultMaterialTiers are representative per-tier material intensities for
// flexible (asphalt) pavement, aligned label-for-label with DefaultCostTiers.
// MixKgPerSqM is derived as compacted lift thickness x hot-mix density
// (~2400 kg/m^3 dense-graded HMA, Asphalt Institute MS-2) for the overlay and
// reconstruction tiers, and as a slurry application rate for the preventive
// tier; BinderPct is typical dense-graded asphalt-cement content (Asphalt
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
	{Label: "preventive", MixKgPerSqM: 30, BinderPct: 0.065},     // microsurfacing/slurry seal, ~2.0 kg/m^2 binder
	{Label: "rehab", MixKgPerSqM: 120, BinderPct: 0.055},         // ~50 mm mill & overlay (0.05 m x 2400), ~6.6 kg/m^2 binder
	{Label: "reconstruction", MixKgPerSqM: 480, BinderPct: 0.05}, // ~200 mm full-depth asphalt (0.20 m x 2400), ~24 kg/m^2 binder
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

// MaterialTierFor returns the tier whose Label matches, and ok=true. When no
// label matches it falls back to the highest-intensity tier present (so an
// unexpected cost_tier over-counts rather than silently under-counts material)
// and ok=false. Returns the zero tier and false for an empty set.
func MaterialTierFor(tiers []MaterialTier, label string) (MaterialTier, bool) {
	if len(tiers) == 0 {
		return MaterialTier{}, false
	}
	worst := tiers[0]
	for _, t := range tiers {
		if t.Label == label {
			return t, true
		}
		if t.MixKgPerSqM > worst.MixKgPerSqM {
			worst = t
		}
	}
	return worst, false
}

// MaterialsForArea returns the material demand to treat area (m^2) at one tier.
func MaterialsForArea(area float64, t MaterialTier) MaterialQuantities {
	// Floor negative area at 0 (house style: clamp-in-place, not error-return):
	// a negative area would otherwise yield negative tonnages/barrels.
	if area < 0 {
		area = 0
	}
	mixTonnes := area * t.MixKgPerSqM / 1000
	binderTonnes := mixTonnes * t.BinderPct
	return MaterialQuantities{
		MixTonnes:       mixTonnes,
		BinderTonnes:    binderTonnes,
		AggregateTonnes: mixTonnes - binderTonnes,
		OilBarrels:      binderTonnes * BarrelsPerTonBinder,
	}
}
