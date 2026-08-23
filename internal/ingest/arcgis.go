package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/httpio"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// arcgisMaxRecords is the resultRecordCount we request per page. A var, not a
// const, so tests can shrink it instead of standing up a server that has to
// serve 5000+ rows to exercise the "server returned more than we asked for"
// branch in repeatedPageOutcome (mirrors arcgisMaxPages, maxResponseBodyBytes).
var arcgisMaxRecords = 5000

// arcgisMaxPages caps the pagination loop. The ceiling is 200 × the SERVER's
// own maxRecordCount, not our requested arcgisMaxRecords — Esri clamps every
// response to its own limit (commonly 1000 or 2000), so the real cap is nearer
// 200k features than 1M. A var, not a const, so tests can shrink it instead of
// standing up a server that has to answer 200 requests (mirrors
// maxResponseBodyBytes).
var arcgisMaxPages = 200

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
paging:
	for page := 0; ; page++ {
		if page >= arcgisMaxPages {
			// Hard error, not a partial return. A returned partial counts as
			// SUCCESS in fetchFromSources, and pkg/cmd/ingest sets
			// replaceSources only when a source FAILED — so whenever every
			// configured source succeeds it stays nil, and UpsertFeatures
			// DELETEs every stored row for the resource before inserting the
			// truncated set. That holds for a city running overpass and arcgis
			// together, not just an arcgis-only one. Silently trading a
			// complete previous ingest for a truncated one is worse than
			// failing: the error path preserves what is already in the
			// database.
			//
			// The offset-ignoring server this cap used to fire on is handled
			// below by the no-new-ids rule, so reaching the cap now means
			// something genuinely pathological.
			return nil, fmt.Errorf("arcgis: exceeded %d pages (%d features), aborting", arcgisMaxPages, len(allFeatures))
		}
		features, rawCount, exceeded, flagPresent, err := fetchArcGISPage(ctx, client, endpoint, envelope, rtVal, offset)
		if err != nil {
			return nil, err
		}
		before := len(allFeatures)
		allFeatures = appendUniqueFeatures(allFeatures, seen, features)
		newUnique := len(allFeatures) - before

		switch classifyArcGISPage(rawCount, len(features), newUnique, exceeded, flagPresent) {
		case pageDone:
			break paging
		case pageRepeatedRows:
			if err := s.confirmRepeatedPageIsComplete(ctx, client, endpoint, offset, rawCount, len(allFeatures)); err != nil {
				return nil, err
			}
			break paging
		case pageTruncated:
			return nil, fmt.Errorf(
				"arcgis: server re-served rows already fetched at offset %d while reporting "+
					"more rows remain (%d features); refusing to store a truncated result",
				offset, len(allFeatures))
		case pageContinue:
		}

		offset += rawCount
		fmt.Fprintf(s.progress(), "ArcGIS: fetched %d features so far, requesting next page at offset %d...\n", len(allFeatures), offset)
	}

	return allFeatures, nil
}

// pageOutcome is what the pagination loop should do after fetching one page.
type pageOutcome int

const (
	pageContinue     pageOutcome = iota // more rows may remain; request the next offset
	pageDone                            // no more rows; stop and return what we have
	pageRepeatedRows                    // the server re-served rows we hold; stop, this is the full result
	pageTruncated                       // the server withheld rows it says exist; fail rather than store a partial
)

