package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/httpio"
	"github.com/jcrussell/solvent-streets/internal/logs"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/textutil"
)

// DefaultOverpassURL is the Overpass interpreter queries go to when the
// config sets no [overpass] url and PVMT_OVERPASS_URL is unset. Requests to it
// are byte-for-byte what they were before the endpoint was configurable, so
// their HTTP cache keys (method, URL and body) still match stored entries.
const DefaultOverpassURL = "https://overpass-api.de/api/interpreter" //nolint:gosec // G101: not a credential

// DefaultOverpassHost is DefaultOverpassURL's host.
const DefaultOverpassHost = "overpass-api.de"

// overpassURLOr returns baseURL, or DefaultOverpassURL when it is empty: an
// unset endpoint everywhere in this package means the default instance.
func overpassURLOr(baseURL string) string {
	if baseURL == "" {
		return DefaultOverpassURL
	}
	return baseURL
}

// OverpassHeaderTimeout is how long the client waits for an Overpass
// response's headers. Overpass sends them only once the query has run, so a
// deadline below the queries' server-side [timeout:N]
// (resource.OverpassTimeoutS) would cut slow-but-progressing queries off,
// and the retry transport would then re-run them up to six times: one
// stalled request would cost minutes before failing.
// The margin covers transfer start; past it a dead server still fails.
const OverpassHeaderTimeout = time.Duration(resource.OverpassTimeoutS+30) * time.Second

const (
	geomLineString   = "LineString"
	geomPolygon      = "Polygon"
	geomMultiPolygon = "MultiPolygon"
	geomPoint        = "Point"
)

const (
	elementNode     = "node"
	elementWay      = "way"
	elementRelation = "relation"
)

type OverpassSource struct {
	BBox [4]float64 // [south, west, north, east]
	// Exclude, when set, leaves out what a resource implementing
	// resource.ExcludingQuerier would return over it; other resources
	// ignore it.
	Exclude *[4]float64
	// URL is the Overpass interpreter to query ([overpass] url or
	// PVMT_OVERPASS_URL); empty uses DefaultOverpassURL.
	URL string
}

var _ Source = (*OverpassSource)(nil)

func (s *OverpassSource) Name() string { return resource.SourceOverpass }

const maxSplitDepth = 3 // max 4^3 = 64 requests per city/resource

func (s *OverpassSource) Fetch(ctx context.Context, client *http.Client, rt resource.Source) ([]db.Feature, error) {
	query := rt.OverpassQuery
	if ex, ok := rt.(resource.ExcludingQuerier); ok && s.Exclude != nil {
		exclude := *s.Exclude
		query = func(bbox [4]float64) string { return ex.OverpassQueryExcluding(bbox, exclude) }
	}
	return fetchSplitting(s.BBox, splitLogger(ctx),
		func(bbox [4]float64) ([]db.Feature, error) {
			return fetchBBox(ctx, client, overpassURLOr(s.URL), rt.Type(), query(bbox))
		},
		func(f db.Feature) string { return f.ID })
}

// splitLogger logs each fetchSplitting split at Info on ctx's logger,
// naming the reason (a response too large for one request, a truncation
// remark, cut-off JSON), so a run's log shows when a bbox was split.
func splitLogger(ctx context.Context) func(depth int, reason error) {
	return func(depth int, reason error) {
		logs.From(ctx).Info("overpass response unusable for one request; splitting into quadrants",
			"depth", depth, "reason", reason)
	}
}

