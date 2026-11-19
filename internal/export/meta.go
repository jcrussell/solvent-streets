package export

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jcrussell/solvent-streets/internal/build"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/game"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/logs"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// CityInfo holds city metadata for the frontend city switcher.
type CityInfo struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// BBox is [west, south, east, north] (GeoJSON order; see geoJSONBBox).
	BBox [4]float64 `json:"bbox"`
	// Center is the map's initial view, [lon, lat].
	Center [2]float64 `json:"center"`
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

// CityOption is one <option> of the city selector: the city's slug as the
// value, its name as the label, and its bbox ("west,south,east,north", which
// the index page reads to fit the map) as data-bbox. Selected marks the one
// option the selector opens on.
type CityOption struct {
	Slug     string
	Name     string
	BBox     string
	Selected bool
}

// CityOptionGroup is a run of city options: one <optgroup> labelled Label
// when Grouped, bare options otherwise (the untagged cities).
type CityOptionGroup struct {
	Grouped bool
	Label   string
	Options []CityOption
}

// CitySelect is the city selector as the city_select partial renders it and
// as selectors.js rebuildCitySelect rebuilds it: CityGroups' layout, one
// option shape, and exactly one option selected. That is ActiveSlug's first
// occurrence (a multi-tag city is listed under each of its groups), or the
// first option when ActiveSlug is empty or names no listed city.
func (d TemplateData) CitySelect() []CityOptionGroup {
	groups := d.CityGroups()
	out := make([]CityOptionGroup, 0, len(groups))
	var first, active *CityOption
	for _, g := range groups {
		og := CityOptionGroup{Grouped: g.Tag != "", Label: g.Label, Options: make([]CityOption, len(g.Cities))}
		for i, c := range g.Cities {
			og.Options[i] = CityOption{Slug: c.Slug, Name: c.Name, BBox: bboxAttr(c.BBox)}
			opt := &og.Options[i]
			if first == nil {
				first = opt
			}
			if active == nil && d.ActiveSlug != "" && c.Slug == d.ActiveSlug {
				active = opt
			}
		}
		out = append(out, og)
	}
	if active == nil {
		active = first
	}
	if active != nil {
		active.Selected = true
	}
	return out
}

// bboxAttr joins a bbox as the comma-separated decimals JavaScript's
// Array.join gives the same numbers, so the server-rendered data-bbox and the
// one rebuildCitySelect writes are the same string.
func bboxAttr(b [4]float64) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strings.Join(parts, ",")
}

// HeadingName is the name the page title and panel heading show before any
// script runs: in multi-city mode the city the selector opens on (CitySelect's
// selected option), else ProjectName. The pages then keep it in step with the
// selected city (solvent-streets-youz).
func (d TemplateData) HeadingName() string {
	if opt, ok := d.selectedCity(); ok {
		return opt.Name
	}
	if len(d.Cities) > 0 {
		return d.Cities[0].Name
	}
	return d.ProjectName
}

// selectedCity is the option CitySelect marks selected.
func (d TemplateData) selectedCity() (CityOption, bool) {
	for _, g := range d.CitySelect() {
		for _, opt := range g.Options {
			if opt.Selected {
				return opt, true
			}
		}
	}
	return CityOption{}, false
}

