package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/jcrussell/solvent-streets/internal/httpio"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

const (
	testGeomLineString = "LineString"
	testGeomPolygon    = "Polygon"
)

// TestTruncate_RuneSafe pins 6pty: the error-snippet truncate must clip on
// a rune boundary, never splitting a multi-byte UTF-8 rune (which would
// leave an invalid string in the surfaced error message).
func TestTruncate_RuneSafe(t *testing.T) {
	// Short strings pass through untouched.
	if got := truncate("short"); got != "short" {
		t.Errorf("truncate(short) = %q; want short", got)
	}
	// A run of 2-byte runes longer than errSnippetMaxLen must clip cleanly.
	long := strings.Repeat("é", errSnippetMaxLen+10) // 2 bytes each
	got := truncate(long)
	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncate should append ellipsis, got %q", got)
	}
	if n := utf8.RuneCountInString(strings.TrimSuffix(got, "...")); n != errSnippetMaxLen {
		t.Errorf("truncate kept %d runes; want %d", n, errSnippetMaxLen)
	}
}

var (
	testResourceRoads   = resource.TypeRoads
	testResourceParking = resource.TypeParking
)

func TestParseOverpassResponse_BasicWayWithGeometry(t *testing.T) {
	data := `{
		"elements": [
			{
				"type": "way",
				"id": 12345,
				"tags": {"name": "Main St", "highway": "residential"},
				"geometry": [
					{"lat": 37.68, "lon": -121.77},
					{"lat": 37.69, "lon": -121.76}
				]
			}
		]
	}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	f := features[0]
	if f.ID != "osm:way:12345" {
		t.Errorf("expected id osm:way:12345, got %s", f.ID)
	}
	if f.Name != "Main St" {
		t.Errorf("expected name Main St, got %s", f.Name)
	}
	if f.ResourceType != testResourceRoads {
		t.Errorf("expected resource type pavements, got %s", f.ResourceType)
	}
	// Should be LineString since first != last
	var geojson struct{ Type string }
	if err := json.Unmarshal([]byte(f.GeometryJSON), &geojson); err != nil {
		t.Fatal(err)
	}
	if geojson.Type != testGeomLineString {
		t.Errorf("expected LineString, got %s", geojson.Type)
	}
}

func TestParseOverpassResponse_WayResolvedViaNodeIndex(t *testing.T) {
	data := `{
		"elements": [
			{"type": "node", "id": 1, "lat": 37.68, "lon": -121.77},
			{"type": "node", "id": 2, "lat": 37.69, "lon": -121.76},
			{
				"type": "way",
				"id": 100,
				"tags": {"name": "Oak Ave"},
				"nodes": [1, 2]
			}
		]
	}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].ID != "osm:way:100" {
		t.Errorf("expected osm:way:100, got %s", features[0].ID)
	}
}

func TestParseOverpassResponse_ClosedPolygon(t *testing.T) {
	data := `{
		"elements": [
			{
				"type": "way",
				"id": 200,
				"tags": {"name": "Parking Lot"},
				"geometry": [
					{"lat": 37.68, "lon": -121.77},
					{"lat": 37.68, "lon": -121.76},
					{"lat": 37.69, "lon": -121.76},
					{"lat": 37.69, "lon": -121.77},
					{"lat": 37.68, "lon": -121.77}
				]
			}
		]
	}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceParking)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	var geojson struct{ Type string }
	if err := json.Unmarshal([]byte(features[0].GeometryJSON), &geojson); err != nil {
		t.Fatal(err)
	}
	if geojson.Type != testGeomPolygon {
		t.Errorf("expected Polygon, got %s", geojson.Type)
	}
}

func TestParseOverpassResponse_WayLessThan2Coords(t *testing.T) {
	data := `{
		"elements": [
			{
				"type": "way",
				"id": 300,
				"tags": {"name": "Short"},
				"geometry": [{"lat": 37.68, "lon": -121.77}]
			}
		]
	}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features for <2 coords, got %d", len(features))
	}
}

