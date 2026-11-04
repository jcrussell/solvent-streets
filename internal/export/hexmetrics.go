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

// MetricJSON is one metric's rows plus its registry description.
type MetricJSON struct {
	Label  string          `json:"label"`
	Unit   string          `json:"unit"`
	Format string          `json:"format"`
	Source string          `json:"source"`
	Scale  hexmetric.Scale `json:"scale"`
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
// card: computed over the hexes that also have pavement rows, so the figures
// describe exactly the hexes the map paints.
type MetricSummaryJSON struct {
	Label  string  `json:"label"`
	Unit   string  `json:"unit"`
	Format string  `json:"format"`
	Hexes  int     `json:"hexes"`
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	Max    float64 `json:"max"`
	Points int     `json:"points,omitempty"`
}

// BuildHexMetrics assembles hex-metrics.json for a city. Returns (nil, nil)
// when no registered metric has rows (the file is optional: see
// OptionalDataFileNames), and propagates real store errors so the server's
// cache evicts rather than memoising a blank file.
func BuildHexMetrics(ctx context.Context, entry CityEntry) (*HexMetricsJSON, error) {
	out := &HexMetricsJSON{V: HexMetricsFileVersion, Metrics: map[string]MetricJSON{}}
	for _, spec := range hexmetric.All() {
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
	m := MetricJSON{Label: spec.Label, Unit: spec.Unit, Format: spec.Format, Source: spec.Source, Scale: spec.Scale, Values: make(map[string]float64, len(rows))}
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

// buildMetricSummaries returns meta.json's per-metric summaries over the
// hexes that carry pavement rows (the ones hexgrid.geojson paints), or nil
// when no metric has rows. Any store error propagates.
func buildMetricSummaries(ctx context.Context, entry CityEntry) (map[string]MetricSummaryJSON, error) {
	painted, err := paintedHexIDs(ctx, entry)
	if err != nil {
		return nil, err
	}
	var out map[string]MetricSummaryJSON
	for _, spec := range hexmetric.All() {
		rows, err := entry.Store.ListHexMetrics(ctx, spec.Key)
		if err != nil {
			return nil, fmt.Errorf("listing hex metric %s: %w", spec.Key, err)
		}
		var vals []float64
		refs := map[string]bool{}
		for _, r := range rows {
			if !painted[r.HexID] {
				continue
			}
			vals = append(vals, r.Value)
			if r.Ref != "" {
				refs[r.Ref] = true
			}
		}
		if len(vals) == 0 {
			continue
		}
		sort.Float64s(vals)
		if out == nil {
			out = map[string]MetricSummaryJSON{}
		}
		out[spec.Key] = MetricSummaryJSON{
			Label: spec.Label, Unit: spec.Unit, Format: spec.Format,
			Hexes: len(vals), Min: round2(vals[0]), Median: round2(vals[len(vals)/2]), Max: round2(vals[len(vals)-1]),
			Points: len(refs),
		}
	}
	return out, nil
}

// paintedHexIDs is the set of hex ids hexgrid.geojson will contain: every hex
// with a hex_stats row for any resource and scope.
func paintedHexIDs(ctx context.Context, entry CityEntry) (map[string]bool, error) {
	ids := map[string]bool{}
	for _, rt := range resource.All {
		for _, scope := range []resource.Scope{resource.ScopeAll, resource.ScopeCity} {
			stats, err := entry.Store.ListHexStats(ctx, rt.Type().With(scope))
			if err != nil {
				return nil, fmt.Errorf("listing hex stats for %s: %w", rt.Type().With(scope), err)
			}
			for _, st := range stats {
				ids[st.HexID] = true
			}
		}
	}
	return ids, nil
}
