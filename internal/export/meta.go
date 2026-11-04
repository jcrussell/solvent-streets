package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jcrussell/solvent-streets/internal/build"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/game"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// CityInfo holds city metadata for the frontend city switcher.
type CityInfo struct {
	Slug      string     `json:"slug"`
	Name      string     `json:"name"`
	BBox      [4]float64 `json:"bbox"`
	CenterLon float64    `json:"center_lon"`
	CenterLat float64    `json:"center_lat"`
	// Tags are optional grouping labels (e.g. "Bay Area", "Top 50"). Omitted
	// from JSON when empty. Used by the template to build city-selector
	// optgroups (a city appears under each of its tags) and by app.js to scope
	// compare/aggregate to a tagged subset.
	Tags []string `json:"tags,omitempty"`
}

// CityGroup is one tag value with the cities that carry it. Tag is the full
// tag string as it appears in config ("Bay Area", "state:CA") and is what the
// client filters on; Label is its display form without the dimension prefix
// ("Bay Area", "CA"). Used by the template to render the city selector as
// <optgroup>s and each dimension's filter as <option>s. A group with an empty
// Tag holds the untagged ("Other") cities and is rendered as bare options; it
// only ever appears in the default dimension. A city with multiple tags in one
// dimension appears in multiple groups.
type CityGroup struct {
	Tag    string
	Label  string
	Cities []CityInfo
}

// TagDimension is one axis of tags ("group", "state", "metro") with its
// cities grouped by value. Name is the config.ParseTag dimension; Label is the
// selector heading ("Group", "State"). Groups follow GroupCitiesByTag's order.
type TagDimension struct {
	Name   string
	Label  string
	Groups []CityGroup
}

// Values returns the dimension's groups minus the untagged bucket: the
// options a filter for this dimension offers.
func (d TagDimension) Values() []CityGroup {
	out := make([]CityGroup, 0, len(d.Groups))
	for _, g := range d.Groups {
		if g.Tag != "" {
			out = append(out, g)
		}
	}
	return out
}

// TemplateData wraps MetaJSON with the forecast seed for the interactive controls.
type TemplateData struct {
	MetaJSON
	ForecastSeed template.JS
	LayerColors  template.JS // JSON map of resource type → color
	RawTOML      string      // original pvmt.toml contents
	ResolvedTOML string      // config with all defaults filled in
	UnitSystem   string      // "metric" or "imperial"
	Cities       []CityInfo
	// TagDimensions is the same cities as Cities, grouped per tag dimension
	// (see GroupCitiesByDimension): the default "group" dimension first, whose
	// groups drive the city selector's <optgroup>s via CityGroups, then the
	// other dimensions in name order; every dimension renders as one filter.
	// Built alongside Cities; the flat Cities slice is kept for the CITIES JS
	// array and cities.json.
	TagDimensions []TagDimension
	// ActiveSlug is the slug of the city this page was rendered against, set
	// only in multi-city mode (empty single-city). The game page (/play) renders
	// per-city and uses it to emit DATA_PREFIX ('cities/<slug>/') and pre-select
	// the city dropdown; the index ignores it (it switches cities client-side).
	ActiveSlug      string
	WasmPrefix      string // path prefix for WASM assets (e.g. "../"); empty = same directory
	MethodologyHTML template.HTML
	// IsLiveServer is true when rendered by pvmt serve; false for static
	// export. Gates server-only UI (e.g. the snapshot picker) that depends
	// on live /api endpoints absent from the static output.
	IsLiveServer bool
	// GeneratedDate is the YYYY-MM-DD date this page was rendered.
	GeneratedDate string
	// BuildVersion is "<version> (commit <hash>)", the same string
	// pvmt --version reports (build.Current().Short()).
	BuildVersion string
}

// CityGroups returns the default-dimension groups the city selector renders
// as <optgroup>s (non-empty tags first, untagged "Other" last), or nil when
// there are no dimensions. A method rather than an `index` in the template so
// a single-city page with a nil TagDimensions cannot panic the render.
func (d TemplateData) CityGroups() []CityGroup {
	if len(d.TagDimensions) == 0 {
		return nil
	}
	return d.TagDimensions[0].Groups
}

// HasTagFilters reports whether any dimension offers a real choice (two or
// more values). The templates omit the filter row entirely when it is false —
// a single-value dimension cannot narrow anything.
func (d TemplateData) HasTagFilters() bool {
	for _, dim := range d.TagDimensions {
		if len(dim.Values()) >= 2 {
			return true
		}
	}
	return false
}

