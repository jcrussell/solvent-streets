package export

import (
	"context"
	"sync"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
)

// drawnGrid is the hex grid hexgrid.geojson draws for one city: the
// boundary-clipped, sliver-filtered grid (cityHexGrid), less any hex
// buildHexFeature cannot encode. It is the one definition of "a hex the map
// can paint": hex-metrics.json and meta.json's per-metric summaries carry
// values for exactly these hexes, so the panel never counts a hex the map
// cannot show. Stored metric rows outside it (an edge or shoreline sliver
// under min_hex_area, an id outside the clipped grid) are left out.
//
// The lattice and clip are built at most once however many of the city's
// files ask for them (exportCityData shares one across hexgrid.geojson,
// meta.json and hex-metrics.json), and only when asked: a city with no
// metric rows and no coverage never builds them. A drawnGrid is for one
// city and one pass; it is safe for concurrent use.
type drawnGrid struct {
	hexes func() ([]geo.Hex, error)
	ids   func() (map[string]bool, error)
}

// newDrawnGrid returns entry's drawnGrid. proj is the projector hexgrid.geojson
// is drawn with; nil derives it from the entry's boundary, as every caller
// that draws the grid does, when the grid is first needed.
func newDrawnGrid(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) *drawnGrid {
	projector := sync.OnceValues(func() (*geo.UTMProjector, error) {
		if proj != nil {
			return proj, nil
		}
		_, lon, lat, err := entry.BBoxAndCenter(ctx)
		if err != nil {
			return nil, err
		}
		return geo.NewUTMProjector(lon, lat), nil
	})
	g := &drawnGrid{}
	g.hexes = sync.OnceValues(func() ([]geo.Hex, error) {
		p, err := projector()
		if err != nil {
			return nil, err
		}
		return cityHexGrid(ctx, entry, p)
	})
	g.ids = sync.OnceValues(func() (map[string]bool, error) {
		// hexgrid.geojson is written only when some resource has per-hex
		// coverage (BuildHexGeoJSON); without it the map draws nothing.
		hasGrid, err := hasHexGrid(ctx, entry)
		if err != nil || !hasGrid {
			return nil, err
		}
		hexes, err := g.hexes()
		if err != nil {
			return nil, err
		}
		p, err := projector()
		if err != nil {
			return nil, err
		}
		decimals := entry.Config.CoordinateDecimals()
		ids := make(map[string]bool, len(hexes))
		for i := range hexes {
			if _, ok := hexGeometryJSON(&hexes[i], p, decimals); ok {
				ids[hexes[i].ID] = true
			}
		}
		return ids, nil
	})
	return g
}

// drawnRows keeps the rows whose hex the map draws (ids, from drawnGrid.ids).
func drawnRows(rows []db.HexMetric, ids map[string]bool) []db.HexMetric {
	var kept []db.HexMetric
	for _, r := range rows {
		if ids[r.HexID] {
			kept = append(kept, r)
		}
	}
	return kept
}
