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
// returns a single GeoJSON MultiPolygon string. Three source shapes are
// supported: closed `natural=water` ways, `natural=water` multipolygon
// relations (whose outer/inner member ways are stitched into rings
// here), and `natural=coastline` linestrings (which only enclose the
// sea once their endpoints are closed along the query bbox). Empty
// water responses return ("", nil) — callers must treat this as a
// benign no-op (no water in the bbox).
func FetchOSMWater(ctx context.Context, client *http.Client, bbox [4]float64) (string, error) {
	return fetchOSMWater(ctx, client, overpassAPI, bbox)
}

func fetchOSMWater(ctx context.Context, client *http.Client, baseURL string, bbox [4]float64) (string, error) {
	query := buildWaterQuery(bbox)

	req, err := http.NewRequestWithContext(AllowRetry(ctx), http.MethodPost, baseURL, strings.NewReader(url.Values{"data": {query}}.Encode()))
	if err != nil {
		return "", fmt.Errorf("create overpass water request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("overpass water request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read overpass water response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("overpass water returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	return parseWaterResponse(ctx, body, bbox)
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

func parseWaterResponse(ctx context.Context, data []byte, bbox [4]float64) (string, error) {
	var resp overpassResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parse overpass water json: %w", err)
	}

	bboxArea := bboxLonLatArea(bbox)
	var polys []waterPolygon
	var coastWays [][][2]float64
	for _, e := range resp.Elements {
		switch e.Type {
		case elementWay:
			coords := resolveWayCoords(e, nil)
			if e.Tags["natural"] == "coastline" {
				coastWays = append(coastWays, coords)
				continue
			}
			if !isClosedRing(coords) {
				continue
			}
			if ok, reason := acceptWaterPolygon(coords, bboxArea); !ok {
				logs.From(ctx).Warn("water way: rejected polygon",
					"way", e.ID, "reason", reason, "vertices", len(coords),
				)
				continue
			}
			polys = append(polys, waterPolygon{outer: coords})
		case elementRelation:
			polys = append(polys, polygonsFromRelation(ctx, e, bboxArea)...)
		}
	}

	for _, chain := range stitchCoastlineChains(coastWays) {
		for _, ring := range closeCoastlineChain(ctx, chain, bbox) {
			if ok, reason := acceptWaterPolygon(ring, bboxArea); !ok {
				logs.From(ctx).Warn("water coastline: rejected closed ring",
					"reason", reason, "vertices", len(ring),
				)
				continue
			}
			polys = append(polys, waterPolygon{outer: ring})
		}
	}

	if len(polys) == 0 {
		return "", nil
	}
	return polysToMultiPolygonGeoJSON(polys), nil
}

// maxOuterBboxAreaFraction caps the planar lon/lat area of any single
// water outer ring at this fraction of the query bbox area. A larger
// outer almost always indicates a stitching error (e.g. inverted
// outer/inner roles producing a continent-sized polygon, or a coastline
// closure that captured the land side). Real water polygons inside a
// city bbox — even the Pacific off SF or Boston Harbor — sit well
// under this threshold because the query bbox is at city scale. Tune
// alongside waterStripMinAreaRatio (pkg/cmd/ingest/ingest.go) since
// both gate the same failure class.
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

func polygonsFromRelation(ctx context.Context, e overpassElement, bboxArea float64) []waterPolygon {
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

	outerRings, droppedOuter := stitchRings(outerWays)
	innerRings, droppedInner := stitchRings(innerWays)
	if len(droppedOuter) > 0 || len(droppedInner) > 0 {
		// Real OSM relations fragment outer/inner boundaries across many
		// member ways; when the chain has a gap, stitchRings cannot close
		// the ring and discards the partial chain. Surface the missing
		// member ids so a human can investigate which OSM data needs
		// fixing — silent drops mask coastline data loss.
		logs.From(ctx).Warn("water relation: dropped unclosed member ways",
			"relation", e.ID,
			"dropped_outer", droppedOuter,
			"dropped_inner", droppedInner,
		)
	}

	polys := make([]waterPolygon, 0, len(outerRings))
	for _, o := range outerRings {
		if ok, reason := acceptWaterPolygon(o, bboxArea); !ok {
			logs.From(ctx).Warn("water relation: rejected outer ring",
				"relation", e.ID, "reason", reason, "vertices", len(o),
			)
			continue
		}
		polys = append(polys, waterPolygon{outer: o})
	}
	for _, h := range innerRings {
		// Assign each inner ring to the first outer ring that contains
		// its first vertex. For our use (subtracting the union from a
		// city boundary), any containing outer gives the right union, so
		// the first-match is sufficient.
		for i, p := range polys {
			if pointInRing(h[0], p.outer) {
				polys[i].holes = append(polys[i].holes, h)
				break
			}
		}
	}
	return polys
}

// stitchInput pairs an OSM way's ref id with its coordinate sequence.
// stitchRings consumes these so it can report the ids of member ways
// whose partial chains had to be dropped.
type stitchInput struct {
	id     int64
	coords [][2]float64
}

// stitchRings chains open ways into closed rings by matching endpoints.
// Ways that cannot be closed are dropped and their ids returned in
// dropped. Each input way is consumed at most once. Time complexity is
// O(n²) in the number of ways, which is fine for the dozens-of-segments-
// per-relation scale of OSM water.
func stitchRings(ways []stitchInput) (rings [][][2]float64, dropped []int64) {
	used := make([]bool, len(ways))

	for i := range ways {
		if used[i] || len(ways[i].coords) < 2 {
			continue
		}
		used[i] = true
		ring := append([][2]float64{}, ways[i].coords...)
		consumed := []int{i}

		for ring[0] != ring[len(ring)-1] {
			extended, next, nextIdx := extendRing(ring, ways, used)
			if !extended {
				break
			}
			ring = next
			consumed = append(consumed, nextIdx)
		}

		if isClosedRing(ring) {
			rings = append(rings, ring)
		} else {
			for _, c := range consumed {
				dropped = append(dropped, ways[c].id)
			}
		}
	}
	return rings, dropped
}

// extendRing finds an unused way whose endpoint matches ring's tail and
// appends it to ring (reversing the way if needed). The matched way is
// marked used. Returns (false, ring, -1) when no way matches.
func extendRing(ring [][2]float64, ways []stitchInput, used []bool) (bool, [][2]float64, int) {
	tail := ring[len(ring)-1]
	for j := range ways {
		if used[j] || len(ways[j].coords) < 2 {
			continue
		}
		w := ways[j].coords
		switch {
		case tail == w[0]:
			ring = append(ring, w[1:]...)
		case tail == w[len(w)-1]:
			for k := len(w) - 2; k >= 0; k-- {
				ring = append(ring, w[k])
			}
		default:
			continue
		}
		used[j] = true
		return true, ring, j
	}
	return false, ring, -1
}

func isClosedRing(coords [][2]float64) bool {
	return len(coords) >= 4 && coords[0] == coords[len(coords)-1]
}

// pointInRing returns true if p is strictly inside ring using ray casting.
// The ring is in lon/lat order; that's fine because point-in-polygon is
// topological — it does not require an equal-area projection.
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

// probeEpsDeg is the offset (in lon/lat degrees) used to place a probe
// point ε to the right of a coastline segment's midpoint. At equator
// scale 1e-7° ≈ 1.1 cm — small enough to stay inside any real water
// region adjacent to the chain, large enough to escape float noise on
// the chain itself.
const probeEpsDeg = 1e-7

// closeCoastlineChain clips chain to bbox and closes any open sub-chain
// along the bbox boundary using the OSM right-hand-water rule.
// Returns one or more closed rings. Sub-chains whose endpoints do not
// land on the bbox boundary after clipping are dropped — they cannot
// enclose a water region without one. Already-closed sub-chains pass
// through only when their orientation places water inside the ring
// (see ringIsCW); CCW closed rings are dropped because they represent
// islands (land inside, water outside) rather than water polygons.
//
// Open sub-chains used to close blindly CW around the bbox, which
// captured the *land* side when the chain entered/exited the bbox in
// an order the CW walk didn't anticipate (closed beads
// solvent-streets-vtcs). The closing direction is now derived from the
// right-hand-water rule: build both CW and CCW candidate rings, then
// pick the one whose interior contains a probe point placed ε to the
// right of the chain's midpoint. The probe IS the rule made local.
func closeCoastlineChain(ctx context.Context, chain [][2]float64, bbox [4]float64) [][][2]float64 {
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
			rings = append(rings, sub)
			continue
		}
		ring, ok := closeOpenSubChain(sub, bbox)
		if !ok {
			continue
		}
		rings = append(rings, ring)
	}
	return rings
}

// closeOpenSubChain closes one open clipped sub-chain into a water-side
// ring by picking between CW and CCW bbox-edge walks based on which
// candidate's interior contains a right-of-chain probe point. Returns
// (nil, false) when the chain endpoints aren't on the bbox boundary,
// when neither candidate ring is well-formed, or when the probe lies
// outside both (water is outside the bbox — nothing to enclose).
func closeOpenSubChain(sub [][2]float64, bbox [4]float64) ([][2]float64, bool) {
	head := sub[0]
	tail := sub[len(sub)-1]
	if !onBBoxEdge(head, bbox) || !onBBoxEdge(tail, bbox) {
		return nil, false
	}
	probe, ok := rightSideProbe(sub, probeEpsDeg)
	if !ok {
		return nil, false
	}
	ringCW, okCW := assembleClosedRing(sub, bboxWalkCW(tail, head, bbox))
	ringCCW, okCCW := assembleClosedRing(sub, bboxWalkCCW(tail, head, bbox))
	cwHit := okCW && pointInRing(probe, ringCW)
	ccwHit := okCCW && pointInRing(probe, ringCCW)
	switch {
	case cwHit && !ccwHit:
		return ringCW, true
	case ccwHit && !cwHit:
		return ringCCW, true
	case cwHit && ccwHit:
		// Both contain the probe — degenerate chain (e.g. infinitesimal
		// span). Pick the smaller ring; the larger would over-claim water.
		if math.Abs(ringSignedArea(ringCW)) <= math.Abs(ringSignedArea(ringCCW)) {
			return ringCW, true
		}
		return ringCCW, true
	default:
		// Probe in neither: water is outside the bbox for this chain.
		return nil, false
	}
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

// rightSideProbe returns a point eps to the right of the chain's
// midpoint, where "right" is the unit normal to the forward direction
// at the segment containing the midpoint. The OSM coastline convention
// places water on the right of the walking direction, so this probe
// lands in water iff the chain is correctly oriented. Returns (_, false)
// when the chain is too short to have a well-defined midpoint segment.
func rightSideProbe(chain [][2]float64, eps float64) ([2]float64, bool) {
	if len(chain) < 2 {
		return [2]float64{}, false
	}
	// Total polyline length, then find the segment containing the
	// half-length mark. Walking arc-length (rather than indexing the
	// middle segment) means the probe is robust to uneven segment sizes.
	var total float64
	segLens := make([]float64, len(chain)-1)
	for i := range segLens {
		segLens[i] = segLen(chain[i], chain[i+1])
		total += segLens[i]
	}
	if total == 0 {
		return [2]float64{}, false
	}
	half := total / 2
	var acc float64
	i := 0
	for j := range segLens {
		i = j
		if acc+segLens[j] >= half {
			break
		}
		acc += segLens[j]
	}
	a, b := chain[i], chain[i+1]
	t := (half - acc) / segLens[i]
	mid := [2]float64{a[0] + t*(b[0]-a[0]), a[1] + t*(b[1]-a[1])}
	// Forward direction (b - a), normalized; right normal is (dy, -dx).
	dx := (b[0] - a[0]) / segLens[i]
	dy := (b[1] - a[1]) / segLens[i]
	return [2]float64{mid[0] + eps*dy, mid[1] - eps*dx}, true
}

func segLen(a, b [2]float64) float64 {
	dx := b[0] - a[0]
	dy := b[1] - a[1]
	return math.Sqrt(dx*dx + dy*dy)
}

// ringSignedArea returns twice the signed area of ring in lon/lat
// coordinates (x=lon east-positive, y=lat north-positive). Only the
// sign matters for orientation, so the divide-by-two is skipped.
// Positive = CCW, negative = CW. A degenerate ring (fewer than 4
// vertices) returns 0.
func ringSignedArea(ring [][2]float64) float64 {
	if len(ring) < 4 {
		return 0
	}
	var sum float64
	for i := range len(ring) - 1 {
		sum += ring[i][0]*ring[i+1][1] - ring[i+1][0]*ring[i][1]
	}
	return sum
}

// ringIsCW reports whether ring is clockwise in lon/lat with y=lat
// north-positive. OSM coastline rule places water on the right of
// forward walk, so a closed coastline ring is CW when water lies
// inside (a lake — the case our pipeline treats as a water polygon)
// and CCW when water lies outside (an island, with land inside).
func ringIsCW(ring [][2]float64) bool {
	return ringSignedArea(ring) < 0
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
