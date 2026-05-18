package export

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"sync"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
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
}

// TemplateData wraps MetaJSON with the forecast seed for the interactive controls.
type TemplateData struct {
	MetaJSON
	ForecastSeed    template.JS
	LayerColors     template.JS // JSON map of resource type → color
	RawTOML         string      // original pvmt.toml contents
	ResolvedTOML    string      // config with all defaults filled in
	UnitSystem      string      // "metric" or "imperial"
	Cities          []CityInfo
	WasmPrefix      string // path prefix for WASM assets (e.g. "../"); empty = same directory
	MethodologyHTML template.HTML
	// IsLiveServer is true when rendered by pvmt serve; false for static
	// export. Gates server-only UI (e.g. the snapshot picker) that depends
	// on live /api endpoints absent from the static output.
	IsLiveServer bool
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

type MetaJSON struct {
	ProjectName   string     `json:"project_name"`
	BBox          [4]float64 `json:"bbox"`
	CenterLon     float64    `json:"center_lon"`
	CenterLat     float64    `json:"center_lat"`
	SnapshotDate  string     `json:"snapshot_date"`
	Stats         []StatJSON `json:"stats"`
	CityAreaSqM   float64    `json:"city_area_sqm,omitempty"`
	TotalPavedSqM float64    `json:"total_paved_sqm,omitempty"`
	PctPaved      float64    `json:"pct_paved,omitempty"`
}

type StatJSON struct {
	Type         string  `json:"type"`
	Color        string  `json:"color"`
	TotalAreaSqM float64 `json:"total_area_sqm"`
	FeatureCount int     `json:"feature_count"`
}

// ResourceColors maps resource type names to their display colors.
var ResourceColors = map[string]string{
	"roads":     "#6b7280",
	"parking":   "#3b82f6",
	"sidewalks": "#f59e0b",
}

// BuildMeta builds metadata JSON for a city entry.
func BuildMeta(ctx context.Context, entry CityEntry) (MetaJSON, error) {
	bbox, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return MetaJSON{}, fmt.Errorf("city %s: %w", entry.City.Name, err)
	}
	meta := MetaJSON{
		ProjectName:  entry.City.Name,
		BBox:         bbox,
		CenterLon:    lon,
		CenterLat:    lat,
		SnapshotDate: time.Now().Format("2006-01-02"),
	}
	for _, rt := range resource.All {
		result, err := entry.Store.LatestComputeResult(ctx, rt.Type())
		if err != nil {
			continue
		}
		typeName := string(result.ResourceType.Bare())
		meta.Stats = append(meta.Stats, StatJSON{
			Type:         typeName,
			Color:        ResourceColors[typeName],
			TotalAreaSqM: result.TotalAreaSqM,
			FeatureCount: result.FeatureCount,
		})
	}

	// Total paved area across all resource types: prefer the cross-resource
	// union row written by `pvmt all compute` (RunCombined). Fall back to
	// summing per-resource rows when the combined row is missing — the sum
	// inflates pct_paved by the road/sidewalk/parking buffer overlap, but
	// keeps single-resource workflows usable until `all compute` runs.
	meta.TotalPavedSqM = totalPavedFromStore(ctx, entry.Store, meta.Stats)

	// Compute city boundary area and % paved.
	if boundaryGJSON, err := entry.Store.GetBoundary(ctx); err == nil && boundaryGJSON != "" {
		if cityAreaSqM, err := geo.BoundaryAreaSqM(boundaryGJSON); err == nil && cityAreaSqM > 0 {
			meta.CityAreaSqM = cityAreaSqM
			if meta.TotalPavedSqM > 0 {
				meta.PctPaved = meta.TotalPavedSqM / cityAreaSqM * 100
			}
		}
	}

	return meta, nil
}

// totalPavedFromStore returns the cross-resource paved area: the "combined"
// ComputeResult if present, otherwise the sum of per-resource Stats. The
// fallback intentionally double-counts where buffers overlap (the bug that
// motivated RunCombined) — better than reporting zero before `all compute`
// has populated the combined row.
func totalPavedFromStore(ctx context.Context, store db.Store, perResource []StatJSON) float64 {
	if r, err := store.LatestComputeResult(ctx, resource.CombinedAll); err == nil && r != nil {
		return r.TotalAreaSqM
	}
	var sum float64
	for _, st := range perResource {
		sum += st.TotalAreaSqM
	}
	return sum
}
