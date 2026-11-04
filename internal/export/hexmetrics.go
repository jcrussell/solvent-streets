package export

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/services"
)

// HexMetricsFileVersion is the "v" of hex-metrics.json; bump on any change
// to the shape below (the client refuses unknown versions).
const HexMetricsFileVersion = 1

// HexMetricsJSON is hex-metrics.json: every registered metric that has rows
// for the city, keyed by metric key, each self-describing enough for the
// dashboard to format values and paint hexes without metric-specific code.
type HexMetricsJSON struct {
	V       int                   `json:"v"`
	Metrics map[string]MetricJSON `json:"metrics"`
}

// MetricJSON is one metric's rows plus its registry description. For a
// service's coverage metric the Scale and ThresholdS come from the RESOLVED
// service spec (config [[services]] overrides applied), so the legend bands
// and the "covered" line match what the compute pass stored.
type MetricJSON struct {
	Label  string          `json:"label"`
	Unit   string          `json:"unit"`
	Format string          `json:"format"`
	Source string          `json:"source"`
	Scale  hexmetric.Scale `json:"scale"`
	// ThresholdS is the total seconds (turnout + travel threshold) within
	// which a hex counts as covered; omitted for metrics without one.
	ThresholdS float64 `json:"threshold_s,omitempty"`
	// Values maps hex id → value.
	Values map[string]float64 `json:"values"`
	// Refs maps hex id → index into Points, for metrics that reference a
	// point of service per hex (fire: the nearest station). Omitted otherwise.
	Refs map[string]int `json:"refs,omitempty"`
	// Points are the referenced points, in index order. Omitted when unused.
	Points []MetricPointJSON `json:"points,omitempty"`
}

// MetricPointJSON is a referenced point of service.
type MetricPointJSON struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Lon    float64 `json:"lon"`
	Lat    float64 `json:"lat"`
	Source string  `json:"source"`
}

// MetricSummaryJSON is the per-metric summary meta.json carries for the stats
// card: computed over every stored row, which (hexgrid v4) are the hexes the
// map paints for that metric.
type MetricSummaryJSON struct {
	Label  string  `json:"label"`
	Unit   string  `json:"unit"`
	Format string  `json:"format"`
	Hexes  int     `json:"hexes"`
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	Max    float64 `json:"max"`
	Points int     `json:"points,omitempty"`
	// ThresholdS and PctWithin describe coverage against the service's
	// resolved threshold (total seconds); omitted for metrics without one.
	ThresholdS float64 `json:"threshold_s,omitempty"`
	PctWithin  float64 `json:"pct_within,omitempty"`
}

// resolvedScale returns the scale (and covered threshold) to publish for a
// metric: the registry's, unless a service produces it, in which case the
// bands follow the service spec resolved against the entry's config so a
// turnout/threshold override moves the legend with it. ok is false when the
// metric has no threshold.
func resolvedScale(entry CityEntry, spec hexmetric.Spec) (hexmetric.Scale, float64, bool) {
	svc, ok := services.ForMetric(spec.Key)
	if !ok {
		return spec.Scale, 0, false
	}
	if entry.Config != nil {
		svc = services.Resolve(svc, entry.Config.Services)
	}
	scale := spec.Scale
	scale.Bands = svc.LegendBands()
	return scale, svc.TurnoutS + svc.TravelThresholdS, true
}

// BuildHexMetrics assembles hex-metrics.json for a city. Returns (nil, nil)
// when no registered metric has rows (the file is optional: see
// OptionalDataFileNames), and propagates real store errors so the server's
// cache evicts rather than memoising a blank file.
func BuildHexMetrics(ctx context.Context, entry CityEntry) (*HexMetricsJSON, error) {
	out := &HexMetricsJSON{V: HexMetricsFileVersion, Metrics: map[string]MetricJSON{}}
	for _, spec := range hexmetric.StoredSpecs() {
		rows, err := entry.Store.ListHexMetrics(ctx, spec.Key)
		if err != nil {
			return nil, fmt.Errorf("listing hex metric %s: %w", spec.Key, err)
		}
		if len(rows) == 0 {
			continue
		}
		m, err := buildMetricJSON(ctx, entry, spec, rows)
		if err != nil {
			return nil, err
		}
		out.Metrics[spec.Key] = m
	}
	if len(out.Metrics) == 0 {
		return nil, nil //nolint:nilnil // nil = legitimate "no metrics", distinct from a store error
	}
	return out, nil
}

// buildMetricJSON assembles one metric's file entry: the registry
// description, rounded per-hex values, and — when rows reference points —
// the resolved points list with per-hex indexes into it.
func buildMetricJSON(ctx context.Context, entry CityEntry, spec hexmetric.Spec, rows []db.HexMetric) (MetricJSON, error) {
	scale, threshold, _ := resolvedScale(entry, spec)
	m := MetricJSON{Label: spec.Label, Unit: spec.Unit, Format: spec.Format, Source: spec.Source, Scale: scale, ThresholdS: threshold, Values: make(map[string]float64, len(rows))}
	refIDs := map[string]bool{}
	for _, r := range rows {
		m.Values[r.HexID] = round2(r.Value)
		if r.Ref != "" {
			refIDs[r.Ref] = true
		}
	}
	if len(refIDs) == 0 {
		return m, nil
	}
	points, err := metricPoints(ctx, entry, spec.Key, refIDs)
	if err != nil {
		return MetricJSON{}, err
	}
	index := make(map[string]int, len(points))
	for i, p := range points {
		index[p.ID] = i
	}
	m.Points = points
	m.Refs = make(map[string]int, len(rows))
	for _, r := range rows {
		if i, ok := index[r.Ref]; ok {
			m.Refs[r.HexID] = i
		}
	}
	return m, nil
}

