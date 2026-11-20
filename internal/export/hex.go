package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"

	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/metric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// HexGridFileVersion is the "v" of hexgrid.geojson; the client refuses any
// other value. Any change to the feature property shape must bump it — the
// client and the exporter ship together, so a bump costs nothing, while a
// client misreading a changed shape (e.g. expanding "city" but not
// "city_same") would silently drop features instead of failing.
const HexGridFileVersion = 6

// hexAgg accumulates a single hex's coverage across resources and scopes.
// all/city map a bare resource name -> {"area", "share"}; city stays nil until
// a ":city" row lands, which doubles as the "this hex is in the city" signal.
//
// Note this is the BUILD-side signal and is not the same as the emitted shape:
// a non-nil city equal to all is emitted as "city_same" rather than a "city"
// object (see buildHexFeature).
type hexAgg struct {
	all  map[string]map[string]float64
	city map[string]map[string]float64
	// k is the hex's city-jurisdiction road decay rate under the config's
	// decay_rate override (roadDecayRates); 0 when the hex has none.
	k float64
}

// BuildHexGeoJSON builds a single GeoJSON FeatureCollection covering both
// scopes — one feature per hex, geometry emitted once. Each feature carries
// "all" coverage keyed by resource name -> {"area" (m²), "share" (0..1 of the
// hex covered)}, plus ONE OF:
//
//   - "city": city-scope coverage, when it differs from all;
//   - "city_same": 1, when the two are identical (the common case, ~70% of
//     features) and the duplicate is elided;
//   - neither, when the hex has no ":city" rows at all.
//
// Only the third case means "no city-scope data". A city whose features are all
// in that case leaves the client's city scope empty and hides the scope toggle
// — note this is the absence of BOTH keys, not the absence of "city".
//
// Every hex of the clipped grid is a feature: a hex with no pavement rows
// carries only "id", so a stored metric such as population can still paint it.
// The FeatureCollection's "v" is HexGridFileVersion.
//
// A parking entry additionally carries "spaces" — the estimated parking spaces
// apportioned to the hex, rounded to a whole count — when it is at least one.
//
// A hex with city-jurisdiction roads also carries "k": the area-weighted blend
// of its road classes' decay rates under the config's decay_rate override (see
// roadDecayRates). It is a city-scope value that sits beside "all" and
// "city"/"city_same" rather than inside the city object, so it does not defeat
// the city_same dedup. The /play board reads it with the city-scope roads area.
//
// Returns (nil, nil) when no per-hex coverage exists (a legitimate empty
// state), but propagates any real coverage-read DB error so callers can evict and retry
// instead of caching a blank grid for the server's lifetime — mirroring
// serveBoundaryGeoJSON's empty-vs-error split.
func BuildHexGeoJSON(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) (map[string]any, error) {
	return BuildHexGeoJSONOn(ctx, entry, NewDrawnGrid(ctx, entry, proj))
}

// BuildHexGeoJSONOn is BuildHexGeoJSON drawn on grid, the city's DrawnGrid,
// which a caller shares with meta.json and hex-metrics.json so the three
// build one grid between them.
func BuildHexGeoJSONOn(ctx context.Context, entry CityEntry, grid *DrawnGrid) (map[string]any, error) {
	return buildHexGeoJSONFromGrid(ctx, entry, grid.drawn)
}

// drawnHexesFunc defers a DrawnGrid build until BuildHexGeoJSON needs it.
type drawnHexesFunc func() (*drawnHexes, error)