// classifyArcGISPage decides how pagination should proceed after one page.
//
// Pagination is driven off rawCount (the number of rows the server returned,
// BEFORE geometry filtering) and exceededTransferLimit, never off the filtered
// feature slice. A page can return rows that are all dropped by the
// null-geometry guard; keying off the filtered length would wrongly terminate
// the loop AND stall the offset.
//
// The server clamps each response to its own maxRecordCount (often well below
// our requested arcgisMaxRecords), so a short page does NOT mean "last page".
// The rules, in order:
//
//  1. Empty page -> definitively past the last record.
//
//  2. Flag PRESENT and false -> the server authoritatively says no rows remain.
//     Stop without spending another request.
//
//  3. A page that parsed rows but added no NEW ids -> the server re-served what
//     we already hold, the signature of a layer that ignores resultOffset
//     (pre-10.3, or supportsPagination:false). That alone does NOT mean we have
//     everything: such a layer may equally have clamped to its own
//     maxRecordCount and withheld the rest. This function reports
//     pageRepeatedRows and the caller disambiguates via repeatedPageOutcome,
//     which needs the layer metadata this pure function has no way to fetch
//     (solvent-streets-ofbo).
//
//     parsed > 0 is load-bearing: a page whose rows were ALL dropped by the
//     null-geometry guard also adds no new ids, but says nothing about whether
//     more rows remain, so it must keep paging.
//
//     If the flag says rows DO remain, the same shape means the server is
//     withholding them — truncation, not completion. That must fail: a partial
//     counts as success in fetchFromSources, and pkg/cmd/ingest leaves
//     replaceSources nil whenever every configured source succeeded, so
//     UpsertFeatures DELETEs every stored row for the resource before inserting
//     the truncated set. Erroring preserves what is already in the database.
//
//     Neither arm catches an offset-ignoring layer with NO OBJECTID — ids there
//     are synthesized from the offset, so repeats look new. That case runs to
//     arcgisMaxPages and errors.
//
//  4. Otherwise keep paging. In particular a flag-absent short page is
//     ambiguous, and the old `!exceeded && rawCount < arcgisMaxRecords` break
//     trusted that absence and once dropped 2000 of 5780 Livermore rows
//     (solvent-streets-9auy). We no longer trust it.
func classifyArcGISPage(rawCount, parsed, newUnique int, exceeded, flagPresent bool) pageOutcome {
	if rawCount == 0 {
		return pageDone
	}
	if flagPresent && !exceeded {
		return pageDone
	}
	if parsed > 0 && newUnique == 0 {
		if flagPresent && exceeded {
			return pageTruncated
		}
		return pageRepeatedRows
	}
	return pageContinue
}

// arcgisLayerMeta is the subset of a layer's own metadata document
// (<layer>?f=json) that pagination needs. MaxRecordCount is a pointer so
// "the server omitted the field" is distinguishable from "the server said 0".
type arcgisLayerMeta struct {
	Error          *arcgisErrorEnvelope `json:"error"`
	MaxRecordCount *int                 `json:"maxRecordCount"`
}

// errNoLayerMetadata reports that the layer's maxRecordCount could not be
// established. It is never a partial success: repeatedPageOutcome treats it as
// truncation, because an unproven result must not overwrite stored rows.
var errNoLayerMetadata = errors.New("layer metadata unavailable")

// arcgisLayerMaxRecordCount fetches the layer's own maxRecordCount — the cap
// ArcGIS clamps every response to, independent of our requested
// resultRecordCount.
//
// Called LAZILY, only when pagination hits a repeated page, so a healthy ingest
// never pays for this request. The repeated-page arm breaks out of the loop
// immediately, so it fires at most once per Fetch.
//
// The layer document lives at the query endpoint minus its /query suffix. Any
// query string the endpoint carries (an arcgis_url with ?token=...) is
// preserved, exactly as fetchArcGISPage does. No fresh validatePublicHTTPURL
// call: this is the same scheme and host Fetch already validated, and the
// client-level CheckRedirect re-validates every redirect hop.
func arcgisLayerMaxRecordCount(ctx context.Context, client *http.Client, endpoint string) (int, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return 0, fmt.Errorf("%w: parse endpoint %q: %w", errNoLayerMetadata, endpoint, err)
	}
	trimmed := strings.TrimSuffix(u.Path, "/")
	if !strings.EqualFold(path.Base(trimmed), "query") {
		return 0, fmt.Errorf("%w: endpoint path %q does not end in /query", errNoLayerMetadata, u.Path)
	}
	u.Path = path.Dir(trimmed)

	q := u.Query()
	q.Set("f", "json")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("%w: create request: %w", errNoLayerMetadata, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errNoLayerMetadata, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := httpio.ReadAllLimit(resp.Body, maxResponseBodyBytes)
	if err != nil {
		return 0, fmt.Errorf("%w: read response: %w", errNoLayerMetadata, err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w: %s returned %d: %s", errNoLayerMetadata, u.String(), resp.StatusCode, truncate(string(body)))
	}

	var meta arcgisLayerMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return 0, fmt.Errorf("%w: parse json: %w", errNoLayerMetadata, err)
	}
	// ArcGIS returns service-level errors as HTTP 200 with an error envelope.
	if msg, ok := arcgisErrorMessageFromEnvelope(meta.Error); ok {
		return 0, fmt.Errorf("%w: %s", errNoLayerMetadata, msg)
	}
	if meta.MaxRecordCount == nil || *meta.MaxRecordCount <= 0 {
		return 0, fmt.Errorf("%w: %s reported no usable maxRecordCount", errNoLayerMetadata, u.String())
	}
	return *meta.MaxRecordCount, nil
}

