package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

var rtRoads = resource.TypeRoads

func TestParseArcGISGeoJSON_BasicFeature(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 42, "FULLNAME": "First St"},
				"geometry": {"type": "LineString", "coordinates": [[-121.77, 37.68], [-121.76, 37.69]]}
			}
		]
	}`
	features, _, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	f := features[0]
	if f.ID != "arcgis:42" {
		t.Errorf("expected id arcgis:42, got %s", f.ID)
	}
	if f.Name != "First St" {
		t.Errorf("expected name First St, got %s", f.Name)
	}
	if f.ResourceType != rtRoads {
		t.Errorf("expected resource type pavements, got %s", f.ResourceType)
	}
}

func TestParseArcGISGeoJSON_NoOBJECTID(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"FULLNAME": "Second St"},
				"geometry": {"type": "LineString", "coordinates": [[-121.77, 37.68]]}
			}
		]
	}`
	features, _, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].ID != "arcgis:0" {
		t.Errorf("expected fallback id arcgis:0, got %s", features[0].ID)
	}
}

func TestParseArcGISGeoJSON_FULLNAMEExtraction(t *testing.T) {
	for _, key := range []string{"FULLNAME", "FullName", "fullname"} {
		data := `{"features": [{"properties": {"` + key + `": "Third Ave"}, "geometry": {"type":"Point","coordinates":[-121.77,37.68]}}]}`
		features, _, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(features) != 1 || features[0].Name != "Third Ave" {
			t.Errorf("key %s: expected name Third Ave, got %s", key, features[0].Name)
		}
	}
}

func TestParseArcGISGeoJSON_NullGeometry(t *testing.T) {
	// Rows with no usable geometry must be skipped. A missing "geometry"
	// field unmarshals to a nil RawMessage; an explicit "geometry": null
	// unmarshals to a non-nil RawMessage("null"). Both are skipped, but they
	// still count toward the raw row count returned for pagination.
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 1}
			},
			{
				"properties": {"OBJECTID": 2},
				"geometry": null
			}
		]
	}`
	features, rawCount, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features (missing + explicit-null geometry), got %d", len(features))
	}
	if rawCount != 2 {
		t.Errorf("expected raw count 2, got %d", rawCount)
	}
}

func TestParseArcGISGeoJSON_EmptyFeatures(t *testing.T) {
	data := `{"features": []}`
	features, _, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
}

func TestParseArcGISGeoJSON_NumericPropertyValues(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 1, "SPEED_LIMIT": 35},
				"geometry": {"type": "Point", "coordinates": [-121.77, 37.68]}
			}
		]
	}`
	features, _, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].Tags["SPEED_LIMIT"] != "35" {
		t.Errorf("expected SPEED_LIMIT tag '35', got %q", features[0].Tags["SPEED_LIMIT"])
	}
}

