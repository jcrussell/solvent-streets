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
		Key:    metric.PavedShare,
		Label:  "Coverage %",
		Source: "Share of the hex covered by the toggled resources (pvmt all compute)",
		Format: FormatPercent,
		Scale: Scale{
			Kind: KindCoverage, Transform: TransformLinear, Mode: ModeRamp,
			Bands: []float64{0, 0.1, 0.25, 0.5, 0.75, 1},
			Ticks: []float64{0, 0.25, 0.5, 0.75, 1},
		},
		Derive: &Derivation{Op: OpSum, Prop: PropShare, Max: 1},
	})
	Register(Spec{
		Key:    metric.PavedArea,
		Label:  "Maintained area",
		Source: "Area of the toggled resources inside the hex, summed per resource as the forecast prices them (pvmt all compute)",
		Format: FormatArea,
		Scale: Scale{
			Kind: KindSequential, Transform: TransformLinear,
			Bands: []float64{500, 1000, 2000, 4000, 8000},
		},
		Derive: &Derivation{Op: OpSum, Prop: PropArea},
	})
	Register(Spec{
		Key:    metric.EstimatedSpaces,
		Label:  "Parking spaces",
		Source: "Estimated parking spaces apportioned to the hex by lot area (pvmt all compute)",
		Format: FormatInt,
		Scale: Scale{
			Kind: KindSequential, Transform: TransformLinear,
			Bands: []float64{10, 25, 50, 100, 250},
		},
		Derive: &Derivation{Op: OpSum, Prop: PropSpaces},
	})
	Register(Spec{
		Key:    metric.Year1Need,
		Label:  "Est. year-1 cost",
		Source: "Each toggled resource's year-1 need (pvmt forecast) split by the hex's share of its area",
		Format: FormatUSD,
		Scale: Scale{
			Kind: KindSequential, Transform: TransformLinear,
			Bands: []float64{1000, 2500, 5000, 10000, 20000},
		},
		Derive: &Derivation{Op: OpSum, Prop: PropYear1Need},
	})
}