// BandCount exposes game.BandCount to the game template, which checks its
// BAND_COLORS palette against it at boot. A method (not a field) so every
// TemplateData construction site — static export and live server — gets it
// without remembering to set it.
func (TemplateData) BandCount() int { return game.BandCount }

// FooterInfo returns the values shown in the page footer: the generation
// date (YYYY-MM-DD) and the build version string, which matches
// `pvmt --version` (build.Current().Short()).
func FooterInfo() (generatedDate, buildVersion string) {
	return time.Now().Format("2006-01-02"), build.Current().Short()
}

// resourceColorsJSOnce lazily marshals ResourceColors. Lazy so a binary
// like `pvmt --version` that never touches the exporter doesn't pay
// the marshal cost at import time. ResourceColors is a constant
// map[string]string so the marshal cannot realistically fail; the
// closure swallows the error for the same reason fmt.Sprintf does not
// — the inputs are statically known.
var resourceColorsJSOnce = sync.OnceValue(func() template.JS {
	data, _ := json.Marshal(ResourceColors)
	return template.JS(data)
})

// ResourceColorsJS returns ResourceColors as a template.JS JSON object.
func ResourceColorsJS() template.JS { return resourceColorsJSOnce() }

// jsCityInfo mirrors the fields the index page's CITIES array historically
// carried (note lon/lat, not the center_lon/center_lat that CityInfo and
// cities.json use). Kept as a distinct shape so app.js's CITIES[i] access is
// byte-for-byte the same contract it had when the array was inlined.
type jsCityInfo struct {
	Slug string     `json:"slug"`
	Name string     `json:"name"`
	BBox [4]float64 `json:"bbox"`
	Lon  float64    `json:"lon"`
	Lat  float64    `json:"lat"`
	// Tags let app.js filter the flat CITIES array for the compare/aggregate
	// tag scope. Omitted for untagged cities (omitempty) — app.js guards the
	// access as (city.tags || []).
	Tags []string `json:"tags,omitempty"`
}

// jsTagDimension / jsTagValue are the PVMT_CONFIG.tagDimensions payload both
// pages read to drive their tag filters and to rebuild the city selector
// client-side: one entry per dimension, values in selector order, each value
// carrying the full tag string (what a city's tags contain) and its label.
type jsTagDimension struct {
	Name   string       `json:"name"`
	Label  string       `json:"label"`
	Values []jsTagValue `json:"values"`
}

type jsTagValue struct {
	Tag   string `json:"tag"`
	Label string `json:"label"`
}

// jsTagDimensions flattens TagDimensions for the config payload, dropping the
// untagged group (it is not a filter value). Never nil, so the client can read
// `.tagDimensions.length` without a guard.
func jsTagDimensions(dims []TagDimension) []jsTagDimension {
	out := make([]jsTagDimension, 0, len(dims))
	for _, d := range dims {
		vals := make([]jsTagValue, 0, len(d.Groups))
		for _, g := range d.Groups {
			if g.Tag == "" {
				continue
			}
			vals = append(vals, jsTagValue{Tag: g.Tag, Label: g.Label})
		}
		out = append(out, jsTagDimension{Name: d.Name, Label: d.Label, Values: vals})
	}
	return out
}

// jsCities maps the flat CityInfo list to the CITIES array shape.
func jsCities(cities []CityInfo) []jsCityInfo {
	out := make([]jsCityInfo, len(cities))
	for i, c := range cities {
		out[i] = jsCityInfo{Slug: c.Slug, Name: c.Name, BBox: c.BBox, Lon: c.CenterLon, Lat: c.CenterLat, Tags: c.Tags}
	}
	return out
}

// rawOrNull adapts a template.JS holding a pre-marshaled JSON fragment (e.g.
// LayerColors, ForecastSeed) into a json.RawMessage for nesting inside the
// PVMT_CONFIG object. An empty fragment becomes `null` so the enclosing
// json.Marshal cannot fail on invalid JSON — a zero TemplateData (rendered by
// the a11y tests) leaves these fields empty.
func rawOrNull(js template.JS) json.RawMessage {
	if js == "" {
		return json.RawMessage("null")
	}
	return json.RawMessage(js)
}