func TestParseArcGISGeoJSON_BaseIndexOffset(t *testing.T) {
	// Features without OBJECTID should use baseIndex+i as the fallback ID.
	data := `{"features": [
		{"properties": {"FULLNAME": "A St"}, "geometry": {"type":"Point","coordinates":[-121.77,37.68]}},
		{"properties": {"FULLNAME": "B St"}, "geometry": {"type":"Point","coordinates":[-121.76,37.69]}}
	]}`
	features, _, err := parseArcGISGeoJSON([]byte(data), rtRoads, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 2 {
		t.Fatalf("expected 2 features, got %d", len(features))
	}
	if features[0].ID != "arcgis:5000" {
		t.Errorf("expected id arcgis:5000, got %s", features[0].ID)
	}
	if features[1].ID != "arcgis:5001" {
		t.Errorf("expected id arcgis:5001, got %s", features[1].ID)
	}
}

// makeArcGISFeatures builds a GeoJSON response with n features, each having
// an OBJECTID starting at startOID.
func makeArcGISFeatures(n, startOID int) []byte {
	type feat struct {
		Properties map[string]any  `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	}
	feats := make([]feat, n)
	for i := range feats {
		feats[i] = feat{
			Properties: map[string]any{"OBJECTID": startOID + i},
			Geometry:   json.RawMessage(`{"type":"Point","coordinates":[-121.77,37.68]}`),
		}
	}
	data, _ := json.Marshal(map[string]any{"features": feats})
	return data
}

// makeArcGISPage builds a GeoJSON page with n features (OBJECTIDs from
// startOID) and the exceededTransferLimit flag nested under "properties",
// matching how f=geojson services (e.g. Alameda County) signal more rows.
func makeArcGISPage(n, startOID int, exceeded bool) []byte {
	type feat struct {
		Properties map[string]any  `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	}
	feats := make([]feat, n)
	for i := range feats {
		feats[i] = feat{
			Properties: map[string]any{"OBJECTID": startOID + i},
			Geometry:   json.RawMessage(`{"type":"Point","coordinates":[-121.77,37.68]}`),
		}
	}
	data, _ := json.Marshal(map[string]any{
		"features":   feats,
		"properties": map[string]any{"exceededTransferLimit": exceeded},
	})
	return data
}

// makeArcGISNullPage builds a GeoJSON page with n features that all carry an
// explicit "geometry": null, plus the exceededTransferLimit flag nested under
// "properties". Every feature here is dropped by the null-geometry guard, so
// the page contributes zero post-filter features — used to prove pagination is
// driven off the raw row count, not the filtered slice length.
func makeArcGISNullPage(n, startOID int, exceeded bool) []byte {
	type feat struct {
		Properties map[string]any  `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	}
	feats := make([]feat, n)
	for i := range feats {
		feats[i] = feat{
			Properties: map[string]any{"OBJECTID": startOID + i},
			Geometry:   json.RawMessage(`null`),
		}
	}
	data, _ := json.Marshal(map[string]any{
		"features":   feats,
		"properties": map[string]any{"exceededTransferLimit": exceeded},
	})
	return data
}

// TestFetch_PaginationContinuesPastAllNullPage pins the fix for the pagination
// bug (yvlv.10): a page whose rows are all dropped by the null-geometry guard
// contributes zero post-filter features, but exceededTransferLimit is set. The
// loop must keep paging (off the raw row count) instead of terminating, and the
// offset must advance by the raw count so page 2 is not re-read.
func TestFetch_PaginationContinuesPastAllNullPage(t *testing.T) {
	const nullPageSize = 100 // all skipped, but exceeded=true
	const realPageSize = 30

	calls := 0
	var sawOffsets []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		sawOffsets = append(sawOffsets, offset)
		var body []byte
		switch offset {
		case 0:
			body = makeArcGISNullPage(nullPageSize, 1, true)
		case nullPageSize:
			body = makeArcGISPage(realPageSize, nullPageSize+1, false)
		default:
			t.Errorf("unexpected offset %d", offset)
			http.Error(w, "bad offset", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != realPageSize {
		t.Fatalf("expected %d real features (all-null page skipped), got %d", realPageSize, len(features))
	}
	if calls != 2 {
		t.Errorf("expected 2 server calls (paged past all-null page), got %d", calls)
	}
	if len(sawOffsets) != 2 || sawOffsets[0] != 0 || sawOffsets[1] != nullPageSize {
		t.Errorf("expected offsets [0 %d], got %v", nullPageSize, sawOffsets)
	}
	// The real features must actually be the page-2 rows.
	if features[0].ID != fmt.Sprintf("arcgis:%d", nullPageSize+1) {
		t.Errorf("first feature id: want arcgis:%d, got %s", nullPageSize+1, features[0].ID)
	}
}

// TestParseArcGISGeoJSON_LargeNumericFormatting pins the fix for yvlv.11: JSON
// numbers decode to float64, so a large integral OBJECTID or numeric property
// must render as plain decimal, not scientific notation.
func TestParseArcGISGeoJSON_LargeNumericFormatting(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 1234567, "LENGTH_FT": 9876543},
				"geometry": {"type": "Point", "coordinates": [-121.77, 37.68]}
			}
		]
	}`
	features, _, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].ID != "arcgis:1234567" {
		t.Errorf("expected id arcgis:1234567 (plain decimal), got %s", features[0].ID)
	}
	if got := features[0].Tags["LENGTH_FT"]; got != "9876543" {
		t.Errorf("expected LENGTH_FT tag '9876543' (plain decimal), got %q", got)
	}
	if got := features[0].Tags["OBJECTID"]; got != "1234567" {
		t.Errorf("expected OBJECTID tag '1234567' (plain decimal), got %q", got)
	}
}

// TestFetch_PreservesEndpointQueryString pins the fix for yvlv.12: an
// arcgis_url that already carries a query string (e.g. ?token=...) must have
// its params preserved while the pagination params are merged in.
func TestFetch_PreservesEndpointQueryString(t *testing.T) {
	var gotToken string
	var gotWhere string
	var gotOffset string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		if q.Get("resultOffset") == "0" {
			// Capture the first page's merged params; the flag is absent, so a
			// terminating empty page follows at the next offset.
			gotToken = q.Get("token")
			gotWhere = q.Get("where")
			gotOffset = q.Get("resultOffset")
			w.Write(makeArcGISFeatures(10, 1))
			return
		}
		w.Write(makeArcGISFeatures(0, 1)) // empty page past the last record
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL + "?token=abc",
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	if _, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads)); err != nil {
		t.Fatal(err)
	}
	if gotToken != "abc" {
		t.Errorf("expected token=abc preserved, got %q", gotToken)
	}
	if gotWhere != "1=1" {
		t.Errorf("expected pagination param where=1=1 merged in, got %q", gotWhere)
	}
	if gotOffset != "0" {
		t.Errorf("expected pagination param resultOffset=0 merged in, got %q", gotOffset)
	}
}

// TestFetch_MetadataRequestPreservesEndpointQueryString is the companion for
// the layer-metadata request added by solvent-streets-ofbo. It is derived from
// the endpoint by trimming /query, so it must carry the same credentials —
// otherwise a token-gated layer answers 499 for the metadata, completeness
// becomes unverifiable, and the fail-closed default turns a working ingest
// into a hard error.
func TestFetch_MetadataRequestPreservesEndpointQueryString(t *testing.T) {
	prevPages := arcgisMaxPages
	arcgisMaxPages = 8
	t.Cleanup(func() { arcgisMaxPages = prevPages })

	var metaToken, metaFormat string
	metaHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/FeatureServer/0/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(makeArcGISFeatures(10, 1)) // offset-ignoring: always the same rows
	})
	mux.HandleFunc("/FeatureServer/0", func(w http.ResponseWriter, r *http.Request) {
		metaHits++
		metaToken = r.URL.Query().Get("token")
		metaFormat = r.URL.Query().Get("f")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"maxRecordCount":100}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL + "/FeatureServer/0/query?token=abc",
		AllowPrivate: true,
	}
	if _, err := src.Fetch(context.Background(), http.DefaultClient, resource.ByType(resource.TypeRoads)); err != nil {
		t.Fatal(err)
	}
	if metaHits != 1 {
		t.Errorf("layer metadata requested %d times, want exactly 1 (lazy, once per Fetch)", metaHits)
	}
	if metaToken != "abc" {
		t.Errorf("metadata request token = %q, want the endpoint's own token preserved", metaToken)
	}
	if metaFormat != "json" {
		t.Errorf("metadata request f = %q, want json (the layer document, not geojson)", metaFormat)
	}
}