// confirmRepeatedPageIsComplete decides whether a repeated page may be returned
// as the layer's full result, and reports an error when it may not.
//
// A repeated page is ambiguous on its own — it is equally the signature of a
// complete single-response layer and of one that clamped and withheld the rest.
// The layer's own maxRecordCount tells them apart (solvent-streets-ofbo).
// Fetching it is LAZY: this is the only path that needs it, so a healthy ingest
// never spends the extra request.
func (s *ArcGISSource) confirmRepeatedPageIsComplete(ctx context.Context, client *http.Client,
	endpoint string, offset, rawCount, fetched int) error {
	maxRC, metaErr := arcgisLayerMaxRecordCount(ctx, client, endpoint)
	if repeatedPageOutcome(rawCount, maxRC, metaErr) == pageTruncated {
		if metaErr != nil {
			return fmt.Errorf(
				"arcgis: server at offset %d re-served rows already fetched (%d features) and "+
					"completeness could not be verified: %w; refusing to store a possibly truncated result",
				offset, fetched, metaErr)
		}
		return fmt.Errorf(
			"arcgis: server at offset %d re-served rows already fetched while returning a full "+
				"page (%d rows = layer maxRecordCount %d, %d features); it ignores resultOffset and "+
				"clamped the result, so rows remain unfetched; refusing to store a truncated result",
			offset, rawCount, maxRC, fetched)
	}
	fmt.Fprintf(s.progress(),
		"ArcGIS: page at offset %d returned only rows already fetched (%d rows, under the layer's "+
			"maxRecordCount of %d); the server ignores resultOffset but answered completely — "+
			"stopping with %d features\n",
		offset, rawCount, maxRC, fetched)
	return nil
}

