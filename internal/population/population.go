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
	"fmt"
	"sync/atomic"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/metric"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// MinResidents is the population below which a hex's per-resident ratios
// are left empty: dividing by a handful of people turns a quiet road into an
// alarming outlier.
const MinResidents = 10

// census says where the counts come from; perResident ends each ratio's
// definition.
const census = "2020 Census, spread from each census block onto the hexes it overlaps by area."

var perResident = fmt.Sprintf(" divided by the hex's population; blank where fewer than %d people live.", MinResidents)

func init() {
	// Band edges are absolute (the Spec is static per key). Counts depend on
	// hex size, so their edges assume the default 100 m hex edge (~26,000 m²);
	// density does not. Skewed values get roughly log-spaced edges by hand —
	// Transform is advisory and the dashboard paints the literal edges.
	hexmetric.Register(hexmetric.Spec{
		Key: metric.Population, Label: "Population", Source: "People counted in the " + census,
		Format: hexmetric.FormatInt, Additive: true,
		Scale: hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{10, 50, 100, 200, 400}},
	})
	hexmetric.Register(hexmetric.Spec{
		Key: metric.HousingUnits, Label: "Housing units", Source: "Homes counted in the " + census,
		Format: hexmetric.FormatInt, Additive: true,
		Scale: hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{5, 25, 50, 100, 200}},
	})
	hexmetric.Register(hexmetric.Spec{
		// FormatDensity: stored per m², shown per the viewer's large area
		// unit. The edges are 500 to 20,000 residents per km².
		Key: metric.PopulationDensity, Label: "Population density", Source: "Residents per unit of the hex's land inside the city, from the " + census,
		Format: hexmetric.FormatDensity,
		Scale:  hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{0.0005, 0.002, 0.005, 0.01, 0.02}},
	})
	hexmetric.Register(hexmetric.Spec{
		// FormatArea: stored in m² per resident, shown in the viewer's unit.
		Key: metric.MaintainedAreaPerResident, Label: "Maintained area per resident", Source: "Maintained area of the toggled resources" + perResident,
		Format: hexmetric.FormatArea,
		Scale:  hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{20, 40, 60, 100, 200}},
		Derive: &hexmetric.Derivation{Op: hexmetric.OpRatio, Prop: hexmetric.PropArea, Den: metric.Population, MinDen: MinResidents},
	})
	hexmetric.Register(hexmetric.Spec{
		Key: metric.Year1NeedPerResident, Label: "Year-1 need per resident",
		Source: "Year-1 need of the toggled resources, backlog included," + perResident,
		Format: hexmetric.FormatUSD,
		Scale:  hexmetric.Scale{Kind: hexmetric.KindSequential, Transform: hexmetric.TransformLog, Bands: []float64{25, 50, 100, 200, 400}},
		Derive: &hexmetric.Derivation{Op: hexmetric.OpRatio, Prop: hexmetric.PropYear1Need, Den: metric.Population, MinDen: MinResidents},
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
				m.Density = append(m.Density, db.HexMetric{HexID: sh.HexID, Value: p / a})
			}
		}
		if u > 0 {
			m.HousingUnits = append(m.HousingUnits, db.HexMetric{HexID: sh.HexID, Value: u})
		}
	}
	return m
}
