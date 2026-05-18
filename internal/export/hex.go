package export

import (
	"context"
	"encoding/json"

	"github.com/peterstace/simplefeatures/geom"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// BuildHexGeoJSONs builds GeoJSON FeatureCollections for both scopes — city
// (rows tagged ":city" by compute) and bbox (bare-label rows). city is nil
// when no ":city" rows exist; that signal hides the scope toggle in the UI.
// bbox is nil when no rows of any kind exist.
func BuildHexGeoJSONs(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) (city, bbox map[string]any) {
	return buildHexGeoJSONForScope(ctx, entry, proj, resource.ScopeCity),
		buildHexGeoJSONForScope(ctx, entry, proj, resource.ScopeAll)
}

// buildHexGeoJSONForScope builds one scope's FeatureCollection. Returns nil
// when no matching hex_stats rows exist for that scope.
func buildHexGeoJSONForScope(ctx context.Context, entry CityEntry, proj *geo.UTMProjector, scope resource.Scope) map[string]any {
	var allStats []db.HexStat
	for _, rt := range resource.All {
		stats, err := entry.Store.ListHexStats(ctx, rt.Type().With(scope))
		if err != nil {
			continue
		}
		allStats = append(allStats, stats...)
	}

	if len(allStats) == 0 {
		return nil
	}

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
	for _, st := range allStats {
		if feat, ok := buildHexFeature(st, hexMap, proj); ok {
			features = append(features, feat)
		}
	}

	return map[string]any{
		"type":     "FeatureCollection",
		"features": features,
	}
}

// filterHexSlivers drops hexes whose geometry area is below minArea sqm.
// Threshold lives in config.DisplayConfig.MinHexAreaSqM (resolved via
// Config.MinHexAreaSqM()); the filter sits in BuildHexGeoJSONs rather
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

// buildHexFeature builds a single GeoJSON feature from a hex stat entry.
// The ":city" scope suffix on st.ResourceType is stripped from the emitted
// resource_type property — the scope dimension is carried by the enclosing
// FeatureCollection's file name (hexgrid-city vs hexgrid-bbox), not by
// per-feature labels the client would otherwise have to split.
// Returns the feature map and true if successful, or nil and false otherwise.
func buildHexFeature(st db.HexStat, hexMap map[string]*geo.Hex, proj *geo.UTMProjector) (map[string]any, bool) {
	h, ok := hexMap[st.HexID]
	if !ok {
		return nil, false
	}
	gjson, err := geo.GeometryToGeoJSON(h.Geom, proj)
	if err != nil {
		return nil, false
	}
	return map[string]any{
		"type":     "Feature",
		"geometry": json.RawMessage(gjson),
		"properties": map[string]any{
			"hex_id":        st.HexID,
			"resource_type": string(st.ResourceType.Bare()),
			"area_sqm":      st.AreaSqM,
			"pct_covered":   st.PctCovered,
		},
	}, true
}