// fetchSplitting returns fetch(bbox). When that response is unusable
// (isParseError) it splits bbox into quadrants and fetches each instead,
// recursing up to maxSplitDepth; a quadrant still unusable at that depth, or
// any other error, fails the whole fetch. An item that crosses a quadrant
// edge comes back from every quadrant it touches, so items are deduped by
// key.
// onSplit, when non-nil, is called before each split with the new depth and
// the error that caused it (splitLogger).
func fetchSplitting[T any](bbox [4]float64, onSplit func(depth int, reason error), fetch func([4]float64) ([]T, error), key func(T) string) ([]T, error) {
	var all []T
	seen := make(map[string]bool)
	var visit func(bbox [4]float64, depth int) error
	visit = func(bbox [4]float64, depth int) error {
		items, err := fetch(bbox)
		if err == nil {
			all = appendUnique(all, seen, items, key)
			return nil
		}
		if !isParseError(err) || depth >= maxSplitDepth {
			return err
		}
		if onSplit != nil {
			onSplit(depth+1, err)
		}
		for _, q := range splitBBox(bbox) {
			if err := visit(q, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(bbox, 0); err != nil {
		return nil, err
	}
	return all, nil
}

// appendUnique appends each item in src to dst, skipping any whose key has
// already been seen. seen is mutated in place, so it dedupes across calls.
func appendUnique[T any](dst []T, seen map[string]bool, src []T, key func(T) string) []T {
	for _, item := range src {
		k := key(item)
		if seen[k] {
			continue
		}
		seen[k] = true
		dst = append(dst, item)
	}
	return dst
}

func fetchBBox(ctx context.Context, client *http.Client, baseURL string, rt resource.Type, query string) ([]db.Feature, error) {
	body, err := postOverpass(ctx, client, baseURL, query)
	if err != nil {
		return nil, err
	}
	return parseOverpassResponse(ctx, body, rt)
}

// errOverpassTruncated marks a 200 response whose top-level "remark" reports a
// server-side runtime failure (timeout / out of memory). Overpass returns valid
// JSON with a partial (often empty) elements array in that case, so it parses
// cleanly; we surface it as a parse-class error so fetchSplitting splits the
// bbox and retries with smaller queries.
var errOverpassTruncated = errors.New("overpass result truncated by server remark")

// errOverpassParse is a typed sentinel wrapped around malformed-JSON parse
// failures so isParseError can match via errors.Is rather than a fragile
// substring check on the formatted message. Both this and errOverpassTruncated
// mark the "response unusable, split the bbox and retry" class.
var errOverpassParse = errors.New("parse overpass json")

// isParseError reports whether err belongs to the "response unusable, shrink
// the bbox and retry" class that fetchSplitting splits quadrants for.
//
// httpio.ErrBodyTooLarge is in that class even though it is not a parse
// failure: an oversized response is as unusable as a truncated one, and
// splitting the bbox shrinks it. Without it, a city whose roads response
// exceeds httpio.ReadAllLimit's cap would fail the whole resource with
// ErrAllSourcesFailed instead of succeeding after splitting.
//
// Both routes to the sentinel are covered: this package's own read (bare
// clients, i.e. tests) and the cache transport's read, which in production runs
// first as the outermost roundtripper and surfaces through client.Do wrapped in
// a *url.Error. errors.Is unwraps both.
func isParseError(err error) bool {
	return err != nil && (errors.Is(err, errOverpassParse) ||
		errors.Is(err, errOverpassTruncated) ||
		errors.Is(err, httpio.ErrBodyTooLarge))
}

// remarkIndicatesTruncation reports whether an Overpass top-level remark signals
// a truncated result (runtime timeout / OOM) rather than a benign note, so a
// harmless informational remark does not trigger needless quadrant retries.
func remarkIndicatesTruncation(remark string) bool {
	r := strings.ToLower(remark)
	return strings.Contains(r, "runtime error") ||
		strings.Contains(r, "timed out") ||
		strings.Contains(r, "out of memory")
}

func splitBBox(bbox [4]float64) [4][4]float64 {
	midLat := (bbox[0] + bbox[2]) / 2
	midLon := (bbox[1] + bbox[3]) / 2
	return [4][4]float64{
		{bbox[0], bbox[1], midLat, midLon}, // SW
		{bbox[0], midLon, midLat, bbox[3]}, // SE
		{midLat, bbox[1], bbox[2], midLon}, // NW
		{midLat, midLon, bbox[2], bbox[3]}, // NE
	}
}

type overpassResponse struct {
	Elements []overpassElement `json:"elements"`
	// Remark carries server-side notices; a runtime timeout/OOM after execution
	// starts arrives here on an HTTP 200 with partial elements (see A2 fix).
	Remark string `json:"remark"`
}

type overpassGeometryPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type overpassRelationMember struct {
	Type     string                  `json:"type"`
	Ref      int64                   `json:"ref"`
	Role     string                  `json:"role"`
	Geometry []overpassGeometryPoint `json:"geometry,omitempty"`
}

type overpassElement struct {
	Type     string                   `json:"type"`
	ID       int64                    `json:"id"`
	Lat      float64                  `json:"lat,omitempty"`
	Lon      float64                  `json:"lon,omitempty"`
	Tags     map[string]string        `json:"tags,omitempty"`
	Nodes    []int64                  `json:"nodes,omitempty"`
	Geometry []overpassGeometryPoint  `json:"geometry,omitempty"`
	Members  []overpassRelationMember `json:"members,omitempty"`
}

func parseOverpassResponse(ctx context.Context, data []byte, resourceType resource.Type) ([]db.Feature, error) {
	var resp overpassResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		// Wrap both the sentinel (so isParseError matches via errors.Is) and
		// the underlying error. errOverpassParse.Error() is "parse overpass
		// json", so the rendered message is unchanged from before.
		return nil, fmt.Errorf("%w: %w", errOverpassParse, err)
	}
	if remarkIndicatesTruncation(resp.Remark) {
		return nil, fmt.Errorf("overpass remark %q: %w", strings.TrimSpace(resp.Remark), errOverpassTruncated)
	}

	// Build node index for resolving way geometries
	nodes := make(map[int64][2]float64)
	for _, e := range resp.Elements {
		if e.Type == "node" {
			nodes[e.ID] = [2]float64{e.Lon, e.Lat}
		}
	}

	// Facility types (resource.FacilityType) collapse every match — tagged
	// node, closed way, multipolygon relation — to a single Point feature:
	// a fire station is a point of service, not a surface. Pavement types
	// keep their line/polygon handling below and never see nodes.
	if resourceType.IsFacility() {
		return parseFacilityElements(ctx, resp.Elements, nodes, resourceType), nil
	}

	var features []db.Feature
	for _, e := range resp.Elements {
		switch e.Type {
		case elementWay:
			if f, ok := buildFeatureFromWay(e, nodes, resourceType); ok {
				features = append(features, f)
			}
		case elementRelation:
			// Multipolygon relations (e.g. amenity=parking lots with
			// landscaping islands) carry the matching tag on the relation, not
			// the member ways, so the way clause never returns the members
			// separately — no double counting. Their geometry is inlined by
			// `out geom`, which relationToPolygons reads from e.Members.
			if f, ok := buildFeatureFromRelation(ctx, e, resourceType); ok {
				features = append(features, f)
			}
		}
	}

	return features, nil
}

// featureName is a feature's display name: its name tag, else the kind of
// thing it is (its highway tag, else its amenity tag), else empty.
func featureName(tags map[string]string) string {
	for _, key := range []string{"name", "highway", "amenity"} {
		if v := tags[key]; v != "" {
			return v
		}
	}
	return ""
}

func buildFeatureFromWay(e overpassElement, nodes map[int64][2]float64, resourceType resource.Type) (db.Feature, bool) {
	coords := resolveWayCoords(e, nodes)
	if len(coords) < 2 {
		return db.Feature{}, false
	}

	geojsonType := geomLineString
	if len(coords) >= 4 && coords[0] == coords[len(coords)-1] {
		if e.Tags["highway"] == "" || e.Tags["area"] == "yes" {
			geojsonType = geomPolygon
		}
	}

	var geojson string
	var err error
	if geojsonType == geomPolygon {
		geojson, err = coordsToPolygonGeoJSON(coords)
	} else {
		geojson, err = coordsToLineStringGeoJSON(coords)
	}
	if err != nil {
		return db.Feature{}, false
	}

	return db.Feature{
		ID:           fmt.Sprintf("osm:way:%d", e.ID),
		ResourceType: resourceType,
		Name:         featureName(e.Tags),
		Tags:         e.Tags,
		GeometryJSON: geojson,
		SourceAPI:    resource.SourceOverpass,
		FetchedAt:    time.Now(),
	}, true
}

// buildFeatureFromRelation converts a multipolygon relation into a single
// MultiPolygon feature, stitching its member ways (inlined by `out geom`) into
// outer rings with holes. Returns false when no closed ring can be formed.
// acceptOuter is nil: the relation already matched the resource's tag filter at
// the query level, so every closed outer ring belongs to the result.
func buildFeatureFromRelation(ctx context.Context, e overpassElement, resourceType resource.Type) (db.Feature, bool) {
	polys := relationToPolygons(ctx, e, stitchRings, nil)
	if len(polys) == 0 {
		return db.Feature{}, false
	}
	geojson, err := polysToMultiPolygonGeoJSON(polys)
	if err != nil {
		return db.Feature{}, false
	}

	return db.Feature{
		ID:           fmt.Sprintf("osm:relation:%d", e.ID),
		ResourceType: resourceType,
		Name:         featureName(e.Tags),
		Tags:         e.Tags,
		GeometryJSON: geojson,
		SourceAPI:    resource.SourceOverpass,
		FetchedAt:    time.Now(),
	}, true
}

// parseFacilityElements turns each tagged element into a Point feature: a
// node at its own position, a way or relation at the vertex centroid of its
// (first outer) ring. Untagged nodes are the geometry of other elements, not
// facilities, and are skipped.
func parseFacilityElements(ctx context.Context, elements []overpassElement, nodes map[int64][2]float64, resourceType resource.Type) []db.Feature {
	var features []db.Feature
	for _, e := range elements {
		c, ok := vertexCentroid(facilityCoords(ctx, e, nodes))
		if !ok {
			continue
		}
		geojson, err := coordToPointGeoJSON(c)
		if err != nil {
			continue
		}
		features = append(features, db.Feature{
			ID:           fmt.Sprintf("osm:%s:%d", e.Type, e.ID),
			ResourceType: resourceType,
			Name:         featureName(e.Tags),
			Tags:         e.Tags,
			GeometryJSON: geojson,
			SourceAPI:    resource.SourceOverpass,
			FetchedAt:    time.Now(),
		})
	}
	return features
}

// facilityCoords returns the vertices a facility element's location is taken
// from: the node itself (only when tagged — untagged nodes are other
// elements' geometry), a way's vertices, or a relation's first outer ring,
// falling back to every member vertex when no ring closes so a broken
// multipolygon still yields a usable location.
func facilityCoords(ctx context.Context, e overpassElement, nodes map[int64][2]float64) [][2]float64 {
	switch e.Type {
	case elementNode:
		if len(e.Tags) == 0 {
			return nil
		}
		return [][2]float64{{e.Lon, e.Lat}}
	case elementWay:
		return resolveWayCoords(e, nodes)
	case elementRelation:
		if polys := relationToPolygons(ctx, e, stitchRings, nil); len(polys) > 0 {
			return polys[0].outer
		}
		var coords [][2]float64
		for _, m := range e.Members {
			for _, g := range m.Geometry {
				coords = append(coords, [2]float64{g.Lon, g.Lat})
			}
		}
		return coords
	}
	return nil
}

// vertexCentroid is the mean of the vertices, dropping a closing duplicate.
// Good enough to place a building-sized footprint; not an area centroid.
func vertexCentroid(coords [][2]float64) ([2]float64, bool) {
	if len(coords) == 0 {
		return [2]float64{}, false
	}
	if len(coords) > 1 && coords[0] == coords[len(coords)-1] {
		coords = coords[:len(coords)-1]
	}
	var sx, sy float64
	for _, c := range coords {
		sx += c[0]
		sy += c[1]
	}
	n := float64(len(coords))
	return [2]float64{sx / n, sy / n}, true
}

func resolveWayCoords(e overpassElement, nodes map[int64][2]float64) [][2]float64 {
	// Try geometry field first (if out geom was used)
	if len(e.Geometry) > 0 {
		coords := make([][2]float64, len(e.Geometry))
		for i, g := range e.Geometry {
			coords[i] = [2]float64{g.Lon, g.Lat}
		}
		return coords
	}

	// Fall back to resolving node IDs
	coords := make([][2]float64, 0, len(e.Nodes))
	for _, nid := range e.Nodes {
		if c, ok := nodes[nid]; ok {
			coords = append(coords, c)
		}
	}
	return coords
}

// errSnippetMaxLen bounds how much of an upstream error-response body is folded
// into an error message — enough to be diagnostic without dumping a full page.
const errSnippetMaxLen = 200

// truncate clips s to errSnippetMaxLen runes for inclusion in an error
// message, appending an ellipsis when it clips. Rune-safe: a byte-index cut
// could split a multi-byte UTF-8 rune mid-sequence.
func truncate(s string) string {
	return textutil.TruncateRunes(s, errSnippetMaxLen, "...")
}
