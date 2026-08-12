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
		allFeatures = append(allFeatures, features...)

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
		return nil, 0, false, false, fmt.Errorf("arcgis %s returned %d: %s", endpoint, resp.StatusCode, truncate(string(body), 200))
	}

	// ArcGIS sometimes returns service-level errors as HTTP 200 with a JSON
	// error envelope (e.g. stale layer path, retired service). Detect those
	// up front so the caller sees the underlying message + endpoint instead
	// of an empty feature list.
	if msg, ok := arcgisErrorMessage(body); ok {
		return nil, 0, false, false, fmt.Errorf("arcgis %s: %s", endpoint, msg)
	}

	features, rawCount, err := parseArcGISGeoJSON(body, resourceType, offset)
	if err != nil {
		return nil, 0, false, false, err
	}
	exceeded, present := arcgisTransferLimit(body)
	return features, rawCount, exceeded, present, nil
}

// arcgisTransferLimit reports whether the response signals that more rows remain
// beyond this page (exceeded) and whether the exceededTransferLimit flag was
// actually PRESENT in the body (present). f=geojson responses carry the flag
// under "properties"; some ArcGIS deployments emit it at the top level. Pointer
// fields distinguish "flag present and false" from "flag absent" — a critical
// distinction for pagination: absence is NOT authoritative (see the loop in
// Fetch), so we must not treat a missing flag as "no more rows". A parse failure
// here is reported as absent/false — the caller's parseArcGISGeoJSON surfaces
// malformed bodies as real errors.
func arcgisTransferLimit(body []byte) (exceeded, present bool) {
	var env struct {
		ExceededTransferLimit *bool `json:"exceededTransferLimit"`
		Properties            struct {
			ExceededTransferLimit *bool `json:"exceededTransferLimit"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return false, false
	}
	if env.ExceededTransferLimit != nil {
		return *env.ExceededTransferLimit, true
	}
	if env.Properties.ExceededTransferLimit != nil {
		return *env.Properties.ExceededTransferLimit, true
	}
	return false, false
}

// arcgisErrorMessage reports whether body is an ArcGIS error envelope of the
// form {"error":{"code":N,"message":"..."}} and returns a human-readable
// summary. Returns ok=false for any non-error response (including valid
// GeoJSON FeatureCollections, which have no "error" key).
func arcgisErrorMessage(body []byte) (string, bool) {
	var env struct {
		Error *struct {
			Code    int      `json:"code"`
			Message string   `json:"message"`
			Details []string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Error == nil {
		return "", false
	}
	msg := env.Error.Message
	if msg == "" {
		msg = "unknown error"
	}
	if len(env.Error.Details) > 0 && env.Error.Details[0] != msg {
		msg = fmt.Sprintf("%s (%s)", msg, env.Error.Details[0])
	}
	return fmt.Sprintf("code %d: %s", env.Error.Code, msg), true
}

type arcgisGeoJSON struct {
	Features []struct {
		Properties map[string]any  `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	} `json:"features"`
}

// parseArcGISGeoJSON parses a GeoJSON page into features, skipping rows with no
// usable geometry (missing or explicit JSON null). It also returns the raw row
// count — the number of features in the server response before geometry
// filtering — which the pagination loop uses to advance the offset and decide
// termination (a page may return rows that are all skipped here).
func parseArcGISGeoJSON(data []byte, resourceType resource.Type, baseIndex int) ([]db.Feature, int, error) {
	var resp arcgisGeoJSON
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, 0, fmt.Errorf("parse arcgis json: %w", err)
	}

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

	return features, len(resp.Features), nil
}

// formatArcGISValue renders a decoded JSON property value as a string. JSON
// numbers decode into float64, so fmt's %v would render large integral values
// in scientific notation (e.g. 1234567 → "1.234567e+06"); FormatFloat with
// precision -1 yields plain decimal ("1234567"). Non-float values fall through
// to %v, matching the prior behavior.
func formatArcGISValue(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}