// buildHexGeoJSONFromGrid is BuildHexGeoJSON with the grid supplied as a thunk,
// forced only *after* the no-hex-stats early return below, so a city with no
// hex stats never builds a grid.
func buildHexGeoJSONFromGrid(ctx context.Context, entry CityEntry, grid drawnHexesFunc) (map[string]any, error) {
	aggs, err := aggregateHexStats(ctx, entry)
	if err != nil {
		return nil, err
	}
	if len(aggs) == 0 {
		// nil map signals "no hex stats" — a legitimate empty both callers
		// handle (server returns an empty FC; static export skips the file).
		// The error slot is reserved for real coverage-read failures, which
		// aggregateHexStats already surfaced above.
		return nil, nil //nolint:nilnil // nil map = legitimate empty, distinct from the propagated DB error above
	}
	drawn, err := grid()
	if err != nil {
		return nil, err
	}

	// Every drawn hex is emitted, with or without pavement rows, so a stored
	// metric such as population can paint hexes that hold no pavement. Hex
	// stats for ids outside the grid (all-scope rows beyond the boundary)
	// have no geometry and are dropped. drawn.order is sorted by id so
	// unchanged data yields a byte-identical file across regens.
	features := make([]map[string]any, 0, len(drawn.order))
	for _, hexID := range drawn.order {
		features = append(features, buildHexFeature(hexID, aggs[hexID], drawn.geometry[hexID]))
	}

	return map[string]any{
		"type":     "FeatureCollection",
		"v":        HexGridFileVersion,
		"features": features,
	}, nil
}

// sameCoverage reports whether two resources' {area, share} maps are equal.
//
// EXACT float equality is deliberate and is only safe because BOTH scopes go
// through the same rounding in aggregateHexStats before they ever reach here — they are
// two aggregations of the same underlying geometry, so when the city polygon
// doesn't clip a hex they round to bit-identical values. If that rounding ever
// moves (or one scope stops being rounded), this silently stops matching and
// the dedup quietly turns itself off: the output stays CORRECT, just large
// again. Nothing would fail, so check this function when hexgrid size regresses
// for no apparent reason.
func sameCoverage(a, b map[string]float64) bool {
	return maps.Equal(a, b)
}

func round2(v float64) float64 { return geo.RoundTo(v, 2) }

// round4 rounds a 0..1 share to four decimals (a hundredth of a percent).
func round4(v float64) float64 { return geo.RoundTo(v, 4) }

// roundSig rounds v to digits significant figures. Unlike round2 it never
// flattens a nonzero magnitude to zero, which matters for values a consumer
// validates as strictly positive at any scale (hexgrid's k — the game engine
// rejects the whole board on k <= 0).
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

// aggregateHexStats collects per-hex coverage across every resource and both
// scopes into per-hex aggregates keyed by hex ID. The caller emits one feature
// per grid hex in sorted id order, so no order is returned here.
//
// LoadHexCoverage returns a nil slice and no error for an un-computed resource
// (a legitimate empty), so any non-nil error here is a real DB failure and is
// propagated — distinguishing "no data" from "the query failed" so the caller
// doesn't memoize a blank grid on a transient error.
func aggregateHexStats(ctx context.Context, entry CityEntry) (map[string]*hexAgg, error) {
	aggs := make(map[string]*hexAgg)
	for _, rt := range resource.All {
		res := string(rt.Type().Bare())
		for _, scope := range []resource.Scope{resource.ScopeAll, resource.ScopeCity} {
			stats, err := results.LoadHexCoverage(ctx, entry.Store, rt.Type().With(scope))
			if err != nil {
				return nil, fmt.Errorf("listing hex stats for %s: %w", rt.Type().With(scope), err)
			}
			spaces, err := hexSpaces(ctx, entry, rt.Type().With(scope), stats)
			if err != nil {
				return nil, err
			}
			for _, st := range stats {
				a := aggs[st.HexID]
				if a == nil {
					a = &hexAgg{}
					aggs[st.HexID] = a
				}
				// Spaces are rounded to a whole count with the same function
				// for both scopes: sameCoverage compares the maps exactly, so
				// asymmetric rounding would silently switch off the city_same
				// dedup for every parking hex.
				a.set(res, scope, round2(st.Area), round4(st.Share), math.Round(spaces[st.HexID]))
			}
		}
	}
	ks, err := roadDecayRates(ctx, entry)
	if err != nil {
		return nil, err
	}
	for id, k := range ks {
		if a := aggs[id]; a != nil {
			a.k = k
		}
	}
	return aggs, nil
}

