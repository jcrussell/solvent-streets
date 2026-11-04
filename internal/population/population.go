// Package population turns 2020 Census blocks into per-hex metrics: each
// block's population and housing units are apportioned onto the hex grid by
// area overlap (geo.ComputeHexWeightedSharesN), and density divides the
// population by the hex's own clipped area, so a hex cut by the city
// boundary is not understated.
//
// The metrics are stored per hex (hex_metrics) and registered here, so the
// dashboard's "Color by" offers them with no population-specific client code.
// Apportioning by area assumes people are spread evenly within a block —
// blocks are small (a city block in town), so the error is a within-block
// shift, never a change in the city total.
package population

import (
	"context"
	"sync/atomic"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// Metric keys.
const (
	KeyPopulation   = "population"
	KeyHousingUnits = "housing_units"
	KeyDensity      = "population_per_km2"
	// KeyRoadAreaPerResident and KeyNeedPerResident are derived ratios the
	// dashboard computes over the toggled resources.
	KeyRoadAreaPerResident = "road_m2_per_resident"
	KeyNeedPerResident     = "year1_need_usd_per_resident"
)

// MinResidents is the population below which a hex's per-resident ratios
// are left empty: dividing by a handful of people turns a quiet road into an
// alarming outlier.
const MinResidents = 10

const source = "2020 Census blocks (TIGERweb), apportioned to hexes by area"

func init() {
	// Band edges are absolute (the Spec is static per key). Counts depend on
	// hex size, so their edges assume the default 100 m hex edge (~26,000 m²);
	// density does not. Skewed values get roughly log-spaced edges by hand —
	// Transform is advisory and the dashboard paints the literal edges.
	hexmetric.Register(hexmetric.Spec{
		Key: KeyPopulation, Label: "Population", Unit: "", Source: source,
		Format: hexmetric.FormatInt, Additive: true,
		Scale: hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{10, 50, 100, 200, 400}},
	})
	hexmetric.Register(hexmetric.Spec{
		Key: KeyHousingUnits, Label: "Housing units", Unit: "", Source: source,
		Format: hexmetric.FormatInt, Additive: true,
		Scale: hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{5, 25, 50, 100, 200}},
	})
	hexmetric.Register(hexmetric.Spec{
		Key: KeyDensity, Label: "Population density (per km²)", Unit: "1/km2", Source: source,
		Format: hexmetric.FormatInt,
		Scale:  hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{500, 2000, 5000, 10000, 20000}},
	})
	hexmetric.Register(hexmetric.Spec{
		Key: KeyRoadAreaPerResident, Label: "Paved m² per resident", Unit: "m2", Source: "Paved area of the toggled resources ÷ " + source,
		Format: hexmetric.FormatFloat1,
		Scale:  hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{20, 40, 60, 100, 200}},
		Derive: &hexmetric.Derivation{Op: hexmetric.OpRatio, Prop: hexmetric.PropArea, Den: KeyPopulation, MinDen: MinResidents},
	})
	hexmetric.Register(hexmetric.Spec{
		Key: KeyNeedPerResident, Label: "Year-1 need per resident", Unit: "usd",
		Source: "Year-1 do-nothing need (incl. backlog) of the toggled resources ÷ " + source,
		Format: hexmetric.FormatUSD,
		Scale:  hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{25, 50, 100, 200, 400}},
		Derive: &hexmetric.Derivation{Op: hexmetric.OpRatio, Prop: hexmetric.PropEstCost, Den: KeyPopulation, MinDen: MinResidents},
	})
}

// Metrics is the stored per-hex output of one apportionment.
type Metrics struct {
	Population   []db.HexMetric
	HousingUnits []db.HexMetric
	Density      []db.HexMetric
	// Total is the population that landed inside the grid.
	Total float64
}

// Apportion spreads the blocks' counts onto grid. blocks are the projected,
// cleaned block polygons (resource.CensusBlocks.BufferFeaturesPaired) in the
// grid's projection. Hexes that receive nothing are omitted. ctx
// cancellation yields a partial result; callers check ctx.Err() before
// saving.
func Apportion(ctx context.Context, grid []geo.Hex, blocks []resource.BufferedFeature, counter *atomic.Int64) Metrics {
	pop := make([]float64, len(blocks))
	hu := make([]float64, len(blocks))
	for i, b := range blocks {
		pop[i], hu[i] = resource.CensusCounts(b.Feature)
	}
	hexArea := make(map[string]float64, len(grid))
	for _, h := range grid {
		hexArea[h.ID] = h.Geom.Area()
	}
	var m Metrics
	for _, sh := range geo.ComputeHexWeightedSharesN(ctx, grid, resource.Geoms(blocks), [][]float64{pop, hu}, counter) {
		p, u := sh.Values[0], sh.Values[1]
		m.Total += p
		if p > 0 {
			m.Population = append(m.Population, db.HexMetric{HexID: sh.HexID, Value: p})
			if a := hexArea[sh.HexID]; a > 0 {
				m.Density = append(m.Density, db.HexMetric{HexID: sh.HexID, Value: p / (a / 1e6)})
			}
		}
		if u > 0 {
			m.HousingUnits = append(m.HousingUnits, db.HexMetric{HexID: sh.HexID, Value: u})
		}
	}
	return m
}
