package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/httpio"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

const arcgisMaxRecords = 5000
const arcgisMaxPages = 200 // safety limit: 200 pages × 5000 = 1M features max

// Default Alameda County ArcGIS feature service URL
const defaultArcGISCenterlines = "https://services5.arcgis.com/ROBnTHSNjoZ2Wm1P/arcgis/rest/services/Street_Centerlines/FeatureServer/0/query"

type ArcGISSource struct {
	BBox         [4]float64 // [south, west, north, east]
	URL          string     // custom ArcGIS endpoint; empty uses default
	Progress     io.Writer  // pagination progress sink; nil discards
	AllowPrivate bool       // skip the SSRF guard; required for self-hosted staging endpoints on internal networks
}

var _ Source = (*ArcGISSource)(nil)

func (s *ArcGISSource) progress() io.Writer {
	if s.Progress == nil {
		return io.Discard
	}
	return s.Progress
}

func (s *ArcGISSource) Name() string { return "arcgis" }

func (s *ArcGISSource) Fetch(ctx context.Context, client *http.Client, rt resource.Source) ([]db.Feature, error) {
	// Only fetch centerlines for road type
	if rt.Type() != resource.TypeRoads {
		return []db.Feature{}, nil
	}

	endpoint := s.URL
	if endpoint == "" {
		endpoint = defaultArcGISCenterlines
	}

	if !s.AllowPrivate {
		if err := validatePublicHTTPURL(ctx, endpoint); err != nil {
			return nil, fmt.Errorf("arcgis endpoint: %w", err)
		}
	} else {
		// Carry the opt-out on the context so the client-level
		// CheckRedirect (which re-validates every redirect hop) also
		// permits private destinations for self-hosted endpoints. The
		// stdlib propagates this context onto each redirect request.
		ctx = WithAllowPrivate(ctx)
	}

	bbox := s.BBox
	envelope := fmt.Sprintf("%f,%f,%f,%f", bbox[1], bbox[0], bbox[3], bbox[2])

	var allFeatures []db.Feature
	// Deduplicate features by ID across pages. Esri offset paging can
	// re-return a boundary row when the server reorders rows, and a
	// resultOffset that lands mid-clamp can overlap the previous page; both
	// yield duplicate OBJECTIDs. Mirror overpass.go / water.go, which dedup
	// their cross-request results the same way. Pagination itself is still
	// driven off the RAW server row count (rawCount) below, never the deduped
	// slice, so dropping a duplicate here cannot stall the offset.
	seen := make(map[string]bool)
	offset := 0

	rtVal := rt.Type()
	for page := 0; ; page++ {
		if page >= arcgisMaxPages {
			return nil, fmt.Errorf("arcgis: exceeded %d pages (%d features), aborting", arcgisMaxPages, len(allFeatures))
		}
		features, rawCount, exceeded, flagPresent, err := fetchArcGISPage(ctx, client, endpoint, envelope, rtVal, offset)
		if err != nil {
			return nil, err
		}
		allFeatures = appendUniqueFeatures(allFeatures, seen, features)

		// Pagination is driven off rawCount (the number of rows the server
		// returned, before geometry filtering) and exceededTransferLimit, NOT
		// off the filtered feature slice. A page can return rows that are all
		// dropped by the null-geometry guard; keying off the filtered length
		// would wrongly terminate the loop AND stall the offset, so we must
		// use the raw row count here.
		//
		// The server clamps each response to its own maxRecordCount (often below
		// our requested arcgisMaxRecords), so a short page does NOT mean "last
		// page". Termination rules, in order:
		//   1. Empty page  -> definitively past the last record. Stop.
		//   2. Flag PRESENT and false -> the server authoritatively says no more
		//      rows remain. Stop (no wasted extra request).
		//   3. Flag absent -> ambiguous: the server may have clamped below our
		//      requested page size and silently dropped the rest. The old
		//      `!exceeded && rawCount < arcgisMaxRecords` short-page break trusted
		//      absence and once dropped 2000/5780 Livermore rows (solvent-streets-9auy),
		//      so we no longer trust it — keep paging until an empty page (rule 1).
		// arcgisMaxPages caps the loop (and errors loudly) so an offset-ignoring
		// server can't spin forever.
		if rawCount == 0 {
			break
		}
		if flagPresent && !exceeded {
			break
		}
		offset += rawCount
		fmt.Fprintf(s.progress(), "ArcGIS: fetched %d features so far, requesting next page at offset %d...\n", len(allFeatures), offset)
	}

	return allFeatures, nil
}