func TestParseOverpassResponse_EmptyResponse(t *testing.T) {
	data := `{"elements": []}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
}

func TestParseOverpassResponse_NameFallbackToHighway(t *testing.T) {
	data := `{
		"elements": [
			{
				"type": "way",
				"id": 400,
				"tags": {"highway": "residential"},
				"geometry": [
					{"lat": 37.68, "lon": -121.77},
					{"lat": 37.69, "lon": -121.76}
				]
			}
		]
	}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].Name != "residential" {
		t.Errorf("expected name fallback to 'residential', got %s", features[0].Name)
	}
}

func TestParseOverpassResponse_GeometryPriorityOverNodes(t *testing.T) {
	data := `{
		"elements": [
			{"type": "node", "id": 1, "lat": 0.0, "lon": 0.0},
			{"type": "node", "id": 2, "lat": 0.0, "lon": 0.0},
			{
				"type": "way",
				"id": 500,
				"tags": {"name": "Test"},
				"nodes": [1, 2],
				"geometry": [
					{"lat": 37.68, "lon": -121.77},
					{"lat": 37.69, "lon": -121.76}
				]
			}
		]
	}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	// Geometry field should take priority; coords should be from geometry, not nodes
	var geojson struct {
		Coordinates [][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(features[0].GeometryJSON), &geojson); err != nil {
		t.Fatal(err)
	}
	if len(geojson.Coordinates) != 2 {
		t.Fatalf("expected 2 coords, got %d", len(geojson.Coordinates))
	}
	// First coord lon should be -121.77 (from geometry), not 0.0 (from nodes)
	if geojson.Coordinates[0][0] != -121.77 {
		t.Errorf("expected lon -121.77, got %f", geojson.Coordinates[0][0])
	}
}

func TestParseOverpassResponse_RelationMultiPolygon(t *testing.T) {
	// A parking multipolygon: the amenity tag lives on the relation, geometry
	// is inlined on the member ways by `out geom`. Previously dropped silently.
	data := `{
		"elements": [
			{
				"type": "relation",
				"id": 900,
				"tags": {"amenity": "parking", "name": "Big Lot"},
				"members": [
					{"type": "way", "ref": 1, "role": "outer", "geometry": [
						{"lat": 37.68, "lon": -121.77},
						{"lat": 37.68, "lon": -121.76},
						{"lat": 37.69, "lon": -121.76},
						{"lat": 37.69, "lon": -121.77},
						{"lat": 37.68, "lon": -121.77}
					]}
				]
			}
		]
	}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceParking)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature from relation, got %d", len(features))
	}
	f := features[0]
	if f.ID != "osm:relation:900" {
		t.Errorf("expected id osm:relation:900, got %s", f.ID)
	}
	if f.Name != "Big Lot" {
		t.Errorf("expected name Big Lot, got %s", f.Name)
	}
	var geojson struct{ Type string }
	if err := json.Unmarshal([]byte(f.GeometryJSON), &geojson); err != nil {
		t.Fatal(err)
	}
	if geojson.Type != "MultiPolygon" {
		t.Errorf("expected MultiPolygon, got %s", geojson.Type)
	}
}