// IndexConfigJSON is the window.PVMT_CONFIG payload for index.html, consumed by
// templates/app.js. It is a method (like BandCount) so both the static exporter
// and the live server emit it without any change to how they build TemplateData.
// json.Marshal's default HTML escaping renders a city name containing
// "</script>" as an escaped fragment, so the template.JS blob is safe inside an
// inline <script> — the same guarantee LayerColors/ForecastSeed already rely on.
func (d TemplateData) IndexConfigJSON() template.JS {
	cfg := struct {
		Cities        []jsCityInfo     `json:"cities"`
		TagDimensions []jsTagDimension `json:"tagDimensions"`
		Center        [2]float64       `json:"center"`
		LayerColors   json.RawMessage  `json:"layerColors"`
		UnitSystem    string           `json:"unitSystem"`
		ForecastSeed  json.RawMessage  `json:"forecastSeed"`
		WasmPrefix    string           `json:"wasmPrefix"`
	}{
		Cities:        jsCities(d.Cities),
		TagDimensions: jsTagDimensions(d.TagDimensions),
		Center:        [2]float64{d.CenterLon, d.CenterLat},
		LayerColors:   rawOrNull(d.LayerColors),
		UnitSystem:    d.UnitSystem,
		ForecastSeed:  rawOrNull(d.ForecastSeed),
		WasmPrefix:    d.WasmPrefix,
	}
	data, _ := json.Marshal(cfg)
	return template.JS(data)
}

// GameConfigJSON is the window.PVMT_CONFIG payload for play.html, consumed by
// templates/game.js. Like IndexConfigJSON it is a method so neither construction
// site changes. DataPrefix/MapHref reproduce the {{if}} branches the template
// used to inline (empty ActiveSlug → client-side resolution; static host → a
// relative back-to-map link). Cities/TagDimensions carry the same payload the
// index page gets so the play page's tag filters can rebuild its city selector
// client-side from the same data.
func (d TemplateData) GameConfigJSON() template.JS {
	dataPrefix := ""
	if d.ActiveSlug != "" {
		dataPrefix = "cities/" + d.ActiveSlug + "/"
	}
	mapHref := "index.html"
	if d.IsLiveServer {
		mapHref = "/"
	}
	cfg := struct {
		Cities        []jsCityInfo     `json:"cities"`
		TagDimensions []jsTagDimension `json:"tagDimensions"`
		Center        [2]float64       `json:"center"`
		ForecastSeed  json.RawMessage  `json:"forecastSeed"`
		DataPrefix    string           `json:"dataPrefix"`
		MapHref       string           `json:"mapHref"`
		BandCount     int              `json:"bandCount"`
		WasmPrefix    string           `json:"wasmPrefix"`
	}{
		Cities:        jsCities(d.Cities),
		TagDimensions: jsTagDimensions(d.TagDimensions),
		Center:        [2]float64{d.CenterLon, d.CenterLat},
		ForecastSeed:  rawOrNull(d.ForecastSeed),
		DataPrefix:    dataPrefix,
		MapHref:       mapHref,
		BandCount:     d.BandCount(),
		WasmPrefix:    d.WasmPrefix,
	}
	data, _ := json.Marshal(cfg)
	return template.JS(data)
}

type MetaJSON struct {
	ProjectName  string     `json:"project_name"`
	BBox         [4]float64 `json:"bbox"`
	CenterLon    float64    `json:"center_lon"`
	CenterLat    float64    `json:"center_lat"`
	SnapshotDate string     `json:"snapshot_date"`
	Stats        []StatJSON `json:"stats"`
	CityArea     float64    `json:"city_area,omitempty"`
	TotalPaved   float64    `json:"total_paved,omitempty"`
	PctPaved     float64    `json:"pct_paved,omitempty"`
}

type StatJSON struct {
	Type         string  `json:"type"`
	Color        string  `json:"color"`
	TotalArea    float64 `json:"total_area"`
	FeatureCount int     `json:"feature_count"`
	// EstimatedSpaces is the parking resource's estimated vehicle capacity
	// (OSM capacity tags, else footprint area over a per-space constant; see
	// resource.EstimateParkingSpaces). Omitted for other resources and for
	// cities computed before the estimate existed.
	EstimatedSpaces int `json:"estimated_spaces,omitempty"`
}

// ResourceColors maps resource type names to their display colors.
var ResourceColors = map[string]string{
	"roads":     "#6b7280",
	"parking":   "#3b82f6",
	"sidewalks": "#f59e0b",
}

