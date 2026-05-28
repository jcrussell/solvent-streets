package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// stripCoverageMinRatio is the minimum fraction of bbox-queried road
// features whose first coord must fall inside a stored boundary for the
// boundary to pass the post-strip road-coverage gate. Calibrated on
// 2026-05-28 against the broken cohort (Brisbane, SF, Albany, Pinole,
// Belvedere: 0.000–0.006) versus the worst working case (Tiburon at
// 0.383, NYC at 0.473). The 0.15 threshold gives a 60× margin above
// the broken cohort and a 2.5× margin below the worst working case.
// Living-document: tune up only with evidence that a real-world
// working city dropped below this floor; tune down only after seeing a
// real inversion squeak through. Solvent-streets-e5mk.
const stripCoverageMinRatio = 0.15

// ErrBoundaryInvertedVsRoads signals that BOTH the stripped boundary
// AND the unstripped Nominatim boundary contain too few of the city's
// own ingested road features. This is the last-resort hard error after
// the rollback path also fails — it almost always means Nominatim
// returned the wrong administrative unit (or the relation-id override
// points at the wrong city). Sentinel so callers can errors.Is.
var ErrBoundaryInvertedVsRoads = errors.New("boundary excludes most of the city's own roads")

// validateBoundaryAgainstRoads returns the fraction of road features
// whose first coord falls strictly inside boundaryGJSON. ok is false
// when there's nothing to measure (no roads, or the boundary geometry
// is unparseable); callers should skip the gate in that case rather
// than fail.
//
// Roads are unimpeachable land probes: a `highway=*` way's first node
// is its starting intersection, virtually always on land. Bridge
// midpoints over water are not relevant because we only consult coords[0],
// which lives at the bridge's land-side approach. See solvent-streets-e5mk
// for why a road-based gate is the right shape of defense (this is
// iteration 10 in a long lineage of fixes that previously used the
// boundary's own PointOnSurface as a probe).
func validateBoundaryAgainstRoads(boundaryGJSON string, roads []db.Feature) (float64, bool) {
	if len(roads) == 0 {
		return 0, false
	}
	idx := indexBoundary(boundaryGJSON)
	if len(idx) == 0 {
		return 0, false
	}
	var inside, total int
	for _, r := range roads {
		coord, ok := extractFirstCoord(r.GeometryJSON)
		if !ok {
			continue
		}
		total++
		if pointInIndexed(coord, idx) {
			inside++
		}
	}
	if total == 0 {
		return 0, false
	}
	return float64(inside) / float64(total), true
}

// extractFirstCoord returns the first [lon, lat] of a GeoJSON
// LineString / Polygon / MultiLineString. Polygons return the
// exterior ring's first vertex. Other types return ok=false.
func extractFirstCoord(geometryJSON string) ([2]float64, bool) {
	var obj struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(geometryJSON), &obj); err != nil {
		return [2]float64{}, false
	}
	switch obj.Type {
	case "LineString":
		var coords [][2]float64
		if err := json.Unmarshal(obj.Coordinates, &coords); err != nil || len(coords) == 0 {
			return [2]float64{}, false
		}
		return coords[0], true
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(obj.Coordinates, &rings); err != nil || len(rings) == 0 || len(rings[0]) == 0 {
			return [2]float64{}, false
		}
		return rings[0][0], true
	case "MultiLineString":
		var lines [][][2]float64
		if err := json.Unmarshal(obj.Coordinates, &lines); err != nil || len(lines) == 0 || len(lines[0]) == 0 {
			return [2]float64{}, false
		}
		return lines[0][0], true
	}
	return [2]float64{}, false
}

// bboxedPoly bundles a polygon's rings with its exterior-ring bbox so
// the gate can skip pointInRing entirely for probes outside the bbox.
// Bbox-rejecting per polygon turns the worst-case O(probes × vertices)
// per polygon into O(probes_in_polygon_bbox × vertices) for tested
// probes plus O(probes) for the bbox check itself.
type bboxedPoly struct {
	rings                  [][][2]float64
	minX, minY, maxX, maxY float64
}

// indexBoundary parses a GeoJSON Polygon / MultiPolygon into a slice
// of bbox-tagged polygons. Returns nil for any other shape or on
// parse failure — callers treat nil as "can't run the gate."
func indexBoundary(boundaryGJSON string) []bboxedPoly {
	var raw struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(boundaryGJSON), &raw); err != nil {
		return nil
	}
	var polys [][][][2]float64
	switch raw.Type {
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(raw.Coordinates, &rings); err != nil {
			return nil
		}
		polys = [][][][2]float64{rings}
	case "MultiPolygon":
		if err := json.Unmarshal(raw.Coordinates, &polys); err != nil {
			return nil
		}
	default:
		return nil
	}
	out := make([]bboxedPoly, 0, len(polys))
	for _, rings := range polys {
		if len(rings) == 0 || len(rings[0]) == 0 {
			continue
		}
		minX, minY, maxX, maxY := ringBBox(rings[0])
		out = append(out, bboxedPoly{rings: rings, minX: minX, minY: minY, maxX: maxX, maxY: maxY})
	}
	return out
}