// TestFetch_HealthyPaginationSkipsLayerMetadata pins the laziness that makes
// the ofbo check free: a server that terminates normally — via the flag or an
// empty page — never reaches the repeated-page arm, so it must never pay for
// the extra metadata request.
func TestFetch_HealthyPaginationSkipsLayerMetadata(t *testing.T) {
	metaHits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/FeatureServer/0/query", func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		w.Header().Set("Content-Type", "application/json")
		if offset >= 20 {
			_, _ = w.Write(makeArcGISFeatures(0, 1)) // past the last record
			return
		}
		_, _ = w.Write(makeArcGISFeatures(10, offset+1))
	})
	mux.HandleFunc("/FeatureServer/0", func(w http.ResponseWriter, r *http.Request) {
		metaHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"maxRecordCount":100}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL + "/FeatureServer/0/query",
		AllowPrivate: true,
	}
	features, err := src.Fetch(context.Background(), http.DefaultClient, resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 20 {
		t.Errorf("got %d features, want 20", len(features))
	}
	if metaHits != 0 {
		t.Errorf("layer metadata requested %d times on a healthy paginating server; "+
			"the check must cost nothing on the common path", metaHits)
	}
}

// TestFetch_PaginationExceededTransferLimit pins the fix for the truncation
// bug: ArcGIS servers clamp each response to their own maxRecordCount, which
// can be below our requested arcgisMaxRecords. Pages here are far smaller than
// arcgisMaxRecords, so the old "short page == last page" heuristic would have
// stopped after page 1. Paging must instead follow exceededTransferLimit.
func TestFetch_PaginationExceededTransferLimit(t *testing.T) {
	const pageSize = 100 // well under arcgisMaxRecords
	const lastPageSize = 50

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		var body []byte
		switch offset {
		case 0:
			body = makeArcGISPage(pageSize, 1, true)
		case pageSize:
			body = makeArcGISPage(pageSize, pageSize+1, true)
		case 2 * pageSize:
			body = makeArcGISPage(lastPageSize, 2*pageSize+1, false)
		default:
			t.Errorf("unexpected offset %d", offset)
			http.Error(w, "bad offset", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	if want := 2*pageSize + lastPageSize; len(features) != want {
		t.Fatalf("expected %d features across all pages, got %d", want, len(features))
	}
	if calls != 3 {
		t.Errorf("expected 3 server calls, got %d", calls)
	}
}

// TestFetch_PaginationFlagOmittedShortPages pins the fix for solvent-streets-9auy:
// a server that clamps its page size below arcgisMaxRecords AND omits
// exceededTransferLimit entirely. The old break condition
// `!exceeded && rawCount < arcgisMaxRecords` would stop after page 1 (100 < 5000,
// flag absent -> exceeded=false), silently dropping every later page. The fix
// paginates until an empty page, so all rows are fetched.
func TestFetch_PaginationFlagOmittedShortPages(t *testing.T) {
	const pageSize = 100 // well under arcgisMaxRecords, and the flag is never sent
	const pages = 3

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		w.Header().Set("Content-Type", "application/json")
		if offset >= pages*pageSize {
			// Past the last record: an empty page is the only reliable end signal.
			w.Write(makeArcGISFeatures(0, 1))
			return
		}
		w.Write(makeArcGISFeatures(pageSize, offset+1))
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	if want := pages * pageSize; len(features) != want {
		t.Fatalf("expected %d features across all pages, got %d (short-page heuristic truncated?)", want, len(features))
	}
	if want := pages + 1; calls != want { // pages + one terminating empty page
		t.Errorf("expected %d server calls, got %d", want, calls)
	}
}

func TestFetch_Pagination(t *testing.T) {
	// Simulate a server that returns arcgisMaxRecords on the first page
	// and a smaller set on the second page.
	pageSize := arcgisMaxRecords
	const page2Size = 42

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		var body []byte
		switch offset {
		case 0:
			body = makeArcGISFeatures(pageSize, 1)
		case pageSize:
			body = makeArcGISFeatures(page2Size, pageSize+1)
		default:
			// Neither page carries exceededTransferLimit, so termination is
			// confirmed by an empty page past the last record (as a real ArcGIS
			// service returns for an offset beyond its row count).
			body = makeArcGISFeatures(0, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	rt := resource.ByType(resource.TypeRoads)
	features, err := src.Fetch(context.Background(), srv.Client(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != pageSize+page2Size {
		t.Fatalf("expected %d features, got %d", pageSize+page2Size, len(features))
	}
	if calls != 3 { // page 0, page 1 (short, flag absent), terminating empty page
		t.Errorf("expected 3 server calls, got %d", calls)
	}
	// Verify IDs from both pages are present.
	if features[0].ID != "arcgis:1" {
		t.Errorf("first feature id: want arcgis:1, got %s", features[0].ID)
	}
	if features[pageSize].ID != fmt.Sprintf("arcgis:%d", pageSize+1) {
		t.Errorf("first page-2 feature id: want arcgis:%d, got %s", pageSize+1, features[pageSize].ID)
	}
}

// TestFetch_PagesOrderedByOBJECTID pins that every paged ArcGIS query
// carries orderByFields=OBJECTID, so offset-based paging is deterministic
// across hosted and non-hosted (map-service/enterprise) backends.
// Regression for solvent-streets-2a7n.26.
func TestFetch_PagesOrderedByOBJECTID(t *testing.T) {
	var sawOrderBy []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawOrderBy = append(sawOrderBy, r.URL.Query().Get("orderByFields"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		w.Header().Set("Content-Type", "application/json")
		if offset == 0 {
			w.Write(makeArcGISFeatures(10, 1)) // short page, no exceededTransferLimit flag
			return
		}
		w.Write(makeArcGISFeatures(0, 1)) // empty page past the last record
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	if _, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads)); err != nil {
		t.Fatal(err)
	}
	if len(sawOrderBy) == 0 {
		t.Fatal("expected at least one request")
	}
	for i, got := range sawOrderBy {
		if got != "OBJECTID" {
			t.Errorf("request %d: orderByFields=%q, want %q", i, got, "OBJECTID")
		}
	}
}

func TestFetch_SinglePage(t *testing.T) {
	// A single short page with NO exceededTransferLimit flag. Because a missing
	// flag is not authoritative (the server might have clamped and dropped rows),
	// termination is confirmed by one follow-up request that returns an empty
	// page — so a genuine single-page dataset costs one extra confirming request.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		w.Header().Set("Content-Type", "application/json")
		if offset == 0 {
			w.Write(makeArcGISFeatures(10, 1))
			return
		}
		w.Write(makeArcGISFeatures(0, 1)) // empty page past the last record
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 10 {
		t.Fatalf("expected 10 features, got %d", len(features))
	}
	if calls != 2 { // the short page, then a terminating empty page
		t.Errorf("expected 2 server calls, got %d", calls)
	}
}

func TestFetch_ArcGISErrorEnvelope(t *testing.T) {
	// ArcGIS returns service-level errors as HTTP 200 with a JSON error
	// envelope. The fetcher should surface the message and endpoint URL
	// rather than silently treating the response as an empty feature list.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Invalid URL","details":["Invalid URL"]}}`))
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	_, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err == nil {
		t.Fatal("expected error for ArcGIS error envelope, got nil")
	}
	msg := err.Error()
	for _, want := range []string{srv.URL, "code 400", "Invalid URL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing expected substring %q", msg, want)
		}
	}
}

func TestFetch_NonOKStatusIncludesEndpoint(t *testing.T) {
	// HTTP 400/500 responses must include the endpoint URL so a stale or
	// misconfigured service is debuggable from the error alone.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Bad Request", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	_, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err == nil {
		t.Fatal("expected error for HTTP 400, got nil")
	}
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error %q does not include endpoint %q", err.Error(), srv.URL)
	}
}

func TestArcGISErrorMessage(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		wantOK bool
		want   string
	}{
		{
			name:   "error envelope with details",
			body:   `{"error":{"code":400,"message":"Invalid URL","details":["Invalid URL"]}}`,
			wantOK: true,
			want:   "code 400: Invalid URL",
		},
		{
			name:   "error envelope with distinct detail",
			body:   `{"error":{"code":498,"message":"Invalid Token","details":["Token expired"]}}`,
			wantOK: true,
			want:   "code 498: Invalid Token (Token expired)",
		},
		{
			name:   "valid feature collection",
			body:   `{"features":[{"properties":{"OBJECTID":1},"geometry":{"type":"Point","coordinates":[0,0]}}]}`,
			wantOK: false,
		},
		{
			name:   "empty features",
			body:   `{"features":[]}`,
			wantOK: false,
		},
		{
			name:   "invalid json",
			body:   `not json`,
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := arcgisErrorMessage([]byte(tc.body))
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v (got=%q)", ok, tc.wantOK, got)
			}
			if ok && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFetch_RefusesLoopback pins the SSRF guard: a hostile pvmt.toml
// pointing arcgis_url at localhost / 169.254.169.254 / RFC1918 must be
// refused with a clear error referencing the override flag. Regression
// for solvent-streets-di49.
func TestFetch_RefusesLoopback(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"loopback IPv4", "http://127.0.0.1/arcgis/rest/services/x/FeatureServer/0/query"},
		{"loopback IPv6", "http://[::1]/arcgis/rest/services/x/FeatureServer/0/query"},
		{"link-local imds", "http://169.254.169.254/latest/meta-data/"},
		{"rfc1918 10/8", "http://10.0.0.1/arcgis/rest/services/x/FeatureServer/0/query"},
		{"rfc1918 192.168/16", "http://192.168.1.1/arcgis/rest/services/x/FeatureServer/0/query"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &ArcGISSource{
				BBox: [4]float64{37.0, -122.0, 38.0, -121.0},
				URL:  tc.url,
			}
			_, err := src.Fetch(context.Background(), http.DefaultClient, resource.ByType(resource.TypeRoads))
			if err == nil {
				t.Fatalf("expected SSRF refusal for %s, got nil", tc.url)
			}
			if !strings.Contains(err.Error(), "allow_private_arcgis") {
				t.Errorf("expected override hint in error, got: %v", err)
			}
		})
	}
}

