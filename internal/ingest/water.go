package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/logs"
)

// FetchOSMWater queries Overpass for OSM water polygons inside bbox and
// returns a single GeoJSON MultiPolygon string. Three source shapes
// are supported: closed `natural=water` ways, `natural=water`
// multipolygon relations (whose outer/inner member ways are stitched
// into rings here), and `natural=coastline` linestrings (which only
// enclose the sea once their endpoints are closed along the query
// bbox). Empty water responses return ("", nil) — callers must treat
// this as a benign no-op (no water in the bbox).
//
// landProbes is one lon/lat point per sub-polygon of the city
// boundary — see geo.InteriorPoints. They disambiguate coastline
// closures: an open chain's closure produces two candidate rings (CW
// and CCW around bbox edges), and the water-side ring is the one
// that contains NONE of the probes. If a candidate ring contains any
// probe it overlaps land and is wrong. If both candidates contain
// land probes, the chain doesn't cleanly separate water from land
// and is dropped. This invariant catches OSM coastline ways
// digitized in inconsistent orientations relative to the right-hand-
// water convention (the cause of solvent-streets-yrr0).
func FetchOSMWater(ctx context.Context, client *http.Client, bbox [4]float64, landProbes [][2]float64) (string, error) {
	return fetchOSMWater(ctx, client, overpassAPI, bbox, landProbes)
}

func fetchOSMWater(ctx context.Context, client *http.Client, baseURL string, bbox [4]float64, landProbes [][2]float64) (string, error) {
	// Split only the FETCH on truncation — never the ASSEMBLY. Each
	// quadrant query returns full geometry for every element it
	// intersects, so the deduped union of quadrant elements equals the
	// full-bbox element set. assembleWaterGeoJSON then runs ONCE against
	// the ORIGINAL bbox, so coastline closing and the land-probe logic
	// (both tied to the query bbox) behave identically to a single big
	// successful query — quadrant edges never enter the geometry.
	seen := make(map[string]bool)
	elements, err := fetchWaterElements(ctx, client, baseURL, bbox, seen, 0)
	if err != nil {
		return "", err
	}
	return assembleWaterGeoJSON(ctx, elements, bbox, landProbes)
}

// fetchWaterElements fetches the raw Overpass water elements for bbox,
// recursively splitting into quadrants when the server returns a
// truncation remark (timeout / OOM), exactly like fetchRecursive does for
// roads/parking. Elements are deduped by type+id via seen because a water
// body spanning a quadrant boundary is returned in full by every quadrant
// it touches. A quadrant still truncated at maxSplitDepth propagates the
// error (fail loud) rather than silently under-reporting water.
func fetchWaterElements(ctx context.Context, client *http.Client, baseURL string, bbox [4]float64, seen map[string]bool, depth int) ([]overpassElement, error) {
	body, err := postOverpass(ctx, client, baseURL, buildWaterQuery(bbox))
	if err != nil {
		return nil, err
	}
	elements, truncated, err := parseWaterElements(body)
	if err != nil {
		return nil, err
	}
	if truncated {
		if depth >= maxSplitDepth {
			return nil, fmt.Errorf("overpass water remark: %w", errOverpassTruncated)
		}
		var all []overpassElement
		for _, q := range splitBBox(bbox) {
			qElements, qErr := fetchWaterElements(ctx, client, baseURL, q, seen, depth+1)
			if qErr != nil {
				return nil, qErr
			}
			all = append(all, qElements...)
		}
		return all, nil
	}

	// Dedup by stable element key (type+id), mirroring fetchRecursive's
	// dedup of db.Feature.ID. A boundary-spanning element appears in
	// multiple quadrant responses with identical full geometry.
	unique := make([]overpassElement, 0, len(elements))
	for _, e := range elements {
		key := fmt.Sprintf("%s:%d", e.Type, e.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, e)
	}
	return unique, nil
}

// parseWaterElements unmarshals an Overpass water response body and
// reports whether the server truncated the result (runtime timeout / OOM
// remark). It does NOT error on truncation — the caller decides whether to
// split-and-retry — but does error on malformed JSON.
func parseWaterElements(data []byte) (elements []overpassElement, truncated bool, err error) {
	var resp overpassResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, false, fmt.Errorf("parse overpass water json: %w", err)
	}
	return resp.Elements, remarkIndicatesTruncation(resp.Remark), nil
}

// maxResponseBodyBytes caps every external API response (Overpass,
// ArcGIS, Nominatim) at 100 MB. Defends against runaway queries (e.g.,
// the global water shape if a bbox arg is botched) or hostile/buggy
// servers that try to drive the process OOM via an unbounded body.
// A var (not const) so tests can shrink it without round-tripping
// 100 MB through an httptest.Server.
var maxResponseBodyBytes int64 = 100 * 1024 * 1024