func ringBBox(ring [][2]float64) (minX, minY, maxX, maxY float64) {
	minX, maxX = ring[0][0], ring[0][0]
	minY, maxY = ring[0][1], ring[0][1]
	for _, c := range ring[1:] {
		if c[0] < minX {
			minX = c[0]
		}
		if c[0] > maxX {
			maxX = c[0]
		}
		if c[1] < minY {
			minY = c[1]
		}
		if c[1] > maxY {
			maxY = c[1]
		}
	}
	return
}

// pointInIndexed tests a (lon, lat) against an indexed boundary.
// Inside means: inside the exterior ring of any sub-polygon AND not
// inside any of that sub-polygon's interior rings.
func pointInIndexed(p [2]float64, polys []bboxedPoly) bool {
	for _, bp := range polys {
		if p[0] < bp.minX || p[0] > bp.maxX || p[1] < bp.minY || p[1] > bp.maxY {
			continue
		}
		if !pointInRing(p, bp.rings[0]) {
			continue
		}
		inHole := false
		for _, hole := range bp.rings[1:] {
			if pointInRing(p, hole) {
				inHole = true
				break
			}
		}
		if !inHole {
			return true
		}
	}
	return false
}

// pointInRing is a ray-cast point-in-polygon test. Ring is in lon/lat
// (planar; point-in-polygon is topological so equal-area projection is
// unnecessary). Mirrors internal/ingest/rings.go::pointInRing — copied
// rather than imported so pkg/cmd/ingest does not depend on
// internal/ingest's polygon machinery.
func pointInRing(p [2]float64, ring [][2]float64) bool {
	if len(ring) < 4 {
		return false
	}
	x, y := p[0], p[1]
	inside := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}
	return inside
}

// describeGateFailure produces the hint text for ErrBoundaryInvertedVsRoads.
// Extracted so the message lives next to the threshold and stays in sync
// when either is tuned.
func describeGateFailure(cityName string, ratioStripped, ratioOriginal float64) string {
	return fmt.Sprintf(
		"Stripped boundary coverage %.1f%%; unstripped Nominatim coverage %.1f%%; "+
			"both below %.0f%% threshold. Nominatim probably returned the wrong "+
			"administrative unit for %q. Fix: find the OSM admin_level=8 relation "+
			"at https://overpass-turbo.eu/ and set [[cities]].boundary_relation_id "+
			"in pvmt.toml, or set skip=true if this city is intentionally untracked.",
		100*ratioStripped, 100*ratioOriginal, 100*stripCoverageMinRatio, cityName,
	)
}

// roadsForCoverageGate returns the road feature set the gate should
// validate against, plus whether the gate has anything to do. The
// freshly-ingested features are reused when this run is roads-ingest;
// for parking/sidewalks-ingest, roads are read from the DB if a prior
// run stored them. ok=false means the gate must skip (no roads
// available) — that preserves current per-resource workflow for
// pre-roads ingest runs.
func roadsForCoverageGate(ctx context.Context, store db.Store, opts *Options, justIngested []db.Feature) ([]db.Feature, bool) {
	if opts.ResourceType.Type() == resource.TypeRoads {
		return justIngested, len(justIngested) > 0
	}
	roads, err := store.ListFeatures(ctx, resource.TypeRoads)
	if err != nil || len(roads) == 0 {
		return nil, false
	}
	return roads, true
}

// applyRoadCoverageGate runs the post-strip road-coverage check
// described in solvent-streets-e5mk. It does nothing when the stripped
// boundary already passes; otherwise it rewrites the saved boundary
// back to the unstripped Nominatim shape (and source label). When even
// the unstripped Nominatim fails the gate, returns
// ErrBoundaryInvertedVsRoads wrapped with an operator-actionable hint.
//
// Caller is responsible for already having saved the stripped boundary
// (resolveBoundary does this); this function only overwrites it via
// SaveBoundary on rollback. Logs go to errOut so prose stays out of
// stdout per byob-iostreams.3.
func applyRoadCoverageGate(
	ctx context.Context,
	store db.Store,
	cityName string,
	currentBoundary string,
	fresh *freshBoundary,
	roads []db.Feature,
	errOut io.Writer,
) error {
	ratioStripped, ok := validateBoundaryAgainstRoads(currentBoundary, roads)
	if !ok {
		return nil
	}
	if ratioStripped >= stripCoverageMinRatio {
		return nil
	}

	// Strip is broken. Try rolling back to unstripped Nominatim.
	ratioOriginal, ok := validateBoundaryAgainstRoads(fresh.Nominatim, roads)
	if !ok || ratioOriginal < stripCoverageMinRatio {
		return cmdutil.Hintf(
			fmt.Errorf("%w: city=%q stripped=%.3f original=%.3f", ErrBoundaryInvertedVsRoads, cityName, ratioStripped, ratioOriginal),
			"%s", describeGateFailure(cityName, ratioStripped, ratioOriginal),
		)
	}

	if err := store.SaveBoundary(ctx, fresh.Nominatim, fresh.UnstrippedSource); err != nil {
		return fmt.Errorf("rollback boundary: %w", err)
	}
	fmt.Fprintf(errOut,
		"  Water-strip rollback for %s: stripped boundary contained %.1f%% of road centroids (< %.0f%% threshold); restored unstripped Nominatim (%.1f%% coverage, source=%s).\n",
		cityName, 100*ratioStripped, 100*stripCoverageMinRatio, 100*ratioOriginal, fresh.UnstrippedSource,
	)
	return nil
}
