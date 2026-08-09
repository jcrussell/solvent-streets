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

// CityGroup is a tag label with the cities that belong to it. Used by the
// template to render the city selector as <optgroup>s. A group with an empty
// Tag holds the untagged ("Other") cities and is rendered as bare options.
// A city with multiple tags appears in multiple groups.
type CityGroup struct {
	Tag    string
	Cities []CityInfo
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
	// CitiesByTag is the same cities as Cities, grouped for the selector:
	// non-empty tags first (sorted ascending by label, cities sorted by
	// name within each), then the untagged group ("Other") last. A city with
	// multiple tags appears in multiple groups. Built alongside Cities; the
	// flat Cities slice is kept for the CITIES JS array and cities.json.
	CitiesByTag []CityGroup
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
	cities := make([]jsCityInfo, len(d.Cities))
	for i, c := range d.Cities {
		cities[i] = jsCityInfo{Slug: c.Slug, Name: c.Name, BBox: c.BBox, Lon: c.CenterLon, Lat: c.CenterLat, Tags: c.Tags}
	}
	cfg := struct {
		Cities       []jsCityInfo    `json:"cities"`
		Center       [2]float64      `json:"center"`
		LayerColors  json.RawMessage `json:"layerColors"`
		UnitSystem   string          `json:"unitSystem"`
		ForecastSeed json.RawMessage `json:"forecastSeed"`
		WasmPrefix   string          `json:"wasmPrefix"`
	}{
		Cities:       cities,
		Center:       [2]float64{d.CenterLon, d.CenterLat},
		LayerColors:  rawOrNull(d.LayerColors),
		UnitSystem:   d.UnitSystem,
		ForecastSeed: rawOrNull(d.ForecastSeed),
		WasmPrefix:   d.WasmPrefix,
	}
	data, _ := json.Marshal(cfg)
	return template.JS(data)
}

// GameConfigJSON is the window.PVMT_CONFIG payload for play.html, consumed by
// templates/game.js. Like IndexConfigJSON it is a method so neither construction
// site changes. DataPrefix/MapHref reproduce the {{if}} branches the template
// used to inline (empty ActiveSlug → client-side resolution; static host → a
// relative back-to-map link).
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
		Center       [2]float64      `json:"center"`
		ForecastSeed json.RawMessage `json:"forecastSeed"`
		DataPrefix   string          `json:"dataPrefix"`
		MapHref      string          `json:"mapHref"`
		BandCount    int             `json:"bandCount"`
		WasmPrefix   string          `json:"wasmPrefix"`
	}{
		Center:       [2]float64{d.CenterLon, d.CenterLat},
		ForecastSeed: rawOrNull(d.ForecastSeed),
		DataPrefix:   dataPrefix,
		MapHref:      mapHref,
		BandCount:    d.BandCount(),
		WasmPrefix:   d.WasmPrefix,
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
}

// ResourceColors maps resource type names to their display colors.
var ResourceColors = map[string]string{
	"roads":     "#6b7280",
	"parking":   "#3b82f6",
	"sidewalks": "#f59e0b",
}

// GroupCitiesByTag groups cities for the selector. Non-empty tags come first,
// sorted ascending by tag label, with cities sorted ascending by name within
// each group. A city with multiple tags appears in every matching group
// (multi-membership). Cities with no tags are collected into a final group
// (Tag == "") rendered as ungrouped ("Other") options. The input slice is not
// mutated. Returns nil when cities is empty.
func GroupCitiesByTag(cities []CityInfo) []CityGroup {
	if len(cities) == 0 {
		return nil
	}
	byTag := make(map[string][]CityInfo)
	for _, c := range cities {
		if len(c.Tags) == 0 {
			byTag[""] = append(byTag[""], c)
			continue
		}
		for _, t := range c.Tags {
			byTag[t] = append(byTag[t], c)
		}
	}

	tags := make([]string, 0, len(byTag))
	for t := range byTag {
		if t != "" {
			tags = append(tags, t)
		}
	}
	sort.Slice(tags, func(i, j int) bool {
		return strings.ToLower(tags[i]) < strings.ToLower(tags[j])
	})

	groups := make([]CityGroup, 0, len(byTag))
	appendSortedGroup := func(tag string) {
		group := byTag[tag]
		sort.SliceStable(group, func(i, j int) bool {
			return strings.ToLower(group[i].Name) < strings.ToLower(group[j].Name)
		})
		groups = append(groups, CityGroup{Tag: tag, Cities: group})
	}
	for _, t := range tags {
		appendSortedGroup(t)
	}
	// Untagged cities last, only if any exist. Keyed off the map rather than a
	// flag set in the len(Tags)==0 branch: a city carrying an explicit empty
	// tag string also lands in byTag[""], and a flag would leave that group
	// unrendered — dropping the city from the selector entirely while it
	// stayed in cities.json. config rejects empty tags, so this is belt and
	// braces for any CityInfo built outside that path.
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
			Type:         typeName,
			Color:        ResourceColors[typeName],
			TotalArea:    result.TotalArea,
			FeatureCount: result.FeatureCount,
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