// TestFetch_AllowPrivateOptIn confirms the override path: with
// AllowPrivate=true, a loopback URL is no longer refused at the SSRF
// layer (and reaches the real fetch, which here returns a valid empty
// FeatureCollection).
func TestFetch_AllowPrivateOptIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"features": []}`))
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true,
	}
	_, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatalf("expected loopback override to permit fetch, got: %v", err)
	}
}

// TestFetch_DeduplicatesAcrossPages pins the cross-page dedup (ak42): Esri
// offset paging can re-return a boundary OBJECTID on the next page (reorder /
// mid-clamp overlap). The deduped result must contain each OBJECTID once, while
// pagination still advances off the RAW server row count (so the overlap does
// not stall the offset).
func TestFetch_DeduplicatesAcrossPages(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		w.Header().Set("Content-Type", "application/json")
		switch offset {
		case 0:
			// OBJECTIDs 1..10, more rows remain.
			w.Write(makeArcGISPage(10, 1, true))
		case 10:
			// OBJECTIDs 10..19 — OBJECTID 10 overlaps page 1. Last page.
			w.Write(makeArcGISPage(10, 10, false))
		default:
			t.Errorf("unexpected offset %d", offset)
			http.Error(w, "bad offset", 400)
		}
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	// 10 + 10 raw rows, one shared OBJECTID (10) → 19 unique features.
	if len(features) != 19 {
		t.Fatalf("expected 19 unique features after dedup, got %d", len(features))
	}
	if calls != 2 {
		t.Errorf("expected 2 server calls, got %d", calls)
	}
	seen := make(map[string]bool)
	for _, f := range features {
		if seen[f.ID] {
			t.Errorf("duplicate feature id survived dedup: %s", f.ID)
		}
		seen[f.ID] = true
	}
}

// TestParseArcGISGeoJSON_LargeOBJECTIDPrecision pins the UseNumber fix (j4x8):
// an OBJECTID above 2^53 must keep full integer precision as the feature id /
// dedupe key. Plain json.Unmarshal into `any` decodes numbers to float64, which
// would round 9007199254740993 down to ...992 and collide with a neighbour.
func TestParseArcGISGeoJSON_LargeOBJECTIDPrecision(t *testing.T) {
	const bigOID = "9007199254740993" // 2^53 + 1, not representable as float64
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": ` + bigOID + `, "LEN": 42.5},
				"geometry": {"type": "Point", "coordinates": [-121.77, 37.68]}
			}
		]
	}`
	features, _, err := parseArcGISGeoJSON([]byte(data), rtRoads, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if want := "arcgis:" + bigOID; features[0].ID != want {
		t.Errorf("expected id %s (full precision), got %s", want, features[0].ID)
	}
	if got := features[0].Tags["OBJECTID"]; got != bigOID {
		t.Errorf("expected OBJECTID tag %q (full precision), got %q", bigOID, got)
	}
	// Fractional float attributes must still render correctly, not be mangled.
	if got := features[0].Tags["LEN"]; got != "42.5" {
		t.Errorf("expected LEN tag '42.5', got %q", got)
	}
}

