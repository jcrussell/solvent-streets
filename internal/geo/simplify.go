package geo

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/peterstace/simplefeatures/geom"
)

// maxAreaDeltaFraction is the tripwire on SimplifyGeoJSONMeters: if the
// simplified rings differ from the originals by more than this fraction of
// total ring area, the whole simplification is discarded and the caller gets
// the input back. At the 10 m default the observed delta is ~0.15% (measured
// across 27 cities), so 1% is roughly a 6x margin — loose enough not to fire on
// legitimate detail loss, tight enough to catch a projector or winding bug that
// mangles the shape.
const maxAreaDeltaFraction = 0.01

// SimplifyGeoJSONMeters reduces the vertex count of a Polygon or MultiPolygon
// GeoJSON geometry using Ramer-Douglas-Peucker at a tolerance expressed in
// METERS, and rounds the emitted coordinates to `decimals` places.
//
// The output is DISPLAY-ONLY and is not guaranteed to be a valid polygon: RDP
// can make a ring self-intersect. That is invisible on a line layer (the only
// thing the client draws with it) but would matter to a consumer filling the
// rings or doing geometry on them. Never feed this back into a computation —
// the authoritative boundary is whatever the store holds.
//
// FAILURE CONTRACT: on any problem this returns the INPUT STRING UNCHANGED plus
// a non-nil error. Callers are expected to emit the returned string
// unconditionally and treat the error as advisory ("fell back, worth a
// warning"). That keeps a weird-but-serviceable stored boundary rendering
// exactly as it does today instead of turning it into a hard failure on a path
// that currently always succeeds.
func SimplifyGeoJSONMeters(gjson string, proj *UTMProjector, tolMeters float64, decimals int) (string, error) {
	// A non-positive tolerance is the byte-exact opt-out. This short-circuit is
	// also what keeps a negative value away from LineString.Simplify, which
	// matters more than the bytes: ramerDouglasPeucker breaks its inner loop on
	// `maxDist <= threshold`, and with a negative (or NaN) threshold that is
	// never true, so the interval never collapses and the call never returns.
	// Do not "simplify" this away.
	if !(tolMeters > 0) {
		return gjson, nil
	}
	if proj == nil {
		return gjson, errors.New("simplify: nil projector")
	}

	var raw struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gjson), &raw); err != nil {
		return gjson, fmt.Errorf("simplify: parse geometry: %w", err)
	}

	var acc areaAccumulator
	var out any

	switch raw.Type {
	case geomTypePolygon:
		var rings [][][2]float64
		if err := json.Unmarshal(raw.Coordinates, &rings); err != nil {
			return gjson, fmt.Errorf("simplify: parse polygon coordinates: %w", err)
		}
		out = simplifyRings(rings, proj, tolMeters, decimals, &acc)
	case geomTypeMultiPolygon:
		var parts [][][][2]float64
		if err := json.Unmarshal(raw.Coordinates, &parts); err != nil {
			return gjson, fmt.Errorf("simplify: parse multipolygon coordinates: %w", err)
		}
		simplified := make([][][][2]float64, len(parts))
		for i, rings := range parts {
			simplified[i] = simplifyRings(rings, proj, tolMeters, decimals, &acc)
		}
		out = simplified
	default:
		// Feature, GeometryCollection, Point, an antimeridian-spanning shape a
		// projector can't be derived for — all of it renders today. Hand it
		// back untouched rather than failing the export or the request.
		return gjson, fmt.Errorf("simplify: unsupported geometry type %q, want Polygon or MultiPolygon", raw.Type)
	}

	if delta := acc.deltaFraction(); delta > maxAreaDeltaFraction {
		return gjson, fmt.Errorf("simplify: area changed by %.3f%% at %g m tolerance, over the %.0f%% limit",
			delta*100, tolMeters, maxAreaDeltaFraction*100)
	}

	encoded, err := json.Marshal(map[string]any{"type": raw.Type, "coordinates": out})
	if err != nil {
		return gjson, fmt.Errorf("simplify: marshal geometry: %w", err)
	}
	return string(encoded), nil
}

// simplifyRings simplifies every ring of one polygon. The RING COUNT IS ALWAYS
// PRESERVED: len(out) == len(in). That is the invariant that keeps small
// islands and holes alive, and it is why this does not route through
// Polygon.Simplify / MultiPolygon.Simplify, which drop rings and whole parts
// that collapse (type_polygon.go:698-701, type_multi_polygon.go:579-581).
func simplifyRings(rings [][][2]float64, proj *UTMProjector, tolMeters float64, decimals int, acc *areaAccumulator) [][][2]float64 {
	out := make([][][2]float64, len(rings))
	for i, ring := range rings {
		out[i] = simplifyRing(ring, proj, tolMeters, decimals, acc)
	}
	return out
}

