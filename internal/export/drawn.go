package export

import (
	"context"
	"sort"
	"sync"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
)

// DrawnGrid is the hex grid hexgrid.geojson draws for one city: the
// boundary-clipped, sliver-filtered grid (cityHexGrid), less any hex
// hexGeometryJSON cannot encode. It is the one definition of "a hex the map
// can paint": hex-metrics.json and meta.json's per-metric summaries carry
// values for exactly these hexes, so the panel never counts a hex the map
// cannot show. Stored metric rows outside it (an edge or shoreline sliver
// under min_hex_area, an id outside the clipped grid) are left out.
//
// The grid is built at most once however many of the city's files ask for
// it, and only when asked: a city with no metric rows and no coverage never
// builds it. Building it clips the lattice and encodes each hex's GeoJSON
// geometry once; hexgrid.geojson emits those encodings and the drawn id set
// is their keys, so the two cannot disagree. Share one DrawnGrid across a
// city's meta.json, hex-metrics.json and hexgrid.geojson (exportCityData
// does; the live server caches one per city and snapshot): each is a
// separate build otherwise.
//
// A DrawnGrid is for one city entry (one snapshot, boundary and hex edge)
// and is safe for concurrent use. Its build runs under the context it was
// made with, and an error is kept: a caller that caches a DrawnGrid must drop
// it when a build over it fails, as the server does.
type DrawnGrid struct {
	drawn func() (*drawnHexes, error)
	ids   func() (map[string]bool, error)

	// rows, when non-nil (keepRows), holds each stored metric's drawn rows
	// keyed by metric and subject, so meta.json's summaries and
	// hex-metrics.json read them once between them. Only the static export
	// keeps them: the live server holds its grids for its lifetime, and the
	// rows would stay with them.
	rowsMu sync.Mutex
	rows   map[[2]string]func() ([]db.HexMetric, error)
}

// keepRows makes g keep the drawn metric rows it is asked for
// (drawnMetricRows), for a caller that builds every file over g at once and
// then drops it.
func (g *DrawnGrid) keepRows() {
	g.rowsMu.Lock()
	defer g.rowsMu.Unlock()
	if g.rows == nil {
		g.rows = map[[2]string]func() ([]db.HexMetric, error){}
	}
}

// keptRows returns the once-only read of metric key over subject when g keeps
// rows (keepRows), building it with read; nil otherwise.
func (g *DrawnGrid) keptRows(key, subject string, read func() ([]db.HexMetric, error)) func() ([]db.HexMetric, error) {
	g.rowsMu.Lock()
	defer g.rowsMu.Unlock()
	if g.rows == nil {
		return nil
	}
	k := [2]string{key, subject}
	if f, ok := g.rows[k]; ok {
		return f
	}
	f := sync.OnceValues(read)
	g.rows[k] = f
	return f
}

// drawnHexes is a built DrawnGrid: the drawn hexes' encoded geometries,
// keyed by hex id, and the ids in sorted order (hexgrid.geojson's order).
type drawnHexes struct {
	geometry map[string]string
	order    []string
}

// NewDrawnGrid returns entry's DrawnGrid. proj is the projector
// hexgrid.geojson is drawn with; nil derives it from the entry's boundary, as
// every caller that draws the grid does, when the grid is first needed.
// Nothing is read until a build asks for the grid.
func NewDrawnGrid(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) *DrawnGrid {
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
	g := &DrawnGrid{}
	g.drawn = sync.OnceValues(func() (*drawnHexes, error) {
		p, err := projector()
		if err != nil {
			return nil, err
		}
		hexes, err := cityHexGrid(ctx, entry, p)
		if err != nil {
			return nil, err
		}
		d := encodeDrawnHexes(ctx, hexes, p, entry.Config.CoordinateDecimals())
		if err := ctx.Err(); err != nil {
			return nil, err // canceled part-way: the grid is incomplete
		}
		return d, nil
	})
	g.ids = sync.OnceValues(func() (map[string]bool, error) {
		// hexgrid.geojson is written only when some resource has per-hex
		// coverage (BuildHexGeoJSON); without it the map draws nothing.
		hasGrid, err := hasHexGrid(ctx, entry)
		if err != nil || !hasGrid {
			return nil, err
		}
		d, err := g.drawn()
		if err != nil {
			return nil, err
		}
		ids := make(map[string]bool, len(d.order))
		for _, id := range d.order {
			ids[id] = true
		}
		return ids, nil
	})
	return g
}

// encodeDrawnHexes encodes each hex's geometry once, in parallel, keeping
// the hexes that encode.
func encodeDrawnHexes(ctx context.Context, hexes []geo.Hex, proj *geo.UTMProjector, decimals int) *drawnHexes {
	type encoded struct{ id, gjson string }
	enc := geo.ParallelMap(ctx, hexes, func(_ int, h geo.Hex) []encoded {
		if gjson, ok := hexGeometryJSON(&h, proj, decimals); ok {
			return []encoded{{h.ID, gjson}}
		}
		return nil
	}, nil)
	d := &drawnHexes{geometry: make(map[string]string, len(enc)), order: make([]string, 0, len(enc))}
	for _, e := range enc {
		d.geometry[e.id] = e.gjson
		d.order = append(d.order, e.id)
	}
	// Sorted by id so unchanged data yields a byte-identical hexgrid.geojson
	// across regens.
	sort.Strings(d.order)
	return d
}

// drawnRows keeps the rows whose hex the map draws (ids, from DrawnGrid.ids).
func drawnRows(rows []db.HexMetric, ids map[string]bool) []db.HexMetric {
	var kept []db.HexMetric
	for _, r := range rows {
		if ids[r.HexID] {
			kept = append(kept, r)
		}
	}
	return kept
}
