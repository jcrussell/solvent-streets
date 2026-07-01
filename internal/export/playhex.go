package export

import (
	"context"
	"fmt"
	"sort"

	"github.com/jcrussell/solvent-streets/internal/filter"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"

	"github.com/peterstace/simplefeatures/geom"
)

// PlayHex is one board cell for the /play game: a city-jurisdiction hex that
// carries actual road geometry, with its blended pavement decay rate. ID
// matches the hexgrid.geojson feature id ("hex:col:row") so the front-end can
// join /data/play-hexes.json onto the served grid by id.
type PlayHex struct {
	ID       string  `json:"id"`
	RoadArea float64 `json:"road_area"` // clipped road footprint in this hex, m²
	K        float64 `json:"k"`         // area-weighted blend of per-class decay rates
}

// hexBlend accumulates a hex's road footprint and area-weighted decay so K can
// be finalized as weighted/area once every class pass has contributed.
type hexBlend struct {
	area     float64
	weighted float64 // Σ_c(area_c · DecayRateForClass(c))
}

// BuildPlayHexes derives a per-hex blended pavement decay rate from REAL road
// geometry for the city-jurisdiction scope. For each hex it blends the decay
// rates of the road classes whose buffered footprint clips into that hex,
// weighted by in-hex area:
//
//	k_hex = Σ_c(area_c · DecayRateForClass(class_c)) / Σ_c area_c
//
// so arterials (motorway/trunk, low k) hold while residential/service (high k)
// rot first. Only road-bearing hexes are emitted; a hex with no clipped road
// area is omitted. Output is sorted by hex id for byte-stable serving.
//
// Geometry assembly shares the cityHexGrid helper with BuildHexGeoJSON — same
// grid (HexGrid over the projected boundary bbox), same boundary clip and
// sliver filter — so the emitted ids are a subset of the served hexgrid.geojson
// ids. Road features are read through the placeholder-safe ListFeatures store
// method and buffered once via the shared Pavement source; per-class per-hex
// clipping reuses geo.ComputeHexStats (errgroup fanout). ctx cancellation
// aborts the underlying ParallelMap cleanly.
func BuildPlayHexes(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) ([]PlayHex, error) {
	dbFeatures, err := entry.Store.ListFeatures(ctx, resource.TypeRoads)
	if err != nil {
		return nil, fmt.Errorf("listing road features: %w", err)
	}
	if len(dbFeatures) == 0 {
		return nil, nil
	}

	feats := make([]resource.Feature, len(dbFeatures))
	for i, f := range dbFeatures {
		feats[i] = resource.Feature{
			ID:           f.ID,
			Name:         f.Name,
			Tags:         f.Tags,
			GeometryJSON: f.GeometryJSON,
			SourceAPI:    f.SourceAPI,
		}
	}

	buffered := (&resource.Pavement{}).BufferFeaturesPaired(ctx, feats, proj)

	// Group the city-jurisdiction buffered footprints by canonical road class.
	// City scope matches the served roads:city hex layer (compute filters on
	// the same jurisdiction), so the play board lines up with what the map
	// shows.
	byClass := make(map[string][]geom.Geometry)
	for _, bf := range buffered {
		if filter.ClassifyJurisdiction(bf.Feature.Tags) != filter.JurisdictionCity {
			continue
		}
		class := forecast.NormalizeClass(bf.Feature.Tags["highway"])
		byClass[class] = append(byClass[class], bf.Geom)
	}
	if len(byClass) == 0 {
		return nil, nil
	}

	hexes, err := cityHexGrid(ctx, entry, proj)
	if err != nil {
		return nil, err
	}
	if len(hexes) == 0 {
		return nil, nil
	}

	// One per-class clip pass over the grid (geo.ComputeHexStats fans out across
	// hexes via the geo errgroup helper). Accumulate each hex's footprint and
	// area-weighted decay so K finalizes as weighted/area.
	blends := make(map[string]*hexBlend)
	for class, geoms := range byClass {
		rate := forecast.DecayRateForClass(class)
		idx := geo.NewGeomIndexFromGeoms(geoms)
		for _, st := range geo.ComputeHexStats(ctx, hexes, idx, class, nil) {
			b := blends[st.HexID]
			if b == nil {
				b = &hexBlend{}
				blends[st.HexID] = b
			}
			b.area += st.Area
			b.weighted += st.Area * rate
		}
	}

	out := make([]PlayHex, 0, len(blends))
	for id, b := range blends {
		if b.area <= 0 {
			continue
		}
		k := b.weighted / b.area
		if k <= 0 {
			// Defensive: every road class has a positive rate, so this only
			// fires if a hex somehow accrued area with no class weight.
			k = forecast.DefaultDecayRates["default"]
		}
		out = append(out, PlayHex{ID: id, RoadArea: b.area, K: k})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