// TestParseArcGISPage_SingleParseDerivesAll pins that one decode of a page body
// yields the error envelope, features, and the exceededTransferLimit flag
// consistently (6xti). Here a well-formed FeatureCollection carries the flag
// nested under top-level "properties" and has no error.
func TestParseArcGISPage_SingleParseDerivesAll(t *testing.T) {
	body := makeArcGISPage(3, 1, true) // 3 features, exceeded=true under "properties"
	page, err := parseArcGISPage(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := arcgisErrorMessageFromPage(page); ok {
		t.Error("valid FeatureCollection should not be an error envelope")
	}
	features, rawCount := featuresFromArcGISPage(page, rtRoads, 0)
	if len(features) != 3 || rawCount != 3 {
		t.Errorf("expected 3 features and rawCount 3, got %d / %d", len(features), rawCount)
	}
	exceeded, present := transferLimitFromPage(page)
	if !present || !exceeded {
		t.Errorf("expected exceededTransferLimit present=true exceeded=true, got present=%v exceeded=%v", present, exceeded)
	}

	// And an error envelope parses into the error branch with no features.
	errPage, err := parseArcGISPage([]byte(`{"error":{"code":498,"message":"Invalid Token"}}`))
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := arcgisErrorMessageFromPage(errPage)
	if !ok || !strings.Contains(msg, "498") {
		t.Errorf("expected error envelope message with code 498, got ok=%v msg=%q", ok, msg)
	}
}

func TestFetch_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"features": []}`))
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
}

// offsetIgnoringServer stands up a layer that ignores resultOffset — pre-10.3,
// or supportsPagination:false — re-serving the same OBJECTIDs forever with no
// exceededTransferLimit. rows is what every query returns; maxRecordCount is
// what the layer document advertises, which is what tells the two cases in
// solvent-streets-ofbo apart. A negative maxRecordCount omits the field.
//
// The layer document lives at the query URL minus /query, so this needs a mux:
// a single catch-all handler would answer the metadata request with features.
func offsetIgnoringServer(t *testing.T, rows, maxRecordCount int) (endpoint string, queryCalls *int) {
	t.Helper()
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/FeatureServer/0/query", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(makeArcGISFeatures(rows, 1))
	})
	mux.HandleFunc("/FeatureServer/0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if maxRecordCount < 0 {
			_, _ = io.WriteString(w, `{"name":"Streets","type":"Feature Layer"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"name":"Streets","maxRecordCount":%d}`, maxRecordCount)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/FeatureServer/0/query", &calls
}

// TestFetch_OffsetIgnoringServerStopsCleanly pins the q48z.5 fix, and covers
// the case TestFetch_PaginationFlagOmittedShortPages cannot: that server HONORS
// resultOffset, so it always reaches an empty page.
//
// A server that IGNORES resultOffset and omits exceededTransferLimit — pre-10.3
// layers, or supportsPagination:false — re-serves page 1 forever. rawCount is
// never 0 and the flag is never present, so the two original termination rules
// could not fire and the loop ran to the page cap, which returned an error and
// discarded every fetched feature after 200 requests against the operator's
// endpoint — for a layer that answered completely in ONE request before v0.3.0.
//
// Restoring the old short-page break is NOT the fix: trusting a short page is
// exactly the truncation that dropped 2000 of 5780 Livermore centerlines
// (solvent-streets-9auy). Instead we stop when a page parses rows but adds no
// new ids AND the layer's own maxRecordCount proves the page was not clamped.
// Here 10 rows sit well under the advertised 100, so the layer really did
// answer completely.
func TestFetch_OffsetIgnoringServerStopsCleanly(t *testing.T) {
	prevPages := arcgisMaxPages
	arcgisMaxPages = 8
	t.Cleanup(func() { arcgisMaxPages = prevPages })

	const pageSize = 10
	endpoint, calls := offsetIgnoringServer(t, pageSize, 100)

	var progress strings.Builder
	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          endpoint,
		Progress:     &progress,
		AllowPrivate: true, // httptest.Server binds 127.0.0.1; the SSRF guard would otherwise refuse it.
	}
	features, err := src.Fetch(context.Background(), http.DefaultClient, resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatalf("Fetch on an offset-ignoring server: %v (must stop cleanly, not abort)", err)
	}
	if len(features) != pageSize {
		t.Errorf("got %d features, want %d (the layer's full result)", len(features), pageSize)
	}
	// Page 1 fetches, page 2 reveals the repeat. Anything more is wasted load
	// on the operator's endpoint; anything less means we never paged at all.
	// The metadata request is not a query and is deliberately not counted.
	if *calls != 2 {
		t.Errorf("made %d query requests, want 2 (fetch, then detect the repeat)", *calls)
	}
	if *calls >= arcgisMaxPages {
		t.Errorf("ran to the page cap; the no-new-ids rule did not fire")
	}
}

// TestFetch_OffsetIgnoringServerOverOurCapSucceeds pins solvent-streets-szq9:
// the false-refusal case that sat between the two ofbo halves below.
//
// Same wire signature again — offset ignored, rows repeat, no
// exceededTransferLimit — but the page is LARGER than the resultRecordCount we
// requested. That is only possible if the server ignored resultRecordCount, so
// our cap cannot be what clamped the page; the layer's own maxRecordCount is the
// only limit that could, and the page sits well under it. The result is
// complete and must be stored.
//
// Before the fix, repeatedPageOutcome compared against
// min(maxRecordCount, arcgisMaxRecords) across the whole range, so this returned
// pageTruncated and Fetch refused a layer it had read in full. It failed safe —
// stored rows survived — but ingest hard-errored on a healthy endpoint.
//
// arcgisMaxRecords is shrunk rather than serving 5001+ rows, the same trade
// arcgisMaxPages already documents.
func TestFetch_OffsetIgnoringServerOverOurCapSucceeds(t *testing.T) {
	prevPages, prevRecords := arcgisMaxPages, arcgisMaxRecords
	arcgisMaxPages, arcgisMaxRecords = 8, 5
	t.Cleanup(func() { arcgisMaxPages, arcgisMaxRecords = prevPages, prevRecords })

	// arcgisMaxRecords (5) < pageSize (6) < maxRecordCount (10).
	const pageSize = 6
	endpoint, calls := offsetIgnoringServer(t, pageSize, 10)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          endpoint,
		AllowPrivate: true,
	}
	features, err := src.Fetch(context.Background(), http.DefaultClient, resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatalf("Fetch refused a complete layer: %v", err)
	}
	if len(features) != pageSize {
		t.Errorf("got %d features, want %d (the layer's full result)", len(features), pageSize)
	}
	if *calls != 2 {
		t.Errorf("made %d query requests, want 2 (fetch, then detect the repeat)", *calls)
	}
}

// TestFetch_OffsetIgnoringServerAtMaxRecordCountErrors is the other half of
// solvent-streets-ofbo: the SAME wire signature as the test above — repeated
// rows, no exceededTransferLimit — but the page is exactly the layer's
// maxRecordCount, so the server clamped and the rest of the layer was withheld.
//
// Returning these rows as a success is destructive, not merely lossy: a
// truncated fetch that reports no error leaves replaceSources nil in
// pkg/cmd/ingest, so UpsertFeatures DELETEs every complete stored row for the
// resource and replaces it with the short set. Fetch must fail instead, and
// must return no features so there is nothing to store.
func TestFetch_OffsetIgnoringServerAtMaxRecordCountErrors(t *testing.T) {
	prevPages := arcgisMaxPages
	arcgisMaxPages = 8
	t.Cleanup(func() { arcgisMaxPages = prevPages })

	const pageSize = 10
	endpoint, _ := offsetIgnoringServer(t, pageSize, pageSize) // clamped: rows == cap

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          endpoint,
		AllowPrivate: true,
	}
	features, err := src.Fetch(context.Background(), http.DefaultClient, resource.ByType(resource.TypeRoads))
	if err == nil {
		t.Fatalf("Fetch succeeded with %d features; a clamped offset-ignoring layer is TRUNCATED "+
			"and storing it deletes the complete rows already in the database", len(features))
	}
	if features != nil {
		t.Errorf("got %d features alongside the error; a failed fetch must store nothing", len(features))
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("error %q does not say the result was truncated", err)
	}
}

// TestFetch_RepeatedPageWithUnverifiableLayerErrors pins the fail-closed
// default. When the layer document cannot be read there is no way to tell a
// complete single-response layer from a clamped one, and an unproven result
// must not overwrite stored rows — so Fetch fails rather than returning what
// it happens to hold.
func TestFetch_RepeatedPageWithUnverifiableLayerErrors(t *testing.T) {
	prevPages := arcgisMaxPages
	arcgisMaxPages = 8
	t.Cleanup(func() { arcgisMaxPages = prevPages })

	for _, tt := range []struct {
		name string
		meta func(w http.ResponseWriter)
	}{
		{"metadata 404", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "not found")
		}},
		{"metadata error envelope", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"error":{"code":499,"message":"Token Required"}}`)
		}},
		{"maxRecordCount omitted", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"name":"Streets","type":"Feature Layer"}`)
		}},
		{"maxRecordCount zero", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"name":"Streets","maxRecordCount":0}`)
		}},
		{"metadata not json", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `<html>proxy error</html>`)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/FeatureServer/0/query", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(makeArcGISFeatures(10, 1))
			})
			mux.HandleFunc("/FeatureServer/0", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				tt.meta(w)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			src := &ArcGISSource{
				BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
				URL:          srv.URL + "/FeatureServer/0/query",
				AllowPrivate: true,
			}
			features, err := src.Fetch(context.Background(), http.DefaultClient, resource.ByType(resource.TypeRoads))
			if err == nil {
				t.Fatalf("Fetch succeeded with %d features; completeness was never proven", len(features))
			}
			if features != nil {
				t.Errorf("got %d features alongside the error; a failed fetch must store nothing", len(features))
			}
		})
	}
}