func TestParseOverpassResponse_RelationWithHole(t *testing.T) {
	// Outer ring with an inner ring (landscaping island) — the hole must be
	// preserved in the emitted MultiPolygon (first polygon has 2 rings).
	data := `{
		"elements": [
			{
				"type": "relation",
				"id": 901,
				"tags": {"amenity": "parking"},
				"members": [
					{"type": "way", "ref": 1, "role": "outer", "geometry": [
						{"lat": 37.680, "lon": -121.770},
						{"lat": 37.680, "lon": -121.760},
						{"lat": 37.690, "lon": -121.760},
						{"lat": 37.690, "lon": -121.770},
						{"lat": 37.680, "lon": -121.770}
					]},
					{"type": "way", "ref": 2, "role": "inner", "geometry": [
						{"lat": 37.683, "lon": -121.767},
						{"lat": 37.683, "lon": -121.763},
						{"lat": 37.687, "lon": -121.763},
						{"lat": 37.687, "lon": -121.767},
						{"lat": 37.683, "lon": -121.767}
					]}
				]
			}
		]
	}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceParking)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	var geojson struct {
		Type        string           `json:"type"`
		Coordinates [][][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(features[0].GeometryJSON), &geojson); err != nil {
		t.Fatal(err)
	}
	if geojson.Type != "MultiPolygon" {
		t.Fatalf("expected MultiPolygon, got %s", geojson.Type)
	}
	if len(geojson.Coordinates) != 1 {
		t.Fatalf("expected 1 polygon, got %d", len(geojson.Coordinates))
	}
	if len(geojson.Coordinates[0]) != 2 {
		t.Errorf("expected outer ring + 1 hole, got %d rings", len(geojson.Coordinates[0]))
	}
}

func TestParseOverpassResponse_TruncationRemark(t *testing.T) {
	// A server-side timeout yields HTTP 200 + remark + partial elements.
	data := `{"remark": "runtime error: Query timed out in \"query\" at line 1 after 120 seconds.", "elements": []}`
	_, err := parseOverpassResponse(context.Background(), []byte(data), testResourceRoads)
	if err == nil {
		t.Fatal("expected error for truncation remark")
	}
	if !isParseError(err) {
		t.Errorf("truncation remark should be a parse-class error (to trigger retry): %v", err)
	}
}

// TestParseOverpassResponse_MalformedJSONIsParseError pins the typed-sentinel
// classification of a malformed-JSON parse failure (u78a): isParseError must
// match via errors.Is on errOverpassParse, not a fragile substring of the
// formatted message, so fetchRecursive still splits the bbox and retries.
func TestParseOverpassResponse_MalformedJSONIsParseError(t *testing.T) {
	_, err := parseOverpassResponse(context.Background(), []byte("{ this is not json"), testResourceRoads)
	if err == nil {
		t.Fatal("expected error for malformed json")
	}
	if !errors.Is(err, errOverpassParse) {
		t.Errorf("expected errors.Is(err, errOverpassParse), got: %v", err)
	}
	if !isParseError(err) {
		t.Errorf("malformed json should be a parse-class error (to trigger retry): %v", err)
	}
	// The truncation sentinel is a distinct parse-class cause and must NOT be
	// reported by a plain malformed-JSON error.
	if errors.Is(err, errOverpassTruncated) {
		t.Errorf("malformed json should not report errOverpassTruncated: %v", err)
	}
}

func TestParseOverpassResponse_BenignRemarkIgnored(t *testing.T) {
	data := `{"remark": "Note: some informational message", "elements": [
		{"type": "way", "id": 1, "tags": {"highway": "residential"}, "geometry": [
			{"lat": 37.68, "lon": -121.77}, {"lat": 37.69, "lon": -121.76}
		]}
	]}`
	features, err := parseOverpassResponse(context.Background(), []byte(data), testResourceRoads)
	if err != nil {
		t.Fatalf("benign remark should not error: %v", err)
	}
	if len(features) != 1 {
		t.Errorf("expected 1 feature despite benign remark, got %d", len(features))
	}
}

func TestCoordsToLineStringGeoJSON(t *testing.T) {
	coords := [][2]float64{{-121.77, 37.68}, {-121.76, 37.69}}
	result := coordsToLineStringGeoJSON(coords)
	var obj struct {
		Type        string       `json:"type"`
		Coordinates [][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Type != testGeomLineString {
		t.Errorf("expected type LineString, got %s", obj.Type)
	}
	if len(obj.Coordinates) != 2 {
		t.Errorf("expected 2 coordinates, got %d", len(obj.Coordinates))
	}
}

func TestCoordsToPolygonGeoJSON(t *testing.T) {
	coords := [][2]float64{
		{-121.77, 37.68}, {-121.76, 37.68},
		{-121.76, 37.69}, {-121.77, 37.69},
		{-121.77, 37.68},
	}
	result := coordsToPolygonGeoJSON(coords)
	var obj struct {
		Type        string         `json:"type"`
		Coordinates [][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Type != testGeomPolygon {
		t.Errorf("expected type Polygon, got %s", obj.Type)
	}
	if len(obj.Coordinates) != 1 {
		t.Errorf("expected 1 ring, got %d", len(obj.Coordinates))
	}
	if len(obj.Coordinates[0]) != 5 {
		t.Errorf("expected 5 coords in ring, got %d", len(obj.Coordinates[0]))
	}
}

// TestIsParseError_BodyTooLarge pins the q48z.4 fix. An oversized response body
// belongs to the "response unusable, shrink the bbox and retry" class that
// fetchRecursive splits quadrants for. Both routes to the sentinel must be
// recognised: this package's own ReadAllLimit call, and the cache transport's,
// which surfaces through client.Do wrapped in a *url.Error.
func TestIsParseError_BodyTooLarge(t *testing.T) {
	cases := map[string]error{
		"bare sentinel": httpio.ErrBodyTooLarge,
		"overpass read": fmt.Errorf("read overpass response: %w", httpio.ErrBodyTooLarge),
		"cache transport": fmt.Errorf("overpass request: %w",
			&url.Error{Op: "Post", URL: overpassAPI, Err: httpio.ErrBodyTooLarge}),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !isParseError(err) {
				t.Errorf("isParseError(%v) = false, want true", err)
			}
		})
	}

	// Guard the other direction: an ordinary transport failure is NOT this
	// class, and splitting the bbox on it would quadruple the request count
	// against an endpoint that is already failing.
	if isParseError(fmt.Errorf("overpass request: %w", errors.New("connection refused"))) {
		t.Errorf("isParseError matched an unrelated transport error")
	}
}

// roundTripFunc adapts a function to http.RoundTripper so a test can serve
// canned Overpass responses without the const overpassAPI endpoint.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestFetch_OversizedBodySplitsQuadrants is the behavioral half of the q48z.4
// fix: a first response over the size cap must drive the quadrant split, not
// abort the whole resource. Before the fix the oversized body surfaced as
// httpio.ErrBodyTooLarge, isParseError returned false, and fetchRecursive
// returned the error straight out — which fetchFromSources then turned into
// ErrAllSourcesFailed for a city that used to succeed after splitting.
func TestFetch_OversizedBodySplitsQuadrants(t *testing.T) {
	prev := maxResponseBodyBytes
	maxResponseBodyBytes = 256
	t.Cleanup(func() { maxResponseBodyBytes = prev })

	// One way per quadrant, with a distinct id so the cross-quadrant dedup
	// can't collapse them.
	quadrantBody := func(id int) string {
		return fmt.Sprintf(`{"elements":[{"type":"way","id":%d,`+
			`"tags":{"highway":"residential"},`+
			`"geometry":[{"lat":37.68,"lon":-121.77},{"lat":37.69,"lon":-121.76}]}]}`, id)
	}

	var mu sync.Mutex
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()

		body := quadrantBody(1000 + n)
		if n == 1 {
			// Comfortably over the 256-byte cap set above.
			body = `{"elements":[],"padding":"` + strings.Repeat("x", 512) + `"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}

	src := &OverpassSource{BBox: [4]float64{37.6, -121.9, 37.8, -121.6}}
	features, err := src.Fetch(context.Background(), client, resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// 1 oversized request at depth 0 + one per quadrant at depth 1.
	if requests != 5 {
		t.Errorf("made %d requests, want 5 (1 oversized + 4 quadrants)", requests)
	}
	if len(features) != 4 {
		t.Errorf("got %d features, want 4 (one per quadrant)", len(features))
	}
}