// roadDecayRates returns each city-jurisdiction road hex's decay rate keyed by
// hex ID, for hexgrid's "k". The roads compute stores the blend at no override
// (metric.DefaultDecayRate, k0 = Σ area_c·rate_c / Σ area_c over the
// hex's classes); an override scales every road class by the same factor
// (forecast.ClassDecayRate → ScaleRoadDecay: rate·override/anchor), so it
// scales the blend by that factor too:
//
//	k = Σ area_c·rate_c·f / Σ area_c = k0·f
//
// which keeps k on the rates forecast.json and the seed cohorts use. Rounded to
// four significant figures, which never flattens a positive rate to zero.
func roadDecayRates(ctx context.Context, entry CityEntry) (map[string]float64, error) {
	rows, err := entry.Store.ListHexMetrics(ctx, metric.DefaultDecayRate, resource.Subject(resource.TypeRoads.With(resource.ScopeCity)))
	if err != nil {
		return nil, fmt.Errorf("listing hex road decay rates: %w", err)
	}
	var override float64
	if entry.Config != nil {
		override = entry.Config.ResolvedForecast(&entry.City).DecayRate
	}
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		k := r.Value
		if override > 0 {
			k = forecast.ScaleRoadDecay(k, override)
		}
		out[r.HexID] = roundSig(k, 4)
	}
	return out, nil
}

// hexSpaces returns t's per-hex parking spaces keyed by hex ID, read from
// hex_metrics (metric.EstimatedSpaces). The parking compute writes its
// coverage and its spaces under ONE run, and the snapshot holds one parking
// run, so the two always describe the same compute. The map is empty, never
// nil, when t carries no spaces.
func hexSpaces(ctx context.Context, entry CityEntry, t resource.Type, stats []results.HexCoverage) (map[string]float64, error) {
	out := map[string]float64{}
	if t.Bare() != resource.TypeParking || len(stats) == 0 {
		return out, nil
	}
	rows, err := entry.Store.ListHexMetrics(ctx, metric.EstimatedSpaces, resource.Subject(t))
	if err != nil {
		return nil, fmt.Errorf("listing hex parking spaces for %s: %w", t, err)
	}
	for _, r := range rows {
		out[r.HexID] = r.Value
	}
	return out, nil
}

// set records one resource's coverage for the given scope, lazily allocating
// the scope's per-resource map. City coverage staying nil doubles as the
// "this hex is not in the city" signal. spaces is emitted only when at least
// one whole space lands in the hex, so non-parking resources (always 0) and
// hexes with a sub-space sliver carry no key.
func (a *hexAgg) set(res string, scope resource.Scope, area, share, spaces float64) {
	m := map[string]float64{"area": area, "share": share}
	if spaces >= 1 {
		m["spaces"] = spaces
	}
	if scope == resource.ScopeCity {
		if a.city == nil {
			a.city = make(map[string]map[string]float64)
		}
		a.city[res] = m
		return
	}
	if a.all == nil {
		a.all = make(map[string]map[string]float64)
	}
	a.all[res] = m
}

// fullHexArea is the area, in m², of an unclipped flat-top hex of the given
// edge.
func fullHexArea(edge float64) float64 { return 3 * math.Sqrt(3) / 2 * edge * edge }

// sliverThresholdWarning returns a warning when the city's min_hex_area would
// drop every hex of the lattice it is drawn at: the threshold is sized
// against the hex edge (Config.ResolvedMinHexArea), but a snapshot renders on
// its own edge, which may be finer than the one the config now has. "" when
// the threshold leaves whole hexes in place.
func sliverThresholdWarning(entry CityEntry, edge float64) string {
	if entry.Config == nil {
		return ""
	}
	minArea := entry.Config.ResolvedMinHexArea(&entry.City)
	if full := fullHexArea(edge); minArea >= full {
		return fmt.Sprintf("%s: min_hex_area %.0f m² is at least the area of a whole hex (%.0f m²) at "+
			"the %g m hex edge its snapshot was computed at, so its map shows no hexes; lower min_hex_area",
			entry.City.Name, minArea, full, edge)
	}
	return ""
}

// filterHexSlivers drops hexes whose geometry area is below minArea sqm.
// Threshold lives in config.DisplayConfig.MinHexArea with a per-city
// CityConfig.MinHexArea override (resolved via Config.ResolvedMinHexArea, which
// pairs with the per-city hex edge); the filter sits in BuildHexGeoJSON rather
// than ComputeHexStats so paved_share's numerator/denominator scope matches.
// Used after the boundary clip (cityHexGrid) to skip the visual misrepresentation
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