// repeatedPageOutcome decides what a repeated page actually means, once the
// layer's maxRecordCount is known.
//
// classifyArcGISPage cannot make this call on its own: `parsed > 0 &&
// newUnique == 0` with no exceededTransferLimit is produced by TWO different
// servers, and treating them alike is solvent-streets-ofbo.
//
//   - (a) An offset-ignoring server whose layer genuinely fits in one response.
//     Everything is in hand; stopping is correct.
//   - (b) A pre-10.3 / supportsPagination:false layer that clamped to its own
//     maxRecordCount AND omitted the flag. Page 2 repeats page 1, so it looks
//     identical to (a) — but the result is PARTIAL.
//
// rawCount is the disambiguator because it is the RAW server row count, taken
// before the null-geometry filter, so it is directly comparable to the server's
// own cap. (A returnCountOnly probe is not: len(seen) is post-filter, so any
// layer with null geometries would look truncated.)
//
// Returning a partial as success is destructive, not merely lossy: a truncated
// fetch that reports no error leaves replaceSources nil in
// pkg/cmd/ingest, so UpsertFeatures DELETEs every stored row for the resource
// before inserting the short set. Failing preserves what is already stored, so
// anything we cannot PROVE complete fails.
//
// Below our own cap the comparison is against min(maxRecordCount,
// arcgisMaxRecords), NOT maxRecordCount alone: a response is clamped by
// whichever limit is lower, and on a layer advertising more than we ask for it
// is OURS that binds. Comparing against maxRecordCount alone let a full 5000-row
// repeated page from a layer with maxRecordCount 10000 read as "complete" —
// reintroducing, for exactly those layers, the silent truncation this function
// exists to prevent.
//
// ABOVE our own cap that reasoning inverts, and generalizing the min() across
// the whole range was solvent-streets-szq9. rawCount > arcgisMaxRecords means
// the server returned more rows than we asked for, which PROVES it never
// applied our resultRecordCount — and a layer that ignores resultRecordCount
// ignores resultOffset too (supportsPagination:false / pre-10.3), which is why
// its pages repeat. Our cap therefore cannot be the binding limit; only the
// server's can. Comparing against it anyway reported pageTruncated for a layer
// we had read completely, and Fetch refused to store it: a layer with
// maxRecordCount 10000 holding 6000 rows served all 6000 in one page, repeated
// them on page 2, and then hard-errored. Fails safe (stored rows survive), but
// it is a false refusal on data we hold in full.
//
// Known, accepted false positive: a layer holding exactly the effective cap in
// rows, on an offset-ignoring server, errors despite being complete. At exactly
// our cap the response IS ambiguous — a clamp and a complete answer look
// identical — so it must still fail closed. It fails safe, and a re-run against
// a corrected endpoint is the remedy.
func repeatedPageOutcome(rawCount, maxRecordCount int, metaErr error) pageOutcome {
	if metaErr != nil || maxRecordCount <= 0 {
		return pageTruncated
	}
	// Strictly greater, not >=: at exactly our cap the page may have been
	// clamped BY that cap, so only the server's limit can settle it here.
	if rawCount > arcgisMaxRecords {
		if rawCount >= maxRecordCount {
			return pageTruncated
		}
		return pageRepeatedRows
	}
	if rawCount >= min(maxRecordCount, arcgisMaxRecords) {
		return pageTruncated
	}
	return pageRepeatedRows
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

// arcgisErrorEnvelope is the {"error":{...}} object ArcGIS returns as an HTTP
// 200 body for service-level failures (stale layer path, retired service, bad
// orderByFields). Shared by arcgisPage and arcgisLayerMeta so both request
// shapes detect it the same way.
type arcgisErrorEnvelope struct {
	Code    int      `json:"code"`
	Message string   `json:"message"`
	Details []string `json:"details"`
}

// arcgisPage is the single combined shape an ArcGIS page body is unmarshalled
// into. It carries every concern the caller needs — the error envelope, the
// GeoJSON features, and the exceededTransferLimit pagination flag (which
// f=geojson nests under top-level "properties", while some deployments emit it
// at the top level) — so fetchArcGISPage parses the body exactly once instead of
// three times. Pointer flag fields distinguish "present and false" from "absent"
// (critical for pagination; see the loop in Fetch).
type arcgisPage struct {
	Error    *arcgisErrorEnvelope `json:"error"`
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
	return arcgisErrorMessageFromEnvelope(page.Error)
}

// arcgisErrorMessageFromEnvelope formats an error envelope, if there is one.
// Split out so the layer-metadata request (which is not a page) reuses exactly
// the same detection and wording.
func arcgisErrorMessageFromEnvelope(e *arcgisErrorEnvelope) (string, bool) {
	if e == nil {
		return "", false
	}
	msg := e.Message
	if msg == "" {
		msg = "unknown error"
	}
	if len(e.Details) > 0 && e.Details[0] != msg {
		msg = fmt.Sprintf("%s (%s)", msg, e.Details[0])
	}
	return fmt.Sprintf("code %d: %s", e.Code, msg), true
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