// simplifyRing simplifies a single closed ring, falling back through
// progressively safer options rather than ever returning something degenerate.
//
// Note on parsing: rings arrive as [][2]float64, so a GeoJSON coordinate with a
// third element (elevation) has it silently DROPPED by encoding/json rather
// than reaching geom.NewSequence with an odd length — NewSequence panics on
// len(coords)%Dimension() != 0 (type_sequence.go:30-33), and this shape is what
// makes that unreachable. The tradeoff is that Z is discarded, which is correct
// for a 2D display boundary.
func simplifyRing(ring [][2]float64, proj *UTMProjector, tolMeters float64, decimals int, acc *areaAccumulator) [][2]float64 {
	projected := projectCoords(ring, proj)
	acc.addBefore(signedRingArea(projected))

	candidate := projected
	simplified := geom.NewLineString(coordsToSequence(projected)).Simplify(tolMeters)

	// LineString.Simplify returns a ZERO-COORDINATE LineString with NO error
	// when the result would be invalid (type_line_string.go:429-432), so
	// without this check a ring can vanish outright. The threshold is 4 rather
	// than 3 because a closed ring needs 4 coordinates (3 distinct + the repeat
	// of the first); at 3 it would admit degenerate rings. Deleting the check
	// is what loses rings — loosening it to 3 only lets in degenerate ones.
	if seq := simplified.Coordinates(); seq.Length() >= 4 {
		candidate = sequenceToCoords(seq)
	}

	acc.addAfter(signedRingArea(candidate))

	// Rounding happens AFTER simplification and can itself degenerate a ring
	// that passed the check above: RDP keeps vertices that are far from the
	// chord but can still be centimetres apart, and at coarse precision those
	// collapse onto each other. A 4-coordinate ring with only 2 distinct points
	// has zero area and draws as a spike, so re-check and step back to the
	// unsimplified ring, then to the raw ring, rather than emitting it.
	if rounded := unprojectAndRound(candidate, proj, decimals); distinctPoints(rounded) >= 3 {
		return rounded
	}
	if rounded := unprojectAndRound(projected, proj, decimals); distinctPoints(rounded) >= 3 {
		return rounded
	}
	return ring
}

// unprojectAndRound converts projected meters back to [lon, lat] at the given
// precision. The fallback paths round too, so one file never mixes 7-decimal
// Nominatim coordinates with 6-decimal simplified ones.
func unprojectAndRound(coords [][2]float64, proj *UTMProjector, decimals int) [][2]float64 {
	out := make([][2]float64, len(coords))
	for i, c := range coords {
		lon, lat := proj.FromProjected(c[0], c[1])
		out[i] = [2]float64{roundTo(lon, decimals), roundTo(lat, decimals)}
	}
	return out
}

func sequenceToCoords(seq geom.Sequence) [][2]float64 {
	out := make([][2]float64, seq.Length())
	for i := range out {
		xy := seq.GetXY(i)
		out[i] = [2]float64{xy.X, xy.Y}
	}
	return out
}

// distinctPoints counts distinct coordinates in a closed ring, ignoring the
// closing repeat of the first point. A valid ring needs at least 3.
func distinctPoints(ring [][2]float64) int {
	seen := make(map[[2]float64]struct{}, len(ring))
	for _, c := range ring {
		seen[c] = struct{}{}
	}
	return len(seen)
}

// signedRingArea is the shoelace area of a ring in projected units.
func signedRingArea(ring [][2]float64) float64 {
	if len(ring) < 3 {
		return 0
	}
	var sum float64
	for i := range len(ring) - 1 {
		sum += ring[i][0]*ring[i+1][1] - ring[i+1][0]*ring[i][1]
	}
	return sum / 2
}

// areaAccumulator totals ring area before and after simplification for the
// tripwire.
//
// It sums the ABSOLUTE area of every ring rather than the signed net area, for
// two reasons: GeoJSON winding is not reliably exterior-CCW/holes-CW in the
// wild, so signed sums would depend on input hygiene; and a hole that shrinks
// is a real shape change that a signed sum would cancel against its exterior
// instead of reporting.
//
// Two blind spots worth knowing, since this is a canary and not a proof:
//   - It is an AGGREGATE. An island worth 0.05% of the total can lose 90% of
//     itself without moving a 1% threshold. Ring-count preservation, not this,
//     is what keeps small parts alive.
//   - A ring whose distinct points are collinear has zero area and so
//     contributes nothing to either side of the ratio.
type areaAccumulator struct {
	before float64
	after  float64
}

func (a *areaAccumulator) addBefore(v float64) { a.before += math.Abs(v) }
func (a *areaAccumulator) addAfter(v float64)  { a.after += math.Abs(v) }

func (a *areaAccumulator) deltaFraction() float64 {
	if a.before == 0 {
		return 0
	}
	return math.Abs(a.before-a.after) / a.before
}