// cityHexGrid builds the boundary-clipped, sliver-filtered hex grid for entry:
// the grid of the served hexgrid.geojson, which the map and the /play board
// both draw (DrawnGrid). BuildHexGeoJSON forces it only after its
// no-hex-stats early return, so a city with no hex stats pays for no
// lattice+clip, and a cityHexGrid error there is "skip this file" rather
// than "abort the export".
func cityHexGrid(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) ([]geo.Hex, error) {
	bbox, _, _, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return nil, err
	}
	// The lattice and the clip compute stored the per-hex results on
	// (geo.CityLattice, geo.ClipHexesToBoundaryGeoJSON), so every stored hex
	// id has the hex it was computed for. Both come from the snapshot the
	// store reads: its hex edge (HexEdge) and the boundary it froze
	// (GetBoundary on a pinned store, which BBoxAndCenter and the projector
	// were derived from too), not the current config and boundary. Without a
	// usable boundary compute ran over the unclipped lattice, and so does
	// this. The clip is the localized one (ClipHexesToBoundaryLocal): the
	// same hexes and shapes, to rounding, in a fraction of the time, which is
	// what lets the live server build a large city's grid inside a request.
	edge, err := entry.HexEdge(ctx)
	if err != nil {
		return nil, err
	}
	hexes := geo.CityLattice(proj, bbox, edge)
	boundaryGJSON, err := entry.Store.GetBoundary(ctx)
	if err != nil {
		return nil, fmt.Errorf("boundary for %s: %w", entry.City.Name, err)
	}
	hexes, clipErr := geo.ClipHexesToBoundaryGeoJSONLocal(ctx, hexes, boundaryGJSON, proj)
	if clipErr != nil && !errors.Is(clipErr, geo.ErrNoClipBoundary) {
		return nil, clipErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err // canceled part-way: the clip is incomplete
	}
	// The sliver filter is display-only: compute keeps slivers so paved
	// totals stay whole; the map leaves them out.
	return filterHexSlivers(hexes, entry.Config.ResolvedMinHexArea(&entry.City)), nil
}

// buildHexFeature builds one GeoJSON feature for a hex from its aggregated
// per-resource coverage and its encoded geometry (DrawnGrid). Geometry is
// emitted once (clipped to the boundary); properties carry "id" plus nested
// scope objects {all, city?} keyed by bare resource name -> {"area",
// "share"}. The scope dimension lives in these per-feature objects rather
// than in separate files, and "city" is omitted when the hex has no ":city"
// rows.
func buildHexFeature(hexID string, agg *hexAgg, gjson string) map[string]any {
	props := map[string]any{"id": hexID}
	if agg == nil {
		// A grid hex with no pavement rows: geometry only, so stored metrics
		// can still paint it.
		return map[string]any{
			"type":       "Feature",
			"geometry":   json.RawMessage(gjson),
			"properties": props,
		}
	}
	props["all"] = agg.all
	if agg.k > 0 {
		props["k"] = agg.k
	}
	switch {
	case agg.city == nil:
		// No ":city" rows: this hex is outside the city polygon. Neither key is
		// emitted, and THAT — not the absence of "city" alone — is what tells
		// the client there is no city-scope data here.
	case maps.EqualFunc(agg.city, agg.all, sameCoverage):
		// Identical to all, which is the common case: ~70% of features
		// site-wide. Emit a flag instead of a byte-for-byte duplicate; the
		// client reads it back as a reference to "all".
		props["city_same"] = 1
	default:
		props["city"] = agg.city
	}
	return map[string]any{
		"type":       "Feature",
		"geometry":   json.RawMessage(gjson),
		"properties": props,
	}
}

// hexGeometryJSON is h's WGS84 GeoJSON geometry as hexgrid.geojson emits it;
// ok is false for a hex that cannot be encoded, which the map then does not
// draw. DrawnGrid encodes each hex with it once, and both hexgrid.geojson's
// features and the drawn id set hex-metrics.json covers come from that one
// result.
func hexGeometryJSON(h *geo.Hex, proj *geo.UTMProjector, decimals int) (string, bool) {
	gjson, err := geo.GeometryToGeoJSONWithPrecision(h.Geom, proj, decimals)
	if err != nil {
		return "", false
	}
	return gjson, true
}