// DefaultCitySlug is the city a multi-city page opens on when none is asked
// for: the selector's first option, which follows the tag groups rather than
// the config order. Empty when no city is listed.
func DefaultCitySlug(cities []CityInfo) string {
	opt, _ := TemplateData{Cities: cities, TagDimensions: GroupCitiesByDimension(cities)}.selectedCity()
	return opt.Slug
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

// ForecastDefaults are the forecast settings a page shows in its static markup
// and falls back on in script wherever a city's own forecast_seed.json has not
// loaded or omits a value. The browser reads them as
// PVMT_CONFIG.forecast_defaults, so it carries no copy of these numbers.
type ForecastDefaults struct {
	InitialPCI          float64 `json:"initial_pci"`
	HorizonYears        int     `json:"horizon_years"`
	TreatmentCycleYears float64 `json:"treatment_cycle_years"`
	BarrelsPerTonBinder float64 `json:"barrels_per_ton_binder"`
}

// PCISlider is InitialPCI on the Initial PCI slider's whole-number track,
// rounded half up as the browser's Math.round does when it seeds the slider.
func (f ForecastDefaults) PCISlider() int { return int(math.Floor(f.InitialPCI + 0.5)) }

// ForecastDefaults returns the page's forecast settings: those of the seed it
// was rendered with (resolved from the config), and the Go defaults for a
// page with no seed or a value the seed leaves unset.
func (d TemplateData) ForecastDefaults() ForecastDefaults {
	out := ForecastDefaults{
		InitialPCI:          config.DefaultInitialPCI,
		HorizonYears:        config.DefaultForecastYears,
		TreatmentCycleYears: forecast.DefaultTreatmentCycleYears,
		BarrelsPerTonBinder: forecast.BarrelsPerTonBinder,
	}
	var seed ForecastSeedJSON
	if d.ForecastSeed == "" || json.Unmarshal([]byte(d.ForecastSeed), &seed) != nil {
		return out
	}
	if seed.InitialPCI > 0 {
		out.InitialPCI = seed.InitialPCI
	}
	if seed.HorizonYears > 0 {
		out.HorizonYears = seed.HorizonYears
	}
	if seed.TreatmentCycleYears > 0 {
		out.TreatmentCycleYears = seed.TreatmentCycleYears
	}
	if seed.BarrelsPerTonBinder > 0 {
		out.BarrelsPerTonBinder = seed.BarrelsPerTonBinder
	}
	return out
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
	return time.Now().Format(time.DateOnly), build.Current().Short()
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
// `.tag_dimensions.length` without a guard.
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

// geoJSONBBox reorders an internal [south, west, north, east] bbox (the
// Overpass order geo and the config use) into the [west, south, east, north]
// order every emitted bbox uses, matching GeoJSON and the [lon, lat] centers.
func geoJSONBBox(b [4]float64) [4]float64 {
	return [4]float64{b[1], b[0], b[3], b[2]}
}

// citiesOrEmpty returns cities, or an empty slice so the payload carries []
// rather than null.
func citiesOrEmpty(cities []CityInfo) []CityInfo {
	if cities == nil {
		return []CityInfo{}
	}
	return cities
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
		Cities        []CityInfo       `json:"cities"`
		TagDimensions []jsTagDimension `json:"tag_dimensions"`
		Center        [2]float64       `json:"center"`
		LayerColors   json.RawMessage  `json:"layer_colors"`
		UnitSystem    string           `json:"unit_system"`
		ForecastSeed  json.RawMessage  `json:"forecast_seed"`
		Defaults      ForecastDefaults `json:"forecast_defaults"`
		HexMetrics    []hexmetric.Spec `json:"hex_metrics"`
		HexGridV      int              `json:"hexgrid_version"`
	}{
		Cities:        citiesOrEmpty(d.Cities),
		TagDimensions: jsTagDimensions(d.TagDimensions),
		Center:        d.Center,
		LayerColors:   rawOrNull(d.LayerColors),
		UnitSystem:    d.UnitSystem,
		ForecastSeed:  rawOrNull(d.ForecastSeed),
		Defaults:      d.ForecastDefaults(),
		// The derived "Color by" metrics (coverage, area, spaces, est. cost):
		// static registry data, so every city offers them with or without
		// a hex-metrics.json.
		HexMetrics: hexmetric.DerivedSpecs(),
		HexGridV:   HexGridFileVersion,
	}
	data, _ := json.Marshal(cfg)
	return template.JS(data)
}

// GameConfigJSON is the window.PVMT_CONFIG payload for play.html, consumed by
// templates/game.js. data_prefix is empty when there is no ActiveSlug (the
// client resolves the city itself); map_href is the back-to-map link, relative
// on a static host. cities/tag_dimensions carry the same payload the index page
// gets so the play page's tag filters can rebuild its city selector
// client-side from the same data. pci_jitter is the board's starting-PCI
// spread (GamePCIJitter). hexgrid_version is the hexgrid.geojson "v" the
// board reads (HexGridFileVersion), as on the index page.
func (d TemplateData) GameConfigJSON() template.JS {
	dataPrefix := ""
	if d.ActiveSlug != "" {
		dataPrefix = CityDataPrefix(d.ActiveSlug)
	}
	mapHref := IndexFileName
	if d.IsLiveServer {
		mapHref = "/"
	}
	cfg := struct {
		Cities        []CityInfo       `json:"cities"`
		TagDimensions []jsTagDimension `json:"tag_dimensions"`
		Center        [2]float64       `json:"center"`
		ForecastSeed  json.RawMessage  `json:"forecast_seed"`
		Defaults      ForecastDefaults `json:"forecast_defaults"`
		DataPrefix    string           `json:"data_prefix"`
		MapHref       string           `json:"map_href"`
		BandCount     int              `json:"band_count"`
		PCIJitter     float64          `json:"pci_jitter"`
		HexGridV      int              `json:"hexgrid_version"`
	}{
		Cities:        citiesOrEmpty(d.Cities),
		TagDimensions: jsTagDimensions(d.TagDimensions),
		Center:        d.Center,
		ForecastSeed:  rawOrNull(d.ForecastSeed),
		Defaults:      d.ForecastDefaults(),
		DataPrefix:    dataPrefix,
		MapHref:       mapHref,
		BandCount:     d.BandCount(),
		PCIJitter:     GamePCIJitter,
		HexGridV:      HexGridFileVersion,
	}
	data, _ := json.Marshal(cfg)
	return template.JS(data)
}

type MetaJSON struct {
	ProjectName  string     `json:"project_name"`
	BBox         [4]float64 `json:"bbox"`   // [west, south, east, north]
	Center       [2]float64 `json:"center"` // [lon, lat]
	SnapshotDate string     `json:"snapshot_date"`
	// Snapshot names the snapshot the city's data comes from; omitted for a
	// latest compute, which LatestCompute marks instead (pvmt serve shows it
	// for a city with no snapshot, and for ?snapshot=latest).
	Snapshot      string     `json:"snapshot,omitempty"`
	LatestCompute bool       `json:"latest_compute,omitempty"`
	Stats         []StatJSON `json:"stats"`
	CityArea      float64    `json:"city_area,omitempty"`
	TotalPaved    float64    `json:"total_paved,omitempty"`
	PavedShare    float64    `json:"paved_share,omitempty"` // total_paved / city_area, 0..1
	// Metrics summarises each per-hex metric (hex-metrics.json) over the
	// painted hexes, for the Map stats card. Omitted when none has rows.
	Metrics map[string]MetricSummaryJSON `json:"metrics,omitempty"`
	// MissingResources names, in resource.All order, each resource the
	// city's compute had no features for (or never computed): a layer whose
	// ingest failed or never ran, not a real zero. The dashboard says the
	// data is unavailable and keeps the city out of rankings that need the
	// layer; check-site lists such cities (solvent-streets-56r1). Omitted when
	// every resource has data, and on a multi-city page's own meta.
	MissingResources []string `json:"missing_resources,omitempty"`
	// IngestFailures maps a resource to the configured sources whose newest
	// ingest attempt failed and that hold none of its rows (no earlier
	// fetch of theirs left any): resource -> sources, in source order. A
	// resource also in MissingResources failed outright; one with data is
	// partial (Albany: OpenStreetMap roads failed, ArcGIS answered). The
	// dashboard marks a partial layer and ranks the city like one missing
	// it; check-site warns (solvent-streets-56r1). Read from the city's
	// ingest history at export time. Omitted when no source is failing.
	IngestFailures map[string][]string `json:"ingest_failures,omitempty"`
	// EmptyResources maps each resource in MissingResources that every
	// configured source last answered with no rows to those sources: upstream
	// maps none there (a small town with no sidewalks in OpenStreetMap), so a
	// re-ingest cannot fill it. The dashboard says none is mapped and
	// check-site does not ask for a re-ingest; the layer stays missing, out
	// of rankings, since no data is not a measured zero (solvent-streets-a3pn).
	// Read from the city's ingest history at export time. Omitted when none.
	EmptyResources map[string][]string `json:"empty_resources,omitempty"`
	// SettingsTOML is the settings the city's figures are computed under
	// (CityResolvedTOML), which a multi-city page's Config tab shows for
	// the city in view. Omitted when the entry carries no config.
	SettingsTOML string `json:"settings_toml,omitempty"`
}

type StatJSON struct {
	ResourceType string  `json:"resource_type"`
	Color        string  `json:"color"`
	TotalArea    float64 `json:"total_area"`
	// EstimatedSpaces is the parking resource's estimated vehicle capacity
	// (OSM capacity tags, else footprint area over a per-space constant; see
	// resource.EstimateParkingSpaces). Omitted for other resources.
	EstimatedSpaces int `json:"estimated_spaces,omitempty"`
}

// ResourceColors maps resource type names to their display colors.
var ResourceColors = map[string]string{
	string(resource.TypeRoads):     "#6b7280",
	string(resource.TypeParking):   "#3b82f6",
	string(resource.TypeSidewalks): "#f59e0b",
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
		sortCitiesByName(group)
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
// snapshot the entry's Store was scoped to (<= 0: whatever the Store reads).
// SnapshotDate is the date of the data, not today: the snapshot's created_at,
// or for a latest compute its newest run's computed_at — a time-travel view of
// a months-old snapshot must show when it was computed, not the render date
// (which would freeze at first-request date in the lifetime cache).
func BuildMeta(ctx context.Context, entry CityEntry, snapshotID int64) (MetaJSON, error) {
	return BuildMetaOn(ctx, entry, snapshotID, NewDrawnGrid(ctx, entry, nil))
}

// BuildMetaOn is BuildMeta over grid, the city's DrawnGrid, which an export
// and the live server share with hexgrid.geojson and hex-metrics.json.
func BuildMetaOn(ctx context.Context, entry CityEntry, snapshotID int64, grid *DrawnGrid) (MetaJSON, error) {
	bbox, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return MetaJSON{}, fmt.Errorf("city %s: %w", entry.City.Name, err)
	}
	stamp, err := dataStamp(ctx, entry, snapshotID)
	if err != nil {
		return MetaJSON{}, fmt.Errorf("city %s snapshot date: %w", entry.City.Name, err)
	}
	meta := MetaJSON{
		ProjectName:   entry.City.Name,
		BBox:          geoJSONBBox(bbox),
		Center:        [2]float64{lon, lat},
		SnapshotDate:  stamp.date,
		Snapshot:      stamp.snapshot,
		LatestCompute: stamp.latest,
		Stats:         []StatJSON{},
	}
	if err := addPavementStats(ctx, entry, &meta); err != nil {
		return MetaJSON{}, err
	}

	if meta.IngestFailures, meta.EmptyResources, err = ingestGaps(ctx, entry, meta.MissingResources); err != nil {
		return MetaJSON{}, fmt.Errorf("city %s ingest history: %w", entry.City.Name, err)
	}

	metrics, err := buildMetricSummaries(ctx, entry, grid)
	if err != nil {
		return MetaJSON{}, err
	}
	meta.Metrics = metrics

	if entry.Config != nil {
		edge, err := entry.HexEdge(ctx)
		if err != nil {
			return MetaJSON{}, fmt.Errorf("hex edge for %s: %w", entry.City.Name, err)
		}
		meta.SettingsTOML = CityResolvedTOML(entry.Config, entry.City, edge)
	}

	// Total paved area across all resource types: prefer the cross-resource
	// union row written by `pvmt all compute` (RunCombined). Fall back to
	// summing per-resource rows when the combined row is missing — the sum
	// inflates paved_share by the road/sidewalk/parking buffer overlap, but
	// keeps single-resource workflows usable until `all compute` runs.
	meta.TotalPaved, _, err = results.PavedArea(ctx, entry.Store, resource.ScopeAll)
	if err != nil {
		return MetaJSON{}, fmt.Errorf("paved area for %s: %w", entry.City.Name, err)
	}

	// Compute city boundary area and the paved share of it. A read failure
	// (a pinned snapshot gone above all) fails the build; an unparsable
	// boundary only leaves the area out.
	boundaryGJSON, err := entry.Store.GetBoundary(ctx)
	if err != nil {
		return MetaJSON{}, fmt.Errorf("boundary for %s: %w", entry.City.Name, err)
	}
	if boundaryGJSON != "" {
		if cityArea, err := geo.BoundaryArea(cityLogCtx(ctx, entry), boundaryGJSON); err == nil && cityArea > 0 {
			meta.CityArea = cityArea
			if meta.TotalPaved > 0 {
				meta.PavedShare = meta.TotalPaved / cityArea
			}
		}
	}

	return meta, nil
}

// cityLogCtx attaches the entry's city to ctx's logger, so a warning a geo
// helper emits from deep inside an export run (BoundaryArea's dropped
// sub-polygons) says which of many cities it is about.
func cityLogCtx(ctx context.Context, entry CityEntry) context.Context {
	return logs.WithLogger(ctx, logs.From(ctx).With("city", entry.City.Name))
}

// stamp is where a city's meta.json says its data comes from.
type stamp struct {
	date     string // YYYY-MM-DD
	snapshot string // the snapshot's name; "" for a latest compute, or none
	latest   bool   // a latest compute (CityEntry.ReadsLatest)
}

// dataStamp dates the data entry reads. A pinned snapshotID (> 0) is looked
// up in ListSnapshots (the snapshot-unaware list, which a WithSnapshot-scoped
// store still exposes); an id that resolved elsewhere but is absent from the
// list is a transient inconsistency (error) rather than a silent fallback to
// today, so serveMetaJSON's cache evicts and retries. Otherwise the snapshot
// the store reads (ViewSnapshot) gives its created_at, and a latest compute
// its newest run's computed_at. Only a store that reads neither (an
// uncomputed city) reports today.
func dataStamp(ctx context.Context, entry CityEntry, snapshotID int64) (stamp, error) {
	if snapshotID > 0 {
		snaps, err := entry.Store.ListSnapshots(ctx)
		if err != nil {
			return stamp{}, fmt.Errorf("list snapshots: %w", err)
		}
		for _, snap := range snaps {
			if snap.ID == snapshotID {
				return stamp{date: snap.CreatedAt.Format(time.DateOnly), snapshot: snap.Name}, nil
			}
		}
		return stamp{}, fmt.Errorf("snapshot %d not found", snapshotID)
	}
	if entry.ReadsLatest() {
		var newest time.Time
		for _, p := range entry.Latest {
			if p.ComputedAt.After(newest) {
				newest = p.ComputedAt
			}
		}
		// An empty latest compute has no date: it was never computed, and
		// today's date would claim it was (solvent-streets-qo1y).
		st := stamp{latest: true}
		if !newest.IsZero() {
			st.date = newest.Format(time.DateOnly)
		}
		return st, nil
	}
	snap, err := entry.Store.ViewSnapshot(ctx)
	if err != nil {
		return stamp{}, err
	}
	if snap != nil {
		return stamp{date: snap.CreatedAt.Format(time.DateOnly), snapshot: snap.Name}, nil
	}
	return stamp{date: time.Now().Format(time.DateOnly)}, nil
}

// ingestGaps reads entry's ingest history for MetaJSON's IngestFailures and
// EmptyResources. failures: per pavement resource, the city's configured
// sources whose newest attempt failed and whose last answer, if any, left no
// rows. A failed source keeps the rows an earlier fetch stored (the save
// replaces only the sources that answered), so a source with a successful
// past is stale at worst, not missing. empty: per resource in missing, the
// configured sources when every one of them last answered with no rows
// (db.AnsweredEmpty).
func ingestGaps(ctx context.Context, entry CityEntry, missing []string) (failures, empty map[string][]string, err error) {
	history, err := entry.Store.IngestHistory(ctx)
	if err != nil {
		return nil, nil, err
	}
	var configured []string
	if entry.City.Overpass {
		configured = append(configured, resource.SourceOverpass)
	}
	if entry.City.ArcGISURL != "" {
		configured = append(configured, resource.SourceArcGIS)
	}
	for _, rt := range resource.All {
		input, name := string(rt.Type()), string(rt.Type().Bare())
		if failing := db.FailingSources(history, input, configured); len(failing) > 0 {
			if failures == nil {
				failures = map[string][]string{}
			}
			failures[name] = failing
		}
		if slices.Contains(missing, name) && db.AnsweredEmpty(history, input, configured) {
			if empty == nil {
				empty = map[string][]string{}
			}
			empty[name] = slices.Clone(configured)
		}
	}
	return failures, empty, nil
}

// addPavementStats fills meta's Stats and MissingResources from each pavement
// resource's stored totals.
func addPavementStats(ctx context.Context, entry CityEntry, meta *MetaJSON) error {
	for _, rt := range resource.All {
		result, ok, err := results.LoadTotal(ctx, entry.Store, rt.Type())
		if err != nil {
			// A real DB failure is propagated so serveMetaJSON's cache evicts
			// and retries instead of memoizing a partial meta for the
			// server's lifetime.
			return fmt.Errorf("latest compute result for %s: %w", rt.Type(), err)
		}
		typeName := string(rt.Type().Bare())
		if !ok || result.Features == 0 {
			meta.MissingResources = append(meta.MissingResources, typeName)
		}
		if !ok {
			// This resource hasn't been computed yet.
			continue
		}
		meta.Stats = append(meta.Stats, StatJSON{
			ResourceType:    typeName,
			Color:           ResourceColors[typeName],
			TotalArea:       result.Area,
			EstimatedSpaces: result.Spaces,
		})
	}
	return nil
}