// TestFetch_RepeatedPageWithoutQuerySuffixErrors covers the endpoint shape that
// has no layer document to ask: without a /query suffix there is nothing to
// trim, so completeness is unverifiable and the fail-closed default applies.
func TestFetch_RepeatedPageWithoutQuerySuffixErrors(t *testing.T) {
	prevPages := arcgisMaxPages
	arcgisMaxPages = 8
	t.Cleanup(func() { arcgisMaxPages = prevPages })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(makeArcGISFeatures(10, 1))
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL, // no /query to trim
		AllowPrivate: true,
	}
	if _, err := src.Fetch(context.Background(), http.DefaultClient, resource.ByType(resource.TypeRoads)); err == nil {
		t.Fatal("Fetch succeeded; an endpoint with no layer document cannot prove completeness")
	}
}

// TestRepeatedPageOutcome is the pure-logic half of solvent-streets-ofbo.
func TestRepeatedPageOutcome(t *testing.T) {
	boom := errors.New("metadata unavailable")
	for _, tt := range []struct {
		name     string
		rawCount int
		maxRC    int
		metaErr  error
		want     pageOutcome
	}{
		{"well under the cap is complete", 10, 2000, nil, pageRepeatedRows},
		{"one under the cap is complete", 1999, 2000, nil, pageRepeatedRows},
		{"exactly at the cap is clamped", 2000, 2000, nil, pageTruncated},
		{"over the cap is clamped", 2500, 2000, nil, pageTruncated},
		// A layer advertising MORE than we request: our own resultRecordCount
		// is the binding limit, so a full page is still a clamped page.
		// Comparing against maxRecordCount alone read these as complete and
		// silently dropped every row past the first 5000.
		{"full page under a larger cap is still clamped", arcgisMaxRecords, 10000, nil, pageTruncated},
		// solvent-streets-szq9. ABOVE our own cap the server returned more rows
		// than we asked for, proving it never applied resultRecordCount — so our
		// cap cannot be what clamped this page, and only maxRecordCount can
		// settle it. Reading it as truncated hard-failed ingest on layers we had
		// read in full.
		{"over our own request cap proves the server ignored our cap", arcgisMaxRecords + 1, 10000, nil, pageRepeatedRows},
		{"over our own cap and at the server cap is clamped", 10000, 10000, nil, pageTruncated},
		{"over our own cap and past the server cap is clamped", 10001, 10000, nil, pageTruncated},
		{"short page under a larger cap is complete", arcgisMaxRecords - 1, 10000, nil, pageRepeatedRows},
		{"metadata error fails closed", 10, 2000, boom, pageTruncated},
		{"absent cap fails closed", 10, 0, nil, pageTruncated},
		{"negative cap fails closed", 10, -1, nil, pageTruncated},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := repeatedPageOutcome(tt.rawCount, tt.maxRC, tt.metaErr); got != tt.want {
				t.Errorf("repeatedPageOutcome(%d, %d, %v) = %v, want %v",
					tt.rawCount, tt.maxRC, tt.metaErr, got, tt.want)
			}
		})
	}
}