// metricPoints resolves the point ids a metric references. A service's
// coverage metric references its facilities: the ingested OSM points (by
// feature id) and the city's hand-placed [[cities.facilities]]
// ("config:<service>:<i>"). Points are returned sorted by id so the file is
// deterministic.
func metricPoints(ctx context.Context, entry CityEntry, metricKey string, refIDs map[string]bool) ([]MetricPointJSON, error) {
	var points []MetricPointJSON
	for _, spec := range services.Registry {
		if spec.Metric != metricKey {
			continue
		}
		feats, err := entry.Store.ListFeatures(ctx, resource.FacilityType(spec.Name))
		if err != nil {
			return nil, fmt.Errorf("listing %s facilities: %w", spec.Name, err)
		}
		for _, f := range feats {
			if !refIDs[f.ID] {
				continue
			}
			lon, lat, ok := pointLonLat(f)
			if !ok {
				continue
			}
			points = append(points, MetricPointJSON{ID: f.ID, Name: f.Name, Lon: lon, Lat: lat, Source: "osm"})
		}
		for i, fc := range entry.City.Facilities {
			id := fmt.Sprintf("config:%s:%d", spec.Name, i)
			if fc.Service != spec.Name || !refIDs[id] {
				continue
			}
			points = append(points, MetricPointJSON{ID: id, Name: fc.Name, Lon: fc.Lon, Lat: fc.Lat, Source: "config"})
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].ID < points[j].ID })
	return points, nil
}

func pointLonLat(f db.Feature) (float64, float64, bool) {
	var pt struct {
		Type        string     `json:"type"`
		Coordinates [2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(f.GeometryJSON), &pt); err != nil || pt.Type != "Point" {
		return 0, 0, false
	}
	return pt.Coordinates[0], pt.Coordinates[1], true
}

// buildMetricSummaries returns meta.json's per-metric summaries over every
// stored row, or nil when no metric has rows or the city has no hex grid to
// paint them on. Any store error propagates.
//
// Every row is a hex the map paints: hexgrid.geojson (v4) carries every hex
// of the clipped grid the producers compute on, whether or not it holds
// pavement. (The export's sliver filter can drop a handful of edge hexes
// below min_hex_area; their rows are a rounding error in these figures.)
func buildMetricSummaries(ctx context.Context, entry CityEntry) (map[string]MetricSummaryJSON, error) {
	var out map[string]MetricSummaryJSON
	checkedGrid := false // checked lazily: most cities have no metrics
	for _, spec := range hexmetric.StoredSpecs() {
		rows, err := entry.Store.ListHexMetrics(ctx, spec.Key)
		if err != nil {
			return nil, fmt.Errorf("listing hex metric %s: %w", spec.Key, err)
		}
		if len(rows) == 0 {
			continue
		}
		if !checkedGrid {
			hasGrid, err := hasHexGrid(ctx, entry)
			if err != nil {
				return nil, err
			}
			if !hasGrid {
				return nil, nil
			}
			checkedGrid = true
		}
		_, threshold, hasThreshold := resolvedScale(entry, spec)
		sum, ok := summarizeMetric(spec, rows, threshold, hasThreshold)
		if !ok {
			continue
		}
		if out == nil {
			out = map[string]MetricSummaryJSON{}
		}
		out[spec.Key] = sum
	}
	return out, nil
}

// summarizeMetric reduces one metric's rows; ok is false when there are none.
func summarizeMetric(spec hexmetric.Spec, rows []db.HexMetric, threshold float64, hasThreshold bool) (MetricSummaryJSON, bool) {
	var vals []float64
	within := 0
	refs := map[string]bool{}
	for _, r := range rows {
		vals = append(vals, r.Value)
		if hasThreshold && r.Value <= threshold {
			within++
		}
		if r.Ref != "" {
			refs[r.Ref] = true
		}
	}
	if len(vals) == 0 {
		return MetricSummaryJSON{}, false
	}
	sort.Float64s(vals)
	sum := MetricSummaryJSON{
		Label: spec.Label, Unit: spec.Unit, Format: spec.Format,
		Hexes: len(vals), Min: round2(vals[0]), Median: round2(vals[len(vals)/2]), Max: round2(vals[len(vals)-1]),
		Points: len(refs),
	}
	if hasThreshold {
		sum.ThresholdS = threshold
		sum.PctWithin = round2(float64(within) / float64(len(vals)) * 100)
	}
	return sum, true
}

// hasHexGrid reports whether hexgrid.geojson will exist for the city: it is
// written only when some resource has hex_stats rows (BuildHexGeoJSON).
func hasHexGrid(ctx context.Context, entry CityEntry) (bool, error) {
	for _, rt := range resource.All {
		for _, scope := range []resource.Scope{resource.ScopeAll, resource.ScopeCity} {
			stats, err := entry.Store.ListHexStats(ctx, rt.Type().With(scope))
			if err != nil {
				return false, fmt.Errorf("listing hex stats for %s: %w", rt.Type().With(scope), err)
			}
			if len(stats) > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}