// postOverpass issues the project-standard Overpass query POST and
// returns the raw response body. Used by all Overpass call sites so
// the HTTP shape (UA, retry, response-size cap, status check) lives
// in one place.
func postOverpass(ctx context.Context, client *http.Client, baseURL, query string) ([]byte, error) {
	req, err := http.NewRequestWithContext(AllowRetry(ctx), http.MethodPost, baseURL, strings.NewReader(url.Values{"data": {query}}.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create overpass request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("overpass request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read overpass response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("overpass returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

func buildWaterQuery(bbox [4]float64) string {
	// bbox is [south, west, north, east] — Overpass expects the same order.
	// The coastline query is separate from the water query because the
	// post-processing differs (coastline ways need bbox-edge closing).
	return fmt.Sprintf(
		`[out:json][timeout:60];(way["natural"="water"](%f,%f,%f,%f);relation["natural"="water"](%f,%f,%f,%f);way["natural"="coastline"](%f,%f,%f,%f););out geom;`,
		bbox[0], bbox[1], bbox[2], bbox[3],
		bbox[0], bbox[1], bbox[2], bbox[3],
		bbox[0], bbox[1], bbox[2], bbox[3],
	)
}

// waterPolygon is an outer ring with optional holes (lon/lat pairs).
type waterPolygon struct {
	outer [][2]float64
	holes [][][2]float64
}

// parseWaterResponse parses one Overpass water response body and
// assembles its GeoJSON. It fails loudly on a truncation remark — a
// truncated water response would under-report water and silently inflate
// land area. fetchOSMWater no longer routes through this (it splits the
// fetch and assembles via assembleWaterGeoJSON), but it remains the
// single-response entry point exercised by the parse-layer tests.
func parseWaterResponse(ctx context.Context, data []byte, bbox [4]float64, landProbes [][2]float64) (string, error) {
	elements, truncated, err := parseWaterElements(data)
	if err != nil {
		return "", err
	}
	if truncated {
		return "", fmt.Errorf("overpass water remark: %w", errOverpassTruncated)
	}
	return assembleWaterGeoJSON(ctx, elements, bbox, landProbes)
}

// assembleWaterGeoJSON builds the water MultiPolygon GeoJSON from a set of
// raw Overpass elements against bbox: closed water ways, water relations,
// and coastline ways closed along the bbox edges (see FetchOSMWater). This
// is the bbox-tied assembly stage — it MUST run once against the original
// query bbox, never per quadrant, or coastlines would close along quadrant
// edges and corrupt land area. Returns ("", nil) when no water survives.
func assembleWaterGeoJSON(ctx context.Context, elements []overpassElement, bbox [4]float64, landProbes [][2]float64) (string, error) {
	bboxArea := bboxLonLatArea(bbox)
	probes := newLandProbeIndex(landProbes)
	polys := make([]waterPolygon, 0, len(elements))
	coastWays := make([][][2]float64, 0, len(elements))
	var sumBBoxFrac float64
	for _, e := range elements {
		coastline, addedPolys, addedFrac := classifyWaterElement(ctx, e, bboxArea, probes)
		if coastline != nil {
			coastWays = append(coastWays, coastline)
		}
		polys = append(polys, addedPolys...)
		sumBBoxFrac += addedFrac
	}

	for chainIdx, chain := range stitchCoastlineChains(coastWays) {
		coastlinePolys, addedFrac := closeAndAcceptCoastline(ctx, chainIdx, chain, bbox, bboxArea, probes)
		polys = append(polys, coastlinePolys...)
		sumBBoxFrac += addedFrac
	}

	logs.From(ctx).Debug("water parse: summary",
		"accepted_polygons", len(polys),
		"sum_bbox_frac", sumBBoxFrac,
	)

	if len(polys) == 0 {
		return "", nil
	}
	return polysToMultiPolygonGeoJSON(polys), nil
}

// classifyWaterElement handles one Overpass element. For coastlines it
// returns the raw coordinate sequence (the caller stitches them later);
// for closed water ways and water relations it returns the accepted
// polygons. The non-coastline path applies acceptWaterPolygon AND a
// per-polygon land-probe check (water polygons whose outer ring
// contains a presumed-land point are dropped — they're traced around
// land in the wrong direction). Rejected polygons are logged at WARN
// with their OSM id and reason.
func classifyWaterElement(ctx context.Context, e overpassElement, bboxArea float64, probes *landProbeIndex) (coastline [][2]float64, polys []waterPolygon, fracSum float64) {
	switch e.Type {
	case elementWay:
		coords := resolveWayCoords(e, nil)
		if e.Tags["natural"] == "coastline" {
			return coords, nil, 0
		}
		if !isClosedRing(coords) {
			return nil, nil, 0
		}
		if ok, reason := acceptWaterPolygon(coords, bboxArea); !ok {
			logs.From(ctx).Warn("water way: rejected polygon",
				"way", e.ID, "reason", reason, "vertices", len(coords),
			)
			return nil, nil, 0
		}
		if hits := probes.RingHits(coords); hits > 0 {
			logs.From(ctx).Warn("water way: dropped polygon containing city land",
				"way", e.ID, "land_hits", hits, "land_probes", probes.Len(),
				"vertices", len(coords),
			)
			return nil, nil, 0
		}
		frac := logAcceptedWaterPolygon(ctx, "water way: accepted polygon", "way", e.ID, coords, bboxArea)
		return nil, []waterPolygon{{outer: coords}}, frac
	case elementRelation:
		relPolys := polygonsFromRelation(ctx, e, bboxArea)
		kept := make([]waterPolygon, 0, len(relPolys))
		var total float64
		for _, p := range relPolys {
			if hits := probes.PolygonHits(p); hits > 0 {
				logs.From(ctx).Warn("water relation: dropped polygon containing city land",
					"relation", e.ID, "land_hits", hits, "land_probes", probes.Len(),
					"outer_vertices", len(p.outer), "holes", len(p.holes),
				)
				continue
			}
			total += logAcceptedWaterPolygon(ctx, "water relation: accepted polygon", "relation", e.ID, p.outer, bboxArea)
			kept = append(kept, p)
		}
		return nil, kept, total
	default:
		return nil, nil, 0
	}
}

// closeAndAcceptCoastline closes one stitched coastline chain into water-
// side rings. Returns the surviving rings as waterPolygons plus the sum of
// their bbox-area fractions for the summary log.
//
// Unlike the closed-way and relation-outer paths, coastline rings are NOT
// gated by the maxOuterBboxAreaFraction cap. By the time a ring is returned
// from closeCoastlineChain it has already passed that path's principled
// water/land discriminators — closed, CW orientation (CCW islands dropped),
// and RingHits == 0 (contains none of the city's land probes). The bbox-
// fraction cap adds nothing here and actively misfires for cities whose
// admin boundary extends into open water: their query bbox is mostly water,
// so the legitimate bay ring exceeds the cap and gets wrongly rejected
// (Foster City's SF Bay ring, 85.6% of a bay-dominated bbox — bd-86aa). The
// downstream clip-to-boundary (geo.SubtractGeoJSON) and the aggregate
// waterStripMinAreaRatio backstop guard against any over-subtraction.
func closeAndAcceptCoastline(ctx context.Context, chainIdx int, chain [][2]float64, bbox [4]float64, bboxArea float64, probes *landProbeIndex) (polys []waterPolygon, fracSum float64) {
	for _, ring := range closeCoastlineChain(ctx, chain, bbox, probes) {
		if !isClosedRing(ring) {
			logs.From(ctx).Warn("water coastline: rejected ring",
				"reason", "ring not closed", "vertices", len(ring),
			)
			continue
		}
		fracSum += logAcceptedWaterPolygon(ctx, "water coastline: accepted ring", "chain", int64(chainIdx), ring, bboxArea)
		polys = append(polys, waterPolygon{outer: ring})
	}
	return polys, fracSum
}

// logAcceptedWaterPolygon emits one Debug-level log line per accepted
// water polygon and returns the polygon's bbox-area fraction so the
// caller can accumulate a summary. Mirrors the WARN-shape of the
// per-source rejection logs so accept/reject traces diff cleanly.
func logAcceptedWaterPolygon(ctx context.Context, msg, idKey string, idVal int64, outer [][2]float64, bboxArea float64) float64 {
	area := math.Abs(ringSignedArea(outer)) / 2
	var frac float64
	if bboxArea > 0 {
		frac = area / bboxArea
	}
	logs.From(ctx).Debug(msg,
		idKey, idVal,
		"vertices", len(outer),
		"area_lonlat_sq", area,
		"bbox_frac", frac,
	)
	return frac
}

// maxOuterBboxAreaFraction caps the planar lon/lat area of any single
// water outer ring at this fraction of the query bbox area. A larger
// outer almost always indicates a stitching error — e.g. inverted
// outer/inner roles producing a continent-sized polygon. This guard
// applies only to closed `natural=water` ways and relation outer rings
// (classifyWaterElement / polygonsFromRelation), where no other water/
// land discriminator has run.
//
// It is intentionally NOT applied to coastline-closure rings: those are
// already validated by land probes + CW orientation in closeCoastlineChain
// (see closeAndAcceptCoastline). The cap would misfire there for cities
// whose admin boundary extends into open water — Foster City's bbox is
// ~85% San Francisco Bay, so its legitimate bay ring (85.6% of bbox) was
// wrongly rejected by this cap until bd-86aa. The lesson: a city's query
// bbox is NOT always land-scale; a boundary reaching into a bay or ocean
// produces a legitimate near-bbox-sized water ring. Tune alongside
// waterStripMinAreaRatio (pkg/cmd/ingest/ingest.go) since both gate the
// same failure class for the way/relation paths.
const maxOuterBboxAreaFraction = 0.8

// acceptWaterPolygon decides whether outer is a plausible water-polygon
// outer ring inside the query bbox. Returns (true, "") on accept,
// (false, reason) on reject so the caller can log with whatever
// context it has (OSM way id, relation id, source). Pure: no logging
// or I/O — Single Responsibility, testable in isolation.
//
// Orientation is intentionally NOT checked here. For `natural=water`
// ways and relation outer rings, OSM does not strictly enforce CW vs
// CCW — orientation is a convention. Callers normalize to CW for
// downstream consistency. The CW=water/CCW=island distinction is
// semantic only for *coastline*-derived rings and lives in
// closeCoastlineChain (solvent-streets-b0v9), which drops CCW closed
// rings outright rather than reversing them.
//
// Rejection rules compose: adding a new check appends another clause
// without disturbing the existing ones.
func acceptWaterPolygon(outer [][2]float64, bboxArea float64) (bool, string) {
	if !isClosedRing(outer) {
		return false, "ring not closed"
	}
	if bboxArea > 0 {
		area := math.Abs(ringSignedArea(outer)) / 2
		if frac := area / bboxArea; frac > maxOuterBboxAreaFraction {
			return false, fmt.Sprintf("outer covers %.1f%% of bbox (max %.0f%%)",
				100*frac, 100*maxOuterBboxAreaFraction)
		}
	}
	return true, ""
}

// bboxLonLatArea returns the planar lon/lat area of bbox. Used as the
// denominator for the per-polygon area fraction check — both numerator
// (ringSignedArea/2) and denominator are in the same lon/lat units, so
// the ratio is meaningful even though neither value is in m².
func bboxLonLatArea(bbox [4]float64) float64 {
	south, west, north, east := bbox[0], bbox[1], bbox[2], bbox[3]
	return (north - south) * (east - west)
}

// relationToPolygons walks one OSM relation's member ways, stitches
// outer/inner rings, assigns each inner to its containing outer, and
// returns the raw polygons. The optional acceptOuter filter rejects
// outers before inner-ring assignment, so an inner ring contained by a
// rejected outer falls through to the next surviving outer instead of
// being silently dropped. Water passes acceptWaterPolygon; admin-boundary
// fetches pass nil (accept any closed ring). Dropped unclosed member
// ways are logged with the relation id so operators can fix OSM data.
//
// The returned slice uses waterPolygon as a shared shape; the name is
// a historical artifact of where the type was first defined. The
// fields (outer + holes) are equally accurate for any OSM polygon.
func relationToPolygons(ctx context.Context, e overpassElement, stitch func([]stitchInput) ([][][2]float64, []int64), acceptOuter func([][2]float64) bool) []waterPolygon {
	var outerWays, innerWays []stitchInput
	for _, m := range e.Members {
		if m.Type != elementWay || len(m.Geometry) < 2 {
			continue
		}
		coords := make([][2]float64, len(m.Geometry))
		for i, g := range m.Geometry {
			coords[i] = [2]float64{g.Lon, g.Lat}
		}
		in := stitchInput{id: m.Ref, coords: coords}
		switch m.Role {
		case "outer", "":
			// Per the OSM multipolygon spec the role should always be
			// "outer" or "inner", but very old relations sometimes have
			// blank roles — treat those as outer (the spec's default).
			outerWays = append(outerWays, in)
		case "inner":
			innerWays = append(innerWays, in)
		}
	}

	outerRings, droppedOuter := stitch(outerWays)
	innerRings, droppedInner := stitch(innerWays)
	if len(droppedOuter) > 0 || len(droppedInner) > 0 {
		logs.From(ctx).Warn("relation: dropped unclosed member ways",
			"relation", e.ID,
			"dropped_outer", droppedOuter,
			"dropped_inner", droppedInner,
		)
	}

	// Filter outers BEFORE inner-ring assignment so an inner contained
	// by a rejected outer can still fall through to the next surviving
	// outer (deep harbors, multi-island archipelagos).
	polys := make([]waterPolygon, 0, len(outerRings))
	for _, o := range outerRings {
		if acceptOuter != nil && !acceptOuter(o) {
			continue
		}
		polys = append(polys, waterPolygon{outer: o})
	}
	for _, h := range innerRings {
		// Assign each inner ring to the first outer ring that contains
		// its first vertex. For the consumers' use (subtracting/clipping),
		// any containing outer gives the right union, so first-match is
		// sufficient.
		for i, p := range polys {
			if pointInRing(h[0], p.outer) {
				polys[i].holes = append(polys[i].holes, h)
				break
			}
		}
	}
	return polys
}

func polygonsFromRelation(ctx context.Context, e overpassElement, bboxArea float64) []waterPolygon {
	// Water/coastline stitching uses the exact, tail-only stitcher: its
	// thresholds are calibrated against real coastlines and a tolerant
	// snap could merge a near-miss that should stay open.
	return relationToPolygons(ctx, e, stitchRings, func(outer [][2]float64) bool {
		ok, reason := acceptWaterPolygon(outer, bboxArea)
		if !ok {
			logs.From(ctx).Warn("water relation: rejected outer ring",
				"relation", e.ID, "reason", reason, "vertices", len(outer),
			)
		}
		return ok
	})
}

func polysToMultiPolygonGeoJSON(polys []waterPolygon) string {
	parts := make([]string, len(polys))
	for i, p := range polys {
		rings := make([]string, 0, 1+len(p.holes))
		rings = append(rings, coordRingJSON(p.outer))
		for _, h := range p.holes {
			rings = append(rings, coordRingJSON(h))
		}
		parts[i] = "[" + strings.Join(rings, ",") + "]"
	}
	return fmt.Sprintf(`{"type":"MultiPolygon","coordinates":[%s]}`, strings.Join(parts, ","))
}

func coordRingJSON(coords [][2]float64) string {
	parts := make([]string, len(coords))
	for i, c := range coords {
		parts[i] = fmt.Sprintf("[%g,%g]", c[0], c[1])
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// stitchCoastlineChains chains coastline ways head-to-tail preserving
// orientation. OSM coastline ways have water on the right when walked
// start→end (the OSM convention), so reversal is forbidden — a reversed
// way would flip the water side and break the right-hand rule that
// downstream bbox closing depends on. Result chains may be closed
// (first == last) or open. Each input way is consumed at most once.
func stitchCoastlineChains(ways [][][2]float64) [][][2]float64 {
	// Map each unused way's start vertex to its index for O(1) extension.
	// A vertex with multiple starts is rare in OSM coastline data but
	// possible at junction tags; we keep all candidates and pick any
	// unused one.
	startsAt := make(map[[2]float64][]int)
	for i, w := range ways {
		if len(w) >= 2 {
			startsAt[w[0]] = append(startsAt[w[0]], i)
		}
	}
	endsAt := make(map[[2]float64][]int)
	for i, w := range ways {
		if len(w) >= 2 {
			endsAt[w[len(w)-1]] = append(endsAt[w[len(w)-1]], i)
		}
	}

	used := make([]bool, len(ways))
	pickUnused := func(idxs []int) (int, bool) {
		for _, i := range idxs {
			if !used[i] {
				return i, true
			}
		}
		return 0, false
	}

	var chains [][][2]float64
	for i := range ways {
		if used[i] || len(ways[i]) < 2 {
			continue
		}
		headIdx := findChainHead(i, ways, endsAt, used)
		chain := buildChainForward(headIdx, ways, startsAt, used, pickUnused)
		chains = append(chains, chain)
	}
	return chains
}

// findChainHead walks back from seed through predecessor ways (whose
// tails match the current head's start vertex) until none remain. The
// walkback uses a local seen-set so closed loops terminate at the seed
// rather than running forever, and does not mark `used` so the forward
// pass can still consume the predecessor ways.
func findChainHead(seed int, ways [][][2]float64, endsAt map[[2]float64][]int, used []bool) int {
	headIdx := seed
	seen := map[int]bool{seed: true}
	for {
		next := -1
		for _, idx := range endsAt[ways[headIdx][0]] {
			if !used[idx] && !seen[idx] {
				next = idx
				break
			}
		}
		if next < 0 {
			return headIdx
		}
		headIdx = next
		seen[next] = true
	}
}

// buildChainForward extends the chain starting at headIdx by repeatedly
// looking up the unused way whose start vertex matches the chain's
// current tail. Stops when no successor exists or the chain closes on
// itself.
func buildChainForward(
	headIdx int,
	ways [][][2]float64,
	startsAt map[[2]float64][]int,
	used []bool,
	pickUnused func([]int) (int, bool),
) [][2]float64 {
	chain := append([][2]float64{}, ways[headIdx]...)
	used[headIdx] = true
	for {
		tail := chain[len(chain)-1]
		next, ok := pickUnused(startsAt[tail])
		if !ok {
			return chain
		}
		chain = append(chain, ways[next][1:]...)
		used[next] = true
		if chain[0] == chain[len(chain)-1] {
			return chain
		}
	}
}

// closeCoastlineChain clips chain to bbox and closes any open sub-chain
// along the bbox boundary using the city's interior as a land probe.
// Returns one or more closed rings. Sub-chains whose endpoints do not
// land on the bbox boundary after clipping are dropped — they cannot
// enclose a water region without one. Already-closed sub-chains pass
// through only when their orientation places water inside the ring
// (see ringIsCW); CCW closed rings are dropped because they represent
// islands (land inside, water outside) rather than water polygons.
//
// landProbes are lon/lat points known to be inside the city's land
// (one per sub-polygon of the Nominatim boundary; see
// geo.InteriorPoints). Open sub-chains produce two candidate rings
// (CW and CCW bbox-edge closures) that together partition the bbox;
// the water-side ring is the one that contains NONE of the probes.
// This is a global correctness invariant: a water polygon used to
// subtract from a city boundary cannot contain any point of the
// city's land. Replaces the old local right-hand-water probe (closed
// bead solvent-streets-vtcs), which guessed wrong for OSM coastline
// ways in NYC that were digitized inconsistently with the convention
// (root cause of solvent-streets-yrr0).
//
// When a chain's closure produces two candidate rings that both
// contain land probes (NYC chain=30 cuts through multiple boroughs),
// the chain doesn't separate water from land at all and is dropped.
func closeCoastlineChain(ctx context.Context, chain [][2]float64, bbox [4]float64, probes *landProbeIndex) [][][2]float64 {
	var rings [][][2]float64
	for _, sub := range clipChainToBBox(chain, bbox) {
		if len(sub) < 2 {
			continue
		}
		if isClosedRing(sub) {
			if !ringIsCW(sub) {
				logs.From(ctx).Warn("water coastline: dropped CCW closed ring (island, not water polygon)",
					"vertices", len(sub),
				)
				continue
			}
			// A closed coastline ring should not enclose any of the
			// city's land probes — if it does, it's traced around land
			// in the wrong direction (data error) and would over-subtract.
			if hits := probes.RingHits(sub); hits > 0 {
				logs.From(ctx).Warn("water coastline: dropped CW closed ring containing city land",
					"vertices", len(sub),
					"land_hits", hits, "land_probes", probes.Len(),
				)
				continue
			}
			rings = append(rings, sub)
			continue
		}
		ring, ok := closeOpenSubChain(ctx, sub, bbox, probes)
		if !ok {
			continue
		}
		rings = append(rings, ring)
	}
	return rings
}

// closeOpenSubChain closes one open clipped sub-chain into a water-side
// ring by picking the candidate (CW or CCW bbox-edge closure) that
// contains NONE of the city's land probes. A candidate containing
// even one probe overlaps the city's land and is wrong.
//
// Returns (nil, false) when:
//   - the chain endpoints aren't on the bbox boundary
//   - neither candidate ring is well-formed
//   - both candidate rings contain at least one land probe (the chain
//     doesn't cleanly separate water from land — e.g. NYC chain=30 cuts
//     through multiple boroughs so both sides include land)
//
// When both candidates are land-free (landProbes far from this chain —
// common for chains in bbox corners well away from the city), the
// smaller-area ring is chosen. The smaller ring is geometrically more
// likely to represent the water immediately adjacent to the coastline;
// the larger candidate would over-claim. Logged at Debug so operators
// can see the tiebreak when it happens.
//
// Replaces the prior unanimous-probe and single-midpoint-probe rules,
// both of which inherited a local right-hand-water assumption that
// some OSM coastline ways violate (root cause of solvent-streets-yrr0).
func closeOpenSubChain(ctx context.Context, sub [][2]float64, bbox [4]float64, probes *landProbeIndex) ([][2]float64, bool) {
	head := sub[0]
	tail := sub[len(sub)-1]
	if !onBBoxEdge(head, bbox) || !onBBoxEdge(tail, bbox) {
		return nil, false
	}
	ringCW, okCW := assembleClosedRing(sub, bboxWalkCW(tail, head, bbox))
	ringCCW, okCCW := assembleClosedRing(sub, bboxWalkCCW(tail, head, bbox))
	if !okCW && !okCCW {
		return nil, false
	}

	var cwHits, ccwHits int
	if okCW {
		cwHits = probes.RingHits(ringCW)
	}
	if okCCW {
		ccwHits = probes.RingHits(ringCCW)
	}

	switch {
	case okCW && cwHits == 0 && (!okCCW || ccwHits > 0):
		return ringCW, true
	case okCCW && ccwHits == 0 && (!okCW || cwHits > 0):
		return ringCCW, true
	case okCW && okCCW && cwHits == 0 && ccwHits == 0:
		// Both candidates are land-free — usually a chain in a bbox corner
		// far from the city. Pick the smaller ring; the larger would
		// over-claim water area beyond the immediate coastline.
		cwArea := math.Abs(ringSignedArea(ringCW))
		ccwArea := math.Abs(ringSignedArea(ringCCW))
		if cwArea <= ccwArea {
			logs.From(ctx).Debug("water coastline: both candidates land-free, picked smaller CW",
				"vertices", len(sub), "cw_area", cwArea, "ccw_area", ccwArea)
			return ringCW, true
		}
		logs.From(ctx).Debug("water coastline: both candidates land-free, picked smaller CCW",
			"vertices", len(sub), "cw_area", cwArea, "ccw_area", ccwArea)
		return ringCCW, true
	default:
		// Both candidates contain at least one land probe (chain weaves
		// through land — NYC chain=30 case). Drop rather than over-
		// subtract.
		logs.From(ctx).Warn("water coastline: dropped chain — both candidates overlap city land",
			"vertices", len(sub),
			"cw_valid", okCW, "ccw_valid", okCCW,
			"cw_land_hits", cwHits, "ccw_land_hits", ccwHits,
			"total_probes", probes.Len(),
		)
		return nil, false
	}
}

// landProbeIndex caches land probes with their overall bounding box
// so each per-polygon hit-count call can early-reject polygons whose
// bbox doesn't overlap any probe, and bbox-reject individual probes
// before paying for pointInRing on every ring vertex. Coastal cities
// can produce hundreds of water polygons against a handful of probes;
// without this, countLandProbeHits is O(polygons × probes × vertices)
// in the coastline path. Solvent-streets-m3kj.
type landProbeIndex struct {
	probes [][2]float64
	// Overall bbox of every probe (lon/lat). Inclusive; degenerate when
	// len(probes)==1.
	minX, minY, maxX, maxY float64
}

func newLandProbeIndex(probes [][2]float64) *landProbeIndex {
	idx := &landProbeIndex{probes: probes}
	if len(probes) == 0 {
		return idx
	}
	idx.minX, idx.maxX = probes[0][0], probes[0][0]
	idx.minY, idx.maxY = probes[0][1], probes[0][1]
	for _, p := range probes[1:] {
		switch {
		case p[0] < idx.minX:
			idx.minX = p[0]
		case p[0] > idx.maxX:
			idx.maxX = p[0]
		}
		switch {
		case p[1] < idx.minY:
			idx.minY = p[1]
		case p[1] > idx.maxY:
			idx.maxY = p[1]
		}
	}
	return idx
}

func (l *landProbeIndex) Len() int { return len(l.probes) }

// RingHits returns the number of probes inside ring. Used by the
// coastline closure rule to detect candidate rings that overlap city
// land (those rings are land-side and would over-subtract).
func (l *landProbeIndex) RingHits(ring [][2]float64) int {
	if len(ring) == 0 || len(l.probes) == 0 {
		return 0
	}
	rMinX, rMinY, rMaxX, rMaxY := ringBBox(ring)
	if rMaxX < l.minX || rMinX > l.maxX || rMaxY < l.minY || rMinY > l.maxY {
		return 0
	}
	hits := 0
	for _, p := range l.probes {
		if p[0] < rMinX || p[0] > rMaxX || p[1] < rMinY || p[1] > rMaxY {
			continue
		}
		if pointInRing(p, ring) {
			hits++
		}
	}
	return hits
}

// PolygonHits returns the number of probes that lie set-theoretic-
// inside the polygon (inside the outer ring AND outside every hole).
// A probe that lands in a hole is geometrically NOT in the water
// polygon — the hole represents a non-water region (typically a real
// island within a lake), so the polygon doesn't claim that area.
func (l *landProbeIndex) PolygonHits(p waterPolygon) int {
	if len(p.outer) == 0 || len(l.probes) == 0 {
		return 0
	}
	oMinX, oMinY, oMaxX, oMaxY := ringBBox(p.outer)
	if oMaxX < l.minX || oMinX > l.maxX || oMaxY < l.minY || oMinY > l.maxY {
		return 0
	}
	hits := 0
probeLoop:
	for _, probe := range l.probes {
		if probe[0] < oMinX || probe[0] > oMaxX || probe[1] < oMinY || probe[1] > oMaxY {
			continue
		}
		if !pointInRing(probe, p.outer) {
			continue
		}
		for _, hole := range p.holes {
			if pointInRing(probe, hole) {
				continue probeLoop
			}
		}
		hits++
	}
	return hits
}

// ringBBox returns the axis-aligned envelope of ring. Single-pass O(V).
func ringBBox(ring [][2]float64) (minX, minY, maxX, maxY float64) {
	minX, maxX = ring[0][0], ring[0][0]
	minY, maxY = ring[0][1], ring[0][1]
	for _, p := range ring[1:] {
		switch {
		case p[0] < minX:
			minX = p[0]
		case p[0] > maxX:
			maxX = p[0]
		}
		switch {
		case p[1] < minY:
			minY = p[1]
		case p[1] > maxY:
			maxY = p[1]
		}
	}
	return minX, minY, maxX, maxY
}

// assembleClosedRing joins sub with the corner sequence and a closing
// vertex, returning the result iff isClosedRing accepts it. Empty
// corners with same-edge endpoints produce a 3-vertex degenerate ring
// that fails the check and is rejected here.
func assembleClosedRing(sub, corners [][2]float64) ([][2]float64, bool) {
	ring := make([][2]float64, 0, len(sub)+len(corners)+1)
	ring = append(ring, sub...)
	ring = append(ring, corners...)
	ring = append(ring, sub[0])
	if !isClosedRing(ring) {
		return nil, false
	}
	return ring, true
}

// clipChainToBBox clips a polyline to bbox, returning one or more
// sub-chains that lie inside (or on the boundary of) bbox. Segments
// are clipped per-edge via Liang-Barsky so the both-endpoints-outside
// case where the segment still crosses the bbox is handled.
func clipChainToBBox(chain [][2]float64, bbox [4]float64) [][][2]float64 {
	if len(chain) < 2 {
		return nil
	}
	var result [][][2]float64
	var current [][2]float64

	flush := func() {
		if len(current) >= 2 {
			result = append(result, current)
		}
		current = nil
	}

	for i := 1; i < len(chain); i++ {
		p, q, ok := liangBarsky(chain[i-1], chain[i], bbox)
		if !ok {
			flush()
			continue
		}
		// Start the sub-chain if we just entered the bbox or are at the
		// first segment.
		if len(current) == 0 {
			current = append(current, p)
		} else if p != current[len(current)-1] {
			// Clip cut off something at the previous segment's tail —
			// emit the existing run and begin a new one.
			flush()
			current = append(current, p)
		}
		current = append(current, q)
		// If the clipped segment ended at a bbox edge that the original
		// `chain[i]` does not equal, then chain[i] is outside bbox: the
		// next segment will reopen.
		if q != chain[i] {
			flush()
		}
	}
	flush()
	return result
}

// liangBarsky clips segment a→b to bbox, returning the in-bbox portion
// (p, q) with ok=true. If the segment lies entirely outside bbox,
// returns ok=false.
func liangBarsky(a, b [2]float64, bbox [4]float64) (p, q [2]float64, ok bool) {
	south, west, north, east := bbox[0], bbox[1], bbox[2], bbox[3]
	dx := b[0] - a[0]
	dy := b[1] - a[1]
	t0, t1 := 0.0, 1.0
	for _, c := range [4]struct {
		num, denom float64
	}{
		{west - a[0], dx},   // left edge: x ≥ west
		{a[0] - east, -dx},  // right edge: x ≤ east
		{south - a[1], dy},  // bottom: y ≥ south
		{a[1] - north, -dy}, // top: y ≤ north
	} {
		if !clipEdge(c.num, c.denom, &t0, &t1) {
			return p, q, false
		}
	}
	// Preserve the original endpoints when the parametric bounds are
	// untouched — otherwise float multiplication drifts and the
	// clipped endpoint stops exactly equalling the input point, which
	// breaks downstream identity checks (chain continuation, ring
	// closure).
	if t0 == 0 {
		p = a
	} else {
		p = [2]float64{a[0] + t0*dx, a[1] + t0*dy}
	}
	if t1 == 1 {
		q = b
	} else {
		q = [2]float64{a[0] + t1*dx, a[1] + t1*dy}
	}
	return p, q, true
}

// clipEdge updates the [t0, t1] parametric window against one bbox
// half-plane constraint expressed as denom*t ≥ num. Returns false when
// the constraint shrinks the window to empty (segment outside).
func clipEdge(num, denom float64, t0, t1 *float64) bool {
	if denom == 0 {
		return num <= 0
	}
	t := num / denom
	if denom > 0 {
		if t > *t1 {
			return false
		}
		if t > *t0 {
			*t0 = t
		}
		return true
	}
	if t < *t0 {
		return false
	}
	if t < *t1 {
		*t1 = t
	}
	return true
}

// onBBoxEdge reports whether p lies on (within float epsilon of) any
// bbox edge.
func onBBoxEdge(p [2]float64, bbox [4]float64) bool {
	return bboxPerimeterPos(p, bbox) >= 0
}

// bboxPerimeterPos returns the arc-length position of p along the bbox
// boundary, parametrized CW from the NE corner (pos 0). Returns -1 if
// p is not on the boundary. The CW cycle is:
//
//	NE (pos 0) → SE (height) → SW (height+width) → NW (2h+w) → NE (perim).
func bboxPerimeterPos(p [2]float64, bbox [4]float64) float64 {
	south, west, north, east := bbox[0], bbox[1], bbox[2], bbox[3]
	height := north - south
	width := east - west
	const eps = 1e-9
	switch {
	case math.Abs(p[0]-east) < eps && p[1] >= south-eps && p[1] <= north+eps:
		return north - p[1]
	case math.Abs(p[1]-south) < eps && p[0] >= west-eps && p[0] <= east+eps:
		return height + (east - p[0])
	case math.Abs(p[0]-west) < eps && p[1] >= south-eps && p[1] <= north+eps:
		return height + width + (p[1] - south)
	case math.Abs(p[1]-north) < eps && p[0] >= west-eps && p[0] <= east+eps:
		return 2*height + width + (p[0] - west)
	}
	return -1
}

// bboxWalkCW returns the bbox corner points encountered when walking
// CW from `from` to `to` along the bbox boundary. Both points must lie
// on the boundary; if either does not, nil is returned. Corners are
// listed in CW order of encounter. When from and to share the same
// edge and the CW-shortest path goes directly to `to`, no corners are
// emitted.
func bboxWalkCW(from, to [2]float64, bbox [4]float64) [][2]float64 {
	return bboxWalk(from, to, bbox, true)
}

// bboxWalkCCW is the CCW mirror of bboxWalkCW. Returned corners are in
// CCW order of encounter.
func bboxWalkCCW(from, to [2]float64, bbox [4]float64) [][2]float64 {
	return bboxWalk(from, to, bbox, false)
}

// bboxWalk is the shared CW/CCW corner walker. Direction is a parameter
// so the two public wrappers don't fork parallel implementations.
func bboxWalk(from, to [2]float64, bbox [4]float64, cw bool) [][2]float64 {
	south, west, north, east := bbox[0], bbox[1], bbox[2], bbox[3]
	height := north - south
	width := east - west
	perim := 2 * (height + width)

	posFrom := bboxPerimeterPos(from, bbox)
	posTo := bboxPerimeterPos(to, bbox)
	if posFrom < 0 || posTo < 0 {
		return nil
	}

	corners := [4]struct {
		pos float64
		pt  [2]float64
	}{
		{0, [2]float64{east, north}},                // NE
		{height, [2]float64{east, south}},           // SE
		{height + width, [2]float64{west, south}},   // SW
		{2*height + width, [2]float64{west, north}}, // NW
	}

	arc := cwArc
	if !cw {
		arc = ccwArc
	}
	target := arc(posFrom, posTo, perim)
	type withDist struct {
		d  float64
		pt [2]float64
	}
	visited := make([]withDist, 0, 4)
	for _, c := range corners {
		d := arc(posFrom, c.pos, perim)
		// Strict inequality skips the case where from or to is a corner
		// (no spurious self-visit).
		if d > 0 && d < target {
			visited = append(visited, withDist{d, c.pt})
		}
	}
	if len(visited) == 0 {
		return nil
	}
	sort.Slice(visited, func(i, j int) bool { return visited[i].d < visited[j].d })
	out := make([][2]float64, len(visited))
	for i, v := range visited {
		out[i] = v.pt
	}
	return out
}

// cwArc returns the CW arc-length from a to b on a cycle of length
// perim. Result is in [0, perim).
func cwArc(a, b, perim float64) float64 {
	d := b - a
	for d < 0 {
		d += perim
	}
	for d >= perim {
		d -= perim
	}
	return d
}

// ccwArc returns the CCW arc-length from a to b on a cycle of length
// perim. Equivalent to cwArc with a and b swapped: walking CCW from a
// to b covers the same distance as walking CW from b to a.
func ccwArc(a, b, perim float64) float64 {
	return cwArc(b, a, perim)
}