// GroupCitiesByDimension groups cities per tag dimension for the selectors.
// The default dimension (config.DefaultTagDimension) always comes first — even
// when no city carries a plain tag, so the city selector's <optgroup> source is
// always index 0 and untagged cities still render as bare options — followed by
// the other dimensions sorted by name. Within a dimension, groups follow
// GroupCitiesByTag's order. The untagged ("Other") group appears only in the
// default dimension: a city lacking a tag in some other dimension is simply not
// a member of that dimension. The input slice is not mutated. Returns nil when
// cities is empty.
func GroupCitiesByDimension(cities []CityInfo) []TagDimension {
	if len(cities) == 0 {
		return nil
	}
	seen := map[string]bool{config.DefaultTagDimension: true}
	for _, c := range cities {
		for _, t := range c.Tags {
			dim, _ := config.ParseTag(t)
			seen[dim] = true
		}
	}
	names := make([]string, 0, len(seen))
	for d := range seen {
		if d != config.DefaultTagDimension {
			names = append(names, d)
		}
	}
	sort.Strings(names)
	names = append([]string{config.DefaultTagDimension}, names...)

	dims := make([]TagDimension, 0, len(names))
	for _, name := range names {
		dims = append(dims, TagDimension{
			Name:   name,
			Label:  dimensionLabel(name),
			Groups: groupCitiesInDimension(cities, name),
		})
	}
	return dims
}

// dimensionLabel is the selector heading for a dimension name: the name with
// its first letter upper-cased ("group" → "Group", "state" → "State").
func dimensionLabel(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// GroupCitiesByTag groups cities by their DEFAULT-dimension tags for the city
// selector: non-empty tags first, sorted ascending by label, with cities sorted
// ascending by name within each group. A city with multiple tags appears in
// every matching group (multi-membership). Cities with no default-dimension tag
// are collected into a final group (Tag == "") rendered as ungrouped ("Other")
// options. Tags in other dimensions ("state:CA") are ignored here; see
// GroupCitiesByDimension for the per-dimension view. The input slice is not
// mutated. Returns nil when cities is empty.
func GroupCitiesByTag(cities []CityInfo) []CityGroup {
	if len(cities) == 0 {
		return nil
	}
	return groupCitiesInDimension(cities, config.DefaultTagDimension)
}

// groupCitiesInDimension groups cities by their tags in one dimension. Only
// the default dimension collects an untagged group (last); for any other
// dimension a city with no tag there is omitted.
func groupCitiesInDimension(cities []CityInfo, dim string) []CityGroup {
	byTag := make(map[string][]CityInfo)
	for _, c := range cities {
		matched := false
		for _, t := range c.Tags {
			if d, _ := config.ParseTag(t); d != dim {
				continue
			}
			if t == "" {
				// An explicit empty tag parses into the default dimension; keep
				// it in the untagged bucket rather than a group labelled "".
				continue
			}
			matched = true
			byTag[t] = append(byTag[t], c)
		}
		if !matched && dim == config.DefaultTagDimension {
			byTag[""] = append(byTag[""], c)
		}
	}

	tags := make([]string, 0, len(byTag))
	for t := range byTag {
		if t != "" {
			tags = append(tags, t)
		}
	}
	// Tiebreak on the raw tag. tags is filled by ranging byTag (randomized
	// order), and a case-only comparator makes "Bay Area" and "bay area" compare
	// equal — sort.Slice is not stable, so the pair kept whatever order the map
	// handed out and index.html differed byte-for-byte across regens of
	// unchanged data. Case-differing tags do coexist: unionTags dedupes on exact
	// equality and validateTags only rejects blanks and malformed prefixes. A
	// stable sort alone would not fix this; the comparator itself has to be
	// total. Compare on the label (the value) so "state:CA" sorts by "CA".
	sort.Slice(tags, func(i, j int) bool {
		a, b := strings.ToLower(config.TagLabel(tags[i])), strings.ToLower(config.TagLabel(tags[j]))
		if a != b {
			return a < b
		}
		return tags[i] < tags[j]
	})

	groups := make([]CityGroup, 0, len(byTag))
	appendSortedGroup := func(tag string) {
		group := byTag[tag]
		sort.SliceStable(group, func(i, j int) bool {
			return strings.ToLower(group[i].Name) < strings.ToLower(group[j].Name)
		})
		groups = append(groups, CityGroup{Tag: tag, Label: config.TagLabel(tag), Cities: group})
	}
	for _, t := range tags {
		appendSortedGroup(t)
	}
	// Untagged cities last, only if any exist. Keyed off the map rather than a
	// flag so a city carrying an explicit empty tag string (rejected by config,
	// but possible for a CityInfo built outside that path) still renders.
	if _, ok := byTag[""]; ok {
		appendSortedGroup("")
	}
	return groups
}

// BuildMeta builds metadata JSON for a city entry. snapshotID is the pinned
// snapshot the entry's Store was scoped to (0 == latest/unpinned). For a
// pinned snapshot the SnapshotDate reflects that snapshot's computed_at rather
// than today — a time-travel view of a months-old snapshot must show when it
// was computed, not the render date (which would freeze at first-request date
// in the lifetime cache).
func BuildMeta(ctx context.Context, entry CityEntry, snapshotID int64) (MetaJSON, error) {
	bbox, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return MetaJSON{}, fmt.Errorf("city %s: %w", entry.City.Name, err)
	}
	snapshotDate, err := snapshotDate(ctx, entry.Store, snapshotID)
	if err != nil {
		return MetaJSON{}, fmt.Errorf("city %s snapshot date: %w", entry.City.Name, err)
	}
	meta := MetaJSON{
		ProjectName:  entry.City.Name,
		BBox:         bbox,
		CenterLon:    lon,
		CenterLat:    lat,
		SnapshotDate: snapshotDate,
	}
	for _, rt := range resource.All {
		result, err := entry.Store.LatestComputeResult(ctx, rt.Type())
		if err != nil {
			// sql.ErrNoRows is the normal "this resource hasn't been computed
			// yet" state — skip it. Any other error is a real DB failure and
			// is propagated so serveMetaJSON's cache evicts and retries
			// instead of memoizing a partial meta for the server's lifetime.
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return MetaJSON{}, fmt.Errorf("latest compute result for %s: %w", rt.Type(), err)
		}
		typeName := string(result.ResourceType.Bare())
		meta.Stats = append(meta.Stats, StatJSON{
			Type:            typeName,
			Color:           ResourceColors[typeName],
			TotalArea:       result.TotalArea,
			FeatureCount:    result.FeatureCount,
			EstimatedSpaces: result.EstimatedSpaces,
		})
	}

	// Total paved area across all resource types: prefer the cross-resource
	// union row written by `pvmt all compute` (RunCombined). Fall back to
	// summing per-resource rows when the combined row is missing — the sum
	// inflates pct_paved by the road/sidewalk/parking buffer overlap, but
	// keeps single-resource workflows usable until `all compute` runs.
	meta.TotalPaved = totalPavedFromStore(ctx, entry.Store, meta.Stats)

	// Compute city boundary area and % paved.
	if boundaryGJSON, err := entry.Store.GetBoundary(ctx); err == nil && boundaryGJSON != "" {
		if cityArea, err := geo.BoundaryArea(ctx, boundaryGJSON); err == nil && cityArea > 0 {
			meta.CityArea = cityArea
			if meta.TotalPaved > 0 {
				meta.PctPaved = meta.TotalPaved / cityArea * 100
			}
		}
	}

	return meta, nil
}