// TestFetch_NullGeometryPageKeepsPaging guards the no-new-ids rule against the
// case it must NOT fire on: a page whose rows were all dropped by the
// null-geometry filter also contributes no new ids, but says nothing about
// whether more rows remain. Breaking there would truncate.
func TestFetch_NullGeometryPageKeepsPaging(t *testing.T) {
	const pageSize = 10

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case offset == pageSize:
			// A full page of rows that all fail the geometry guard: rawCount
			// is pageSize, but zero features survive.
			_, _ = w.Write(makeArcGISNullGeometryPage(pageSize))
		case offset >= 3*pageSize:
			_, _ = w.Write(makeArcGISFeatures(0, 1))
		default:
			_, _ = w.Write(makeArcGISFeatures(pageSize, offset+1))
		}
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true,
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err != nil {
		t.Fatal(err)
	}
	// Pages at offsets 0 and 2*pageSize carry real rows; the middle page is
	// all-null and must not stop the loop.
	if want := 2 * pageSize; len(features) != want {
		t.Errorf("got %d features, want %d (an all-null page must not terminate paging)", len(features), want)
	}
}

// makeArcGISNullGeometryPage builds a page of n rows that all have null
// geometry, so rawCount is n but every row is dropped by the geometry guard.
func makeArcGISNullGeometryPage(n int) []byte {
	type feat struct {
		Properties map[string]any  `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	}
	feats := make([]feat, n)
	for i := range feats {
		feats[i] = feat{
			Properties: map[string]any{"OBJECTID": 90000 + i},
			Geometry:   json.RawMessage(`null`),
		}
	}
	data, _ := json.Marshal(map[string]any{"features": feats})
	return data
}

// TestFetch_RepeatedRowsWithExceededFlagErrors guards the other side of the
// no-new-ids rule. A server that re-serves rows we already hold WHILE setting
// exceededTransferLimit is reporting that more rows remain — that is truncation,
// not a complete result.
//
// Returning a partial here would be worse than failing: with arcgis as the only
// source, fetchFromSources marks it succeeded, replaceSources stays nil, and
// UpsertFeatures DELETEs every stored row for the resource before inserting the
// truncated set — trading a complete previous ingest for a partial one, with no
// error anywhere. The error path preserves what is already in the database.
func TestFetch_RepeatedRowsWithExceededFlagErrors(t *testing.T) {
	prevPages := arcgisMaxPages
	arcgisMaxPages = 8
	t.Cleanup(func() { arcgisMaxPages = prevPages })

	const pageSize = 10

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Same rows every time AND "there are more rows" — a clamping layer
		// that ignores resultOffset.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(makeArcGISPage(pageSize, 1, true))
	}))
	t.Cleanup(srv.Close)

	src := &ArcGISSource{
		BBox:         [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:          srv.URL,
		AllowPrivate: true,
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByType(resource.TypeRoads))
	if err == nil {
		t.Fatalf("Fetch returned %d features and no error; a truncated result must not be "+
			"reported as success (it would destructively replace good stored rows)", len(features))
	}
	if features != nil {
		t.Errorf("Fetch returned %d features alongside the error; want nil", len(features))
	}
}

// TestClassifyArcGISPage covers the pagination termination rules directly, so
// each is pinned independently of the HTTP plumbing that exercises them.
func TestClassifyArcGISPage(t *testing.T) {
	cases := map[string]struct {
		rawCount, parsed, newUnique int
		exceeded, flagPresent       bool
		want                        pageOutcome
	}{
		"empty page ends pagination": {
			rawCount: 0, want: pageDone,
		},
		"flag present and false ends pagination": {
			rawCount: 100, parsed: 100, newUnique: 100, exceeded: false, flagPresent: true, want: pageDone,
		},
		"flag present and true keeps paging": {
			rawCount: 100, parsed: 100, newUnique: 100, exceeded: true, flagPresent: true, want: pageContinue,
		},
		// The Livermore truncation: a short page with no flag must NOT be
		// trusted as the last page (solvent-streets-9auy).
		"short page with no flag keeps paging": {
			rawCount: 100, parsed: 100, newUnique: 100, want: pageContinue,
		},
		"repeated rows with no flag is a complete result": {
			rawCount: 100, parsed: 100, newUnique: 0, want: pageRepeatedRows,
		},
		"repeated rows while claiming more remain is truncation": {
			rawCount: 100, parsed: 100, newUnique: 0, exceeded: true, flagPresent: true, want: pageTruncated,
		},
		// All rows dropped by the null-geometry guard: no new ids, but that
		// says nothing about whether more rows remain.
		"all-null page keeps paging": {
			rawCount: 100, parsed: 0, newUnique: 0, want: pageContinue,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyArcGISPage(tc.rawCount, tc.parsed, tc.newUnique, tc.exceeded, tc.flagPresent)
			if got != tc.want {
				t.Errorf("classifyArcGISPage = %v, want %v", got, tc.want)
			}
		})
	}
}
