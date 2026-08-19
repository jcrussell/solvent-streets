package export

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"sort"
	"sync"

	"github.com/peterstace/simplefeatures/geom"

	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// hexAgg accumulates a single hex's coverage across resources and scopes.
// bbox/city map a bare resource name -> {"area", "pct"}; city stays nil until
// a ":city" row lands, which doubles as the "this hex is in the city" signal.
//
// Note this is the BUILD-side signal and is not the same as the emitted shape:
// a non-nil city equal to bbox is emitted as "city_same" rather than a "city"
// object (see buildHexFeature).
type hexAgg struct {
	bbox map[string]map[string]float64
	city map[string]map[string]float64
}

// BuildHexGeoJSON builds a single GeoJSON FeatureCollection covering both
// scopes — one feature per hex, geometry emitted once. Each feature carries
// "bbox" coverage keyed by resource name, plus ONE OF:
//
//   - "city": city-scope coverage, when it differs from bbox;
//   - "city_same": 1, when the two are identical (the common case, ~70% of
//     features) and the duplicate is elided;
//   - neither, when the hex has no ":city" rows at all.
//
// Only the third case means "no city-scope data". A city whose features are all
// in that case leaves the client's city scope empty and hides the scope toggle
// — note this is the absence of BOTH keys, not the absence of "city".
//
// The FeatureCollection carries "v": 2 for that property shape; see the version
// comment below.
//
// Returns (nil, nil) when no hex_stats rows exist (a legitimate empty state),
// but propagates any real ListHexStats DB error so callers can evict and retry
// instead of caching a blank grid for the server's lifetime — mirroring
// serveBoundaryGeoJSON's empty-vs-error split.
func BuildHexGeoJSON(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) (map[string]any, error) {
	return buildHexGeoJSONFromGrid(ctx, entry, proj, newCityHexGridOnce(ctx, entry, proj))
}

// buildHexGeoJSONFromGrid is BuildHexGeoJSON with the grid supplied as a thunk
// so exportCityData can share one build with buildPlayHexesFromGrid. Behaviour
// is identical: the thunk is forced at exactly the point cityHexGrid used to be
// called, which is *after* the no-hex-stats early return below.
func buildHexGeoJSONFromGrid(ctx context.Context, entry CityEntry, proj *geo.UTMProjector, grid cityHexGridFunc) (map[string]any, error) {
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

	hexes, err := grid()
	if err != nil {
		return nil, err
	}

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
		"type": "FeatureCollection",
		// Format version. Bumped to 2 when city_same dedup landed (B6b): a
		// client that expands "city" but not "city_same" would silently drop
		// ~70% of city-scope features rather than fail, so the client refuses
		// to render a file it doesn't recognize instead. Any future change to
		// the property shape must bump this too.
		"v":        2,
		"features": features,
	}, nil
}

// sameCoverage reports whether two resources' {area, pct} maps are equal.
//
// EXACT float equality is deliberate and is only safe because BOTH scopes go
// through round2 in aggregateHexStats before they ever reach here — they are
// two aggregations of the same underlying geometry, so when the city polygon
// doesn't clip a hex they round to bit-identical values. If that rounding ever
// moves (or one scope stops being rounded), this silently stops matching and
// the dedup quietly turns itself off: the output stays CORRECT, just large
// again. Nothing would fail, so check this function when hexgrid size regresses
// for no apparent reason.
func sameCoverage(a, b map[string]float64) bool {
	return maps.Equal(a, b)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// roundSig rounds v to digits significant figures. Unlike round2 it never
// flattens a nonzero magnitude to zero, which matters for values a consumer
// validates as strictly positive at any scale (BuildPlayHexes' k — the game
// engine rejects the whole board on k <= 0).
//
// Zero and non-finite inputs pass through. So does anything whose scaling would
// overflow (a denormal drives mag to +Inf and the quotient to NaN): emitting v
// at full precision is always safe, emitting NaN is not. digits below 1 is a
// caller error and is treated as 1 — otherwise the scaling collapses and the
// overflow fallback would hand back the unrounded input, silently doing nothing.
func roundSig(v float64, digits int) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	if digits < 1 {
		digits = 1
	}
	mag := math.Pow(10, float64(digits-1)-math.Floor(math.Log10(math.Abs(v))))
	r := math.Round(v*mag) / mag
	if r == 0 || math.IsNaN(r) || math.IsInf(r, 0) {
		return v
	}
	return r
}

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
// Threshold lives in config.DisplayConfig.MinHexArea with a per-city
// CityConfig.MinHexArea override (resolved via Config.ResolvedMinHexArea, which
// pairs with the per-city hex edge); the filter sits in BuildHexGeoJSON rather
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

// cityHexGrid builds the boundary-clipped, sliver-filtered hex grid for entry.
// It is the single source of the grid for both BuildHexGeoJSON (the served
// hexgrid.geojson) and BuildPlayHexes (the /play board); sharing one function
// keeps their hex ids ("hex:col:row") in lock-step so the front-end join holds.
// cityHexGridFunc defers a cityHexGrid build to its first use and memoizes the
// result. Both consumers return early in their own empty cases (no hex stats /
// no road features) before they need a grid, so an eagerly-built grid would add
// a full lattice+clip for those cities and would turn a cityHexGrid error from
// "skip this file" into "abort the whole export".
type cityHexGridFunc func() ([]geo.Hex, error)

// newCityHexGridOnce builds the grid at most once for the given city. The
// returned slice is shared read-only by every caller: BuildHexGeoJSON only takes
// &hexes[i] to read, and geo.ComputeHexStats takes each Hex by value.
func newCityHexGridOnce(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) cityHexGridFunc {
	return sync.OnceValues(func() ([]geo.Hex, error) {
		return cityHexGrid(ctx, entry, proj)
	})
}

func cityHexGrid(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) ([]geo.Hex, error) {
	bbox, _, _, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return nil, err
	}
	hexEdge := entry.Config.ResolvedHexEdge(&entry.City)
	minX, minY, maxX, maxY := geo.ProjectedBBoxExtent(proj, bbox)
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	hexes = clipHexGridToBoundary(ctx, hexes, entry, proj)
	hexes = filterHexSlivers(hexes, entry.Config.ResolvedMinHexArea(&entry.City))
	return hexes, nil
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
	switch {
	case agg.city == nil:
		// No ":city" rows: this hex is outside the city polygon. Neither key is
		// emitted, and THAT — not the absence of "city" alone — is what tells
		// the client there is no city-scope data here.
	case maps.EqualFunc(agg.city, agg.bbox, sameCoverage):
		// Identical to bbox, which is the common case: ~70% of features
		// site-wide. Emit a flag instead of a byte-for-byte duplicate; the
		// client reads it back as a reference to "bbox".
		props["city_same"] = 1
	default:
		props["city"] = agg.city
	}
	return map[string]any{
		"type":       "Feature",
		"geometry":   json.RawMessage(gjson),
		"properties": props,
	}, true
}
