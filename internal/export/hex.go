package export

import (
	"context"
	"encoding/json"
	"fmt"
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
// the scope toggle in the UI).
//
// Returns (nil, nil) when no hex_stats rows exist (a legitimate empty state),
// but propagates any real ListHexStats DB error so callers can evict and retry
// instead of caching a blank grid for the server's lifetime — mirroring
// serveBoundaryGeoJSON's empty-vs-error split.
func BuildHexGeoJSON(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) (map[string]any, error) {
	decimals := entry.Config.CoordinateDecimals()

	aggs, order, err := aggregateHexStats(ctx, entry)
	if err != nil {
		return nil, err
	}
	if len(order) == 0 {
		// nil map signals "no hex stats" — a legitimate empty both callers
		// handle (server returns an empty FC; static export skips the file).
		// The error slot is reserved for real ListHexStats failures, which
		// aggregateHexStats already surfaced above.
		return nil, nil //nolint:nilnil // nil map = legitimate empty, distinct from the propagated DB error above
	}
	// ListHexStats has no ORDER BY, so sort hex IDs here for reproducible
	// output — unchanged data yields a byte-identical file across regens.
	sort.Strings(order)

	bbox, _, _, _ := entry.BBoxAndCenter(ctx)
	hexEdge := entry.Config.ResolvedHexEdge(&entry.City)
	minX, minY, maxX, maxY := geo.ProjectedBBoxExtent(proj, bbox)
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)

	hexes = clipHexGridToBoundary(ctx, hexes, entry, proj)
	hexes = filterHexSlivers(hexes, entry.Config.MinHexArea())

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
	}, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// aggregateHexStats collects hex_stats rows across every resource and both
// scopes into per-hex aggregates. It returns the aggregates keyed by hex ID and
// the first-seen hex order; the caller sorts this for reproducible output.
//
// ListHexStats returns a nil slice and no error for an un-computed resource
// (a legitimate empty), so any non-nil error here is a real DB failure and is
// propagated — distinguishing "no data" from "the query failed" so the caller
// doesn't memoize a blank grid on a transient error.
func aggregateHexStats(ctx context.Context, entry CityEntry) (map[string]*hexAgg, []string, error) {
	aggs := make(map[string]*hexAgg)
	var order []string
	for _, rt := range resource.All {
		res := string(rt.Type().Bare())
		for _, scope := range []resource.Scope{resource.ScopeAll, resource.ScopeCity} {
			stats, err := entry.Store.ListHexStats(ctx, rt.Type().With(scope))
			if err != nil {
				return nil, nil, fmt.Errorf("listing hex stats for %s: %w", rt.Type().With(scope), err)
			}
			for _, st := range stats {
				a := aggs[st.HexID]
				if a == nil {
					a = &hexAgg{}
					aggs[st.HexID] = a
					order = append(order, st.HexID)
				}
				a.set(res, scope, round2(st.Area), round2(st.PctCovered))
			}
		}
	}
	return aggs, order, nil
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
// Threshold lives in config.DisplayConfig.MinHexArea (resolved via
// Config.MinHexArea()); the filter sits in BuildHexGeoJSON rather
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

	// geom.Intersection runs a full OverlayNG per call, rebuilding the
	// boundary's spatial index from scratch every time — for tens of
	// thousands of hexes that dominates export. Prepare the boundary once so
	// its index is cached, then use cheap prepared predicates to clip only the
	// hexes that actually straddle the boundary edge:
	//   - not intersecting  -> drop (hex lies entirely outside the city)
	//   - fully covered     -> keep as-is (hex ∩ boundary == hex, no overlay)
	//   - otherwise         -> the expensive overlay, but only for the thin
	//                          perimeter band.
	// If Prepare fails, fall back to the unconditional per-hex overlay.
	prep, pErr := geom.Prepare(boundaryGeom)

	filtered := make([]geo.Hex, 0, len(hexes))
	for _, h := range hexes {
		if pErr == nil {
			if intersects, iErr := prep.Intersects(h.Geom); iErr == nil && !intersects {
				continue
			}
			if covers, cErr := prep.Covers(h.Geom); cErr == nil && covers {
				filtered = append(filtered, h)
				continue
			}
		}
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
