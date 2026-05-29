package export

import (
	"context"
	"encoding/json"
	"math"
	"sort"

	"github.com/peterstace/simplefeatures/geom"

	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// hexAgg accumulates a single hex's coverage across resources and scopes.
// bbox/city map a bare resource name -> {"area", "pct"}; city stays nil until
// a ":city" row lands, which doubles as the "this hex is in the city" signal.
type hexAgg struct {
	bbox map[string]map[string]float64
	city map[string]map[string]float64
}

// BuildHexGeoJSON builds a single GeoJSON FeatureCollection covering both
// scopes — one feature per hex, geometry emitted once. Each feature carries
// nested {bbox, city?} objects keyed by resource name; "city" is omitted when
// the hex has no ":city" rows (so a city whose features all lack "city" hides
// the scope toggle in the UI). Returns nil when no hex_stats rows exist.
func BuildHexGeoJSON(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) map[string]any {
	decimals := entry.Config.CoordinateDecimals()

	aggs, order := aggregateHexStats(ctx, entry)
	if len(order) == 0 {
		return nil
	}
	// ListHexStats has no ORDER BY, so sort hex IDs here for reproducible
	// output — unchanged data yields a byte-identical file across regens.
	sort.Strings(order)

	bbox, _, _, _ := entry.BBoxAndCenter(ctx)
	hexEdge := entry.Config.ResolvedHexEdge(&entry.City)
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0])
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2])
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)

	hexes = clipHexGridToBoundary(ctx, hexes, entry, proj)
	hexes = filterHexSlivers(hexes, entry.Config.MinHexAreaSqM())

	hexMap := make(map[string]*geo.Hex, len(hexes))
	for i := range hexes {
		hexMap[hexes[i].ID] = &hexes[i]
	}

	var features []map[string]any
	for _, hexID := range order {
		if feat, ok := buildHexFeature(hexID, aggs[hexID], hexMap, proj, decimals); ok {
			features = append(features, feat)
		}
	}

	return map[string]any{
		"type":     "FeatureCollection",
		"features": features,
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// aggregateHexStats collects hex_stats rows across every resource and both
// scopes into per-hex aggregates. It returns the aggregates keyed by hex ID and
// the first-seen hex order; the caller sorts this for reproducible output.
func aggregateHexStats(ctx context.Context, entry CityEntry) (map[string]*hexAgg, []string) {
	aggs := make(map[string]*hexAgg)
	var order []string
	for _, rt := range resource.All {
		res := string(rt.Type().Bare())
		for _, scope := range []resource.Scope{resource.ScopeAll, resource.ScopeCity} {
			stats, err := entry.Store.ListHexStats(ctx, rt.Type().With(scope))
			if err != nil {
				continue
			}
			for _, st := range stats {
				a := aggs[st.HexID]
				if a == nil {
					a = &hexAgg{}
					aggs[st.HexID] = a
					order = append(order, st.HexID)
				}
				a.set(res, scope, round2(st.AreaSqM), round2(st.PctCovered))
			}
		}
	}
	return aggs, order
}

// set records one resource's coverage for the given scope, lazily allocating
// the scope's per-resource map. City coverage staying nil doubles as the
// "this hex is not in the city" signal.
func (a *hexAgg) set(res string, scope resource.Scope, area, pct float64) {
	m := map[string]float64{"area": area, "pct": pct}
	if scope == resource.ScopeCity {
		if a.city == nil {
			a.city = make(map[string]map[string]float64)
		}
		a.city[res] = m
		return
	}
	if a.bbox == nil {
		a.bbox = make(map[string]map[string]float64)
	}
	a.bbox[res] = m
}

// filterHexSlivers drops hexes whose geometry area is below minArea sqm.
// Threshold lives in config.DisplayConfig.MinHexAreaSqM (resolved via
// Config.MinHexAreaSqM()); the filter sits in BuildHexGeoJSON rather
// than ComputeHexStats so pct_paved's numerator/denominator scope matches.
// Used after clipHexGridToBoundary to skip the visual misrepresentation
// that a fully-covered sliver hex would produce on the heatmap.
func filterHexSlivers(hexes []geo.Hex, minArea float64) []geo.Hex {
	kept := make([]geo.Hex, 0, len(hexes))
	for _, h := range hexes {
		if h.Geom.Area() < minArea {
			continue
		}
		kept = append(kept, h)
	}
	return kept
}

func clipHexGridToBoundary(ctx context.Context, hexes []geo.Hex, entry CityEntry, proj *geo.UTMProjector) []geo.Hex {
	boundaryGJSON, err := entry.Store.GetBoundary(ctx)
	if err != nil || boundaryGJSON == "" {
		return hexes
	}
	boundaryGeom, _, gErr := geo.GeoJSONToProjectedGeometry(boundaryGJSON, proj)
	if gErr != nil || boundaryGeom.IsEmpty() {
		return hexes
	}
	filtered := make([]geo.Hex, 0, len(hexes))
	for _, h := range hexes {
		inter, iErr := geom.Intersection(h.Geom, boundaryGeom)
		if iErr == nil && !inter.IsEmpty() {
			h.Geom = inter
			filtered = append(filtered, h)
		}
	}
	return filtered
}

// buildHexFeature builds one GeoJSON feature for a hex from its aggregated
// per-resource coverage. Geometry is emitted once (clipped to the boundary by
// the caller); properties carry "id" plus nested scope objects {bbox, city?}
// keyed by bare resource name -> {"area", "pct"}. The scope dimension lives in
// these per-feature objects rather than in separate files, and "city" is
// omitted when the hex has no ":city" rows. Returns the feature and true on
// success, or nil and false when the hex has no geometry.
func buildHexFeature(hexID string, agg *hexAgg, hexMap map[string]*geo.Hex, proj *geo.UTMProjector, decimals int) (map[string]any, bool) {
	h, ok := hexMap[hexID]
	if !ok {
		return nil, false
	}
	gjson, err := geo.GeometryToGeoJSONWithPrecision(h.Geom, proj, decimals)
	if err != nil {
		return nil, false
	}
	props := map[string]any{
		"id":   hexID,
		"bbox": agg.bbox,
	}
	if agg.city != nil {
		props["city"] = agg.city
	}
	return map[string]any{
		"type":       "Feature",
		"geometry":   json.RawMessage(gjson),
		"properties": props,
	}, true
}