// appendUniqueFeatures appends each feature in src to dst, skipping any whose ID
// has already been seen. seen is mutated in place so it dedupes across pages.
// Mirrors the cross-request dedup in overpass.go / water.go.
func appendUniqueFeatures(dst []db.Feature, seen map[string]bool, src []db.Feature) []db.Feature {
	for _, f := range src {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		dst = append(dst, f)
	}
	return dst
}

// fetchArcGISPage fetches one page of features and reports the raw number of
// rows the server returned (before geometry filtering) and whether the server
// signalled that more rows remain (exceededTransferLimit).
func fetchArcGISPage(ctx context.Context, client *http.Client, endpoint, envelope string, resourceType resource.Type, offset int) ([]db.Feature, int, bool, bool, error) {
	params := url.Values{
		"where":             {"1=1"},
		"geometry":          {envelope},
		"geometryType":      {"esriGeometryEnvelope"},
		"inSR":              {"4326"},
		"outSR":             {"4326"},
		"outFields":         {"*"},
		"f":                 {"geojson"},
		"resultRecordCount": {strconv.Itoa(arcgisMaxRecords)},
		"resultOffset":      {strconv.Itoa(offset)},
		// Esri does not guarantee row order across resultOffset pages
		// without an explicit orderByFields; non-hosted (map-service /
		// enterprise) layers can otherwise reorder rows between pages,
		// silently skipping/duplicating features. Order by OBJECTID,
		// consistent with the OBJECTID assumption in parseArcGISGeoJSON's
		// id-parsing below. Behavior change: on the rare layer whose OID
		// field is not literally "OBJECTID" the server rejects this
		// orderBy — a loud failure (surfaced via arcgisErrorMessage)
		// rather than today's silent dup/skip, which is acceptable since
		// such layers already mis-parse ids. (solvent-streets-2a7n.26)
		"orderByFields": {"OBJECTID"},
	}

	// Merge the pagination/query params into any query string the endpoint
	// already carries (e.g. an arcgis_url with an appended ?token=...), rather
	// than blindly appending "?...", which would corrupt an existing query.
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("parse arcgis endpoint %q: %w", endpoint, err)
	}
	merged := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			merged.Set(k, v)
		}
	}
	u.RawQuery = merged.Encode()
	reqURL := u.String()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("create arcgis request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("arcgis request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := httpio.ReadAllLimit(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("read arcgis response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, 0, false, false, fmt.Errorf("arcgis %s returned %d: %s", endpoint, resp.StatusCode, truncate(string(body)))
	}

	// Parse the body exactly ONCE into a combined envelope that carries the
	// error, the features, and the exceededTransferLimit flag. The three
	// concerns (error detection, feature parsing, pagination flag) are then
	// derived from that single parse rather than re-unmarshalling the body
	// three times.
	page, err := parseArcGISPage(body)
	if err != nil {
		return nil, 0, false, false, err
	}

	// ArcGIS sometimes returns service-level errors as HTTP 200 with a JSON
	// error envelope (e.g. stale layer path, retired service). Detect those
	// up front so the caller sees the underlying message + endpoint instead
	// of an empty feature list.
	if msg, ok := arcgisErrorMessageFromPage(page); ok {
		return nil, 0, false, false, fmt.Errorf("arcgis %s: %s", endpoint, msg)
	}

	features, rawCount := featuresFromArcGISPage(page, resourceType, offset)
	exceeded, present := transferLimitFromPage(page)
	return features, rawCount, exceeded, present, nil
}

// arcgisPage is the single combined shape an ArcGIS page body is unmarshalled
// into. It carries every concern the caller needs — the error envelope, the
// GeoJSON features, and the exceededTransferLimit pagination flag (which
// f=geojson nests under top-level "properties", while some deployments emit it
// at the top level) — so fetchArcGISPage parses the body exactly once instead of
// three times. Pointer flag fields distinguish "present and false" from "absent"
// (critical for pagination; see the loop in Fetch).
type arcgisPage struct {
	Error *struct {
		Code    int      `json:"code"`
		Message string   `json:"message"`
		Details []string `json:"details"`
	} `json:"error"`
	Features []struct {
		Properties map[string]any  `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	} `json:"features"`
	ExceededTransferLimit *bool `json:"exceededTransferLimit"`
	Properties            struct {
		ExceededTransferLimit *bool `json:"exceededTransferLimit"`
	} `json:"properties"`
}

// parseArcGISPage unmarshals one ArcGIS page body into the combined envelope.
// It decodes with UseNumber so numeric property values (notably OBJECTID, the
// feature-id / dedupe key) keep full integer precision: plain json.Unmarshal
// into `any` decodes every number to float64, which silently loses precision for
// integers above 2^53 (e.g. an OBJECTID of 9007199254740993 would round to
// 9007199254740992 and collide with a neighbour). json.Number preserves the
// exact source text; formatArcGISValue renders it verbatim.
func parseArcGISPage(data []byte) (arcgisPage, error) {
	var page arcgisPage
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&page); err != nil {
		return arcgisPage{}, fmt.Errorf("parse arcgis json: %w", err)
	}
	return page, nil
}

// transferLimitFromPage reports whether the response signals that more rows
// remain beyond this page (exceeded) and whether the exceededTransferLimit flag
// was actually PRESENT (present). Absence is NOT authoritative for pagination
// (see the loop in Fetch), so a missing flag must not be treated as "no more
// rows".
func transferLimitFromPage(page arcgisPage) (exceeded, present bool) {
	if page.ExceededTransferLimit != nil {
		return *page.ExceededTransferLimit, true
	}
	if page.Properties.ExceededTransferLimit != nil {
		return *page.Properties.ExceededTransferLimit, true
	}
	return false, false
}

// arcgisErrorMessageFromPage reports whether page is an ArcGIS error envelope of
// the form {"error":{"code":N,"message":"..."}} and returns a human-readable
// summary. Returns ok=false for any non-error response (including valid GeoJSON
// FeatureCollections, which have no "error" key).
func arcgisErrorMessageFromPage(page arcgisPage) (string, bool) {
	if page.Error == nil {
		return "", false
	}
	msg := page.Error.Message
	if msg == "" {
		msg = "unknown error"
	}
	if len(page.Error.Details) > 0 && page.Error.Details[0] != msg {
		msg = fmt.Sprintf("%s (%s)", msg, page.Error.Details[0])
	}
	return fmt.Sprintf("code %d: %s", page.Error.Code, msg), true
}

// arcgisErrorMessage is the byte-level entry point retained for tests; it parses
// then delegates to arcgisErrorMessageFromPage. A parse failure returns ok=false.
func arcgisErrorMessage(body []byte) (string, bool) {
	page, err := parseArcGISPage(body)
	if err != nil {
		return "", false
	}
	return arcgisErrorMessageFromPage(page)
}

// parseArcGISGeoJSON is the byte-level entry point retained for tests; it parses
// then delegates to featuresFromArcGISPage.
func parseArcGISGeoJSON(data []byte, resourceType resource.Type, baseIndex int) ([]db.Feature, int, error) {
	page, err := parseArcGISPage(data)
	if err != nil {
		return nil, 0, err
	}
	features, rawCount := featuresFromArcGISPage(page, resourceType, baseIndex)
	return features, rawCount, nil
}

// featuresFromArcGISPage builds features from an already-parsed page, skipping
// rows with no usable geometry (missing or explicit JSON null). It also returns
// the raw row count — the number of features in the server response before
// geometry filtering — which the pagination loop uses to advance the offset and
// decide termination (a page may return rows that are all skipped here).
func featuresFromArcGISPage(resp arcgisPage, resourceType resource.Type, baseIndex int) ([]db.Feature, int) {
	var features []db.Feature
	for i, f := range resp.Features {
		// Skip rows with no usable geometry. A missing "geometry" field
		// unmarshals to a nil RawMessage; an explicit "geometry": null
		// unmarshals to a non-nil 4-byte RawMessage("null"), so guard both.
		if f.Geometry == nil || bytes.Equal(bytes.TrimSpace(f.Geometry), []byte("null")) {
			continue
		}

		tags := make(map[string]string)
		var name string
		for k, v := range f.Properties {
			if v != nil {
				tags[k] = formatArcGISValue(v)
			}
			if v != nil && (k == "FULLNAME" || k == "FullName" || k == "fullname") {
				name = formatArcGISValue(v)
			}
		}

		id := fmt.Sprintf("arcgis:%d", baseIndex+i)
		if oid, ok := f.Properties["OBJECTID"]; ok {
			id = "arcgis:" + formatArcGISValue(oid)
		}

		features = append(features, db.Feature{
			ID:           id,
			ResourceType: resourceType,
			Name:         name,
			Tags:         tags,
			GeometryJSON: string(f.Geometry),
			SourceAPI:    "arcgis",
			FetchedAt:    time.Now(),
		})
	}

	return features, len(resp.Features)
}

// formatArcGISValue renders a decoded JSON property value as a string. With the
// UseNumber decoder every JSON number arrives as json.Number, whose String()
// returns the exact source text — plain decimal, no scientific notation, and no
// float64 precision loss for large integers (the OBJECTID / dedupe-key case).
// A float64 branch is kept for any value that reaches here already decoded as a
// float (defensive; renders plain decimal via FormatFloat -1). Everything else
// falls through to %v, matching the prior behavior.
func formatArcGISValue(v any) string {
	switch n := v.(type) {
	case json.Number:
		return n.String()
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}