// snapshotDate returns the YYYY-MM-DD date to report as snapshot_date. For the
// unpinned/latest path (snapshotID <= 0) it returns today, matching the
// restart-for-fresh-data invariant of the rest of the exporter. For a pinned
// snapshot it looks up that snapshot's computed_at via ListSnapshots (the
// snapshot-unaware list, which the WithSnapshot-scoped store still exposes) so
// the time-travel UI shows when the data was actually computed. A pinned id
// that resolved elsewhere but is absent from the list is treated as a
// transient inconsistency (error) rather than silently falling back to today,
// so serveMetaJSON's cache evicts and retries.
func snapshotDate(ctx context.Context, store db.Store, snapshotID int64) (string, error) {
	if snapshotID <= 0 {
		return time.Now().Format("2006-01-02"), nil
	}
	snaps, err := store.ListSnapshots(ctx)
	if err != nil {
		return "", fmt.Errorf("list snapshots: %w", err)
	}
	for _, snap := range snaps {
		if snap.ID == snapshotID {
			return snap.ComputedAt.Format("2006-01-02"), nil
		}
	}
	return "", fmt.Errorf("snapshot %d not found", snapshotID)
}

// totalPavedFromStore returns the cross-resource paved area: the "combined"
// ComputeResult if present, otherwise the sum of per-resource Stats. The
// fallback intentionally double-counts where buffers overlap (the bug that
// motivated RunCombined) — better than reporting zero before `all compute`
// has populated the combined row.
func totalPavedFromStore(ctx context.Context, store db.Store, perResource []StatJSON) float64 {
	if r, err := store.LatestComputeResult(ctx, resource.CombinedAll); err == nil && r != nil {
		return r.TotalArea
	}
	var sum float64
	for _, st := range perResource {
		sum += st.TotalArea
	}
	return sum
}
