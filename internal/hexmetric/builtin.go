package hexmetric

import "github.com/jcrussell/solvent-streets/internal/metric"

// Built-in derived metrics: the pavement colorings the dashboard has always
// offered, expressed as registry Specs so they share one paint, legend,
// selector and tooltip path with stored metrics like fire response. Their
// values are computed by the dashboard from hexgrid.geojson over the toggled
// resources (see Derivation), so a new pavement resource gets every one of
// these colorings without code.
//
// Band edges are absolute and city-independent (the Spec is static per key).
// They were picked from a real city's distribution (Livermore) at the
// default 100 m hex edge: roughly the 10th–99th percentiles. The cost edges
// sit a little above that city's roads-only spread, to leave room for
// parking and sidewalks.
func init() {
	Register(Spec{
		// The default "Color by" metric. It stays out of the URL.
		Key:    metric.MaintainedShare,
		Label:  "Maintained share",
		Group:  GroupPavement,
		Order:  1,
		Source: "Share of the hex's land the toggled resources cover, each resource's share added together (so overlaps count once per resource) and capped at 100%.",
		Format: FormatPercent,
		Scale: Scale{
			Kind: KindSequential, Transform: TransformLinear,
			Bands: []float64{0.05, 0.1, 0.2, 0.3, 0.5},
		},
		Derive: &Derivation{Op: OpSum, Prop: PropShare, Max: 1},
	})
	Register(Spec{
		Key:    metric.MaintainedArea,
		Label:  "Maintained area",
		Group:  GroupPavement,
		Order:  2,
		Source: "Area of the toggled resources inside the hex, each resource counted as the forecast prices it, so a sidewalk beside a road counts in both.",
		Format: FormatArea,
		Scale: Scale{
			Kind: KindSequential, Transform: TransformLinear,
			Bands: []float64{500, 1000, 2000, 4000, 8000},
		},
		Derive: &Derivation{Op: OpSum, Prop: PropArea},
	})
	Register(Spec{
		Key:    metric.ParkingSpaces,
		Label:  "Parking spaces",
		Group:  GroupPavement,
		Order:  3,
		Source: "Estimated parking spaces, from each lot's capacity tag or its area, shared among hexes by how much of the lot each holds.",
		Format: FormatInt,
		Scale: Scale{
			Kind: KindSequential, Transform: TransformLinear,
			Bands: []float64{10, 25, 50, 100, 250},
		},
		Derive: &Derivation{Op: OpSum, Prop: PropSpaces},
	})
	Register(Spec{
		Key:    metric.Year1Need,
		Label:  "Year-1 need",
		Group:  GroupPavement,
		Order:  4,
		Source: "First-year repair need of the toggled resources, backlog included, shared among hexes by area.",
		Format: FormatUSD,
		Scale: Scale{
			Kind: KindSequential, Transform: TransformLinear,
			Bands: []float64{1000, 2500, 5000, 10000, 20000},
		},
		Derive: &Derivation{Op: OpSum, Prop: PropYear1Need},
	})
}
