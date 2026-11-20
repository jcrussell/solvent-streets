package export

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/hexmetric"
	"github.com/jcrussell/solvent-streets/internal/metric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/services"
)

// HexMetricsFileVersion is the "v" of hex-metrics.json; bump on any change
// to the shape below (the client refuses unknown versions).
const HexMetricsFileVersion = 2

// HexMetricsJSON is hex-metrics.json: every registered metric that has rows
// for the city's drawn hexes (DrawnGrid), keyed by metric key, each
// self-describing enough for the dashboard to format values and paint hexes
// without metric-specific code.
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
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Position [2]float64 `json:"position"` // [lon, lat]
	Source   string     `json:"source"`
}

// MetricSummaryJSON is the per-metric summary meta.json carries for the stats
// card: computed over the stored rows of the hexes the map draws (DrawnGrid),
// the same rows hex-metrics.json carries.
type MetricSummaryJSON struct {
	Label    string `json:"label"`
	Unit     string `json:"unit"`
	Format   string `json:"format"`
	HexCount int    `json:"hex_count"`
	// Total is the sum over the city's hexes, for additive metrics
	// (hexmetric.Spec.Additive) only.
	Total  *float64 `json:"total,omitempty"`
	Min    float64  `json:"min"`
	Median float64  `json:"median"`
	Max    float64  `json:"max"`
	// PointCount is the points of service the metric references over the
	// drawn hexes: for fire, the stations that are some hex's nearest, inside
	// the city or across its line. For a service it counts only the
	// facilities its run recorded, which are the markers hex-metrics.json
	// carries, so it is InsidePointCount + OutsidePointCount when those are
	// present.
	PointCount int `json:"point_count,omitempty"`
	// PointLabel names those points on the stats panel ("Stations serving the
	// city"), for a service's coverage metric.
	PointLabel string `json:"point_label,omitempty"`
	// InsidePointCount and OutsidePointCount split PointCount by whether the
	// point lies inside the city boundary. Present (0 included) for a
	// service's coverage metric whose boundary could be read.
	InsidePointCount  *int `json:"inside_point_count,omitempty"`
	OutsidePointCount *int `json:"outside_point_count,omitempty"`
	// PointsIncomplete flags a facility list that is probably missing some:
	// no point inside the city serves it, or fewer inside than the service's
	// floor per 100,000 residents (services.Spec.SparsePer100k, against the
	// population summary's total). PointsCaveat is the one line the stats
	// panel shows for it.
	PointsIncomplete bool   `json:"points_incomplete,omitempty"`
	PointsCaveat     string `json:"points_caveat,omitempty"`
	// UnreachableCount is, for a service's coverage metric, the hexes on the
	// road network that no facility reached (services.SubjectUnreachable).
	// They carry no value, so the map leaves them blank, and they are not in
	// HexCount, Min, Median or Max.
	UnreachableCount int `json:"unreachable_count,omitempty"`
	// ThresholdS and WithinThresholdShare describe coverage against the
	// service's resolved threshold (total seconds): the share is of every
	// hex on the network, the unreachable ones included. Omitted for metrics
	// without one.
	ThresholdS           float64  `json:"threshold_s,omitempty"`
	WithinThresholdShare *float64 `json:"within_threshold_share,omitempty"`
}

// resolvedSpec returns the spec (and covered threshold) to publish for a
// metric: the registry's, unless a service produces it, in which case the
// bands and the methodology line (Source) follow the service spec. The turnout is the one the snapshot's run
// for the service recorded (metric.TurnoutS), since its stored times
// include it; the travel threshold, a presentation setting, is the config's
// ([[services]] overrides applied). A run that recorded no turnout falls back
// to the config's. ok is false when the metric has no threshold.
func resolvedSpec(ctx context.Context, entry CityEntry, spec hexmetric.Spec) (hexmetric.Spec, float64, bool, error) {
	svc, ok := services.ForMetric(spec.Key)
	if !ok {
		return spec, 0, false, nil
	}
	if entry.Config != nil {
		svc = services.Resolve(svc, entry.Config.ResolvedServices(&entry.City))
	}
	turnout, found, err := runTurnout(ctx, entry, svc.Name)
	if err != nil {
		return hexmetric.Spec{}, 0, false, err
	}
	if found {
		svc.TurnoutS = turnout
	}
	spec.Scale.Bands = svc.LegendBands()
	spec.Source = svc.SourceText()
	return spec, svc.CoveredS(), true, nil
}

// runTurnout is the turnout the snapshot's run for service recorded; found is
// false when it recorded none.
func runTurnout(ctx context.Context, entry CityEntry, service string) (turnout float64, found bool, err error) {
	subject := db.ServiceProducer(service)
	rows, err := entry.Store.ListCityMetrics(ctx, []string{subject})
	if err != nil {
		return 0, false, fmt.Errorf("listing %s run settings: %w", service, err)
	}
	for _, r := range rows {
		if r.Metric == metric.TurnoutS && r.Subject == subject && r.Class == "" {
			return r.Value, true, nil
		}
	}
	return 0, false, nil
}

// BuildHexMetrics assembles hex-metrics.json for a city: the stored rows of
// the hexes hexgrid.geojson draws, and no others. Returns (nil, nil) when no
// registered metric has rows there, or there is no grid (the file is
// optional: see OptionalDataFileNames), and propagates real store errors so
// the server's cache evicts rather than memoising a blank file.
func BuildHexMetrics(ctx context.Context, entry CityEntry) (*HexMetricsJSON, error) {
	return BuildHexMetricsOn(ctx, entry, NewDrawnGrid(ctx, entry, nil))
}

// BuildHexMetricsOn is BuildHexMetrics over grid, the city's DrawnGrid,
// shared with meta.json and hexgrid.geojson.
func BuildHexMetricsOn(ctx context.Context, entry CityEntry, grid *DrawnGrid) (*HexMetricsJSON, error) {
	out := &HexMetricsJSON{V: HexMetricsFileVersion, Metrics: map[string]MetricJSON{}}
	for _, spec := range hexmetric.StoredSpecs() {
		rows, err := drawnMetricRows(ctx, entry, grid, spec.Key, "")
		if err != nil {
			return nil, err
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

// metricRows lists a stored metric's rows from the entry's resolved snapshot.
// Each metric is its producer's part of that snapshot,
// so it is current by construction: a recompute of roads leaves it in place,
// and a producer rerun that stored nothing hides it (its new, empty run
// replaces the old one) rather than letting older rows resurface.
func metricRows(ctx context.Context, entry CityEntry, key, subject string) ([]db.HexMetric, error) {
	rows, err := entry.Store.ListHexMetrics(ctx, key, subject)
	if err != nil {
		return nil, fmt.Errorf("listing hex metric %s: %w", key, err)
	}
	return rows, nil
}

// drawnMetricRows is metricRows less the rows of hexes the map does not draw
// (DrawnGrid). The grid is built only when there are rows to filter; nil
// when there is no grid at all. A grid that keeps rows (DrawnGrid.keepRows)
// reads each metric once however many files ask for it.
func drawnMetricRows(ctx context.Context, entry CityEntry, grid *DrawnGrid, key, subject string) ([]db.HexMetric, error) {
	read := func() ([]db.HexMetric, error) { return readDrawnMetricRows(ctx, entry, grid, key, subject) }
	if kept := grid.keptRows(key, subject, read); kept != nil {
		return kept()
	}
	return read()
}

// readDrawnMetricRows is drawnMetricRows' read.
func readDrawnMetricRows(ctx context.Context, entry CityEntry, grid *DrawnGrid, key, subject string) ([]db.HexMetric, error) {
	rows, err := metricRows(ctx, entry, key, subject)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	ids, err := grid.ids()
	if err != nil {
		return nil, fmt.Errorf("hex grid for %s: %w", entry.City.Name, err)
	}
	return drawnRows(rows, ids), nil
}

// buildMetricJSON assembles one metric's file entry: the registry
// description, rounded per-hex values, and — when rows reference points —
// the resolved points list with per-hex indexes into it.
func buildMetricJSON(ctx context.Context, entry CityEntry, spec hexmetric.Spec, rows []db.HexMetric) (MetricJSON, error) {
	spec, threshold, _, err := resolvedSpec(ctx, entry, spec)
	if err != nil {
		return MetricJSON{}, err
	}
	m := MetricJSON{Label: spec.Label, Unit: spec.Unit, Format: spec.Format, Source: spec.Source, Scale: spec.Scale, ThresholdS: threshold, Values: make(map[string]float64, len(rows))}
	refIDs := map[string]bool{}
	for _, r := range rows {
		m.Values[r.HexID] = roundMetric(r.Value)
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
// coverage metric references the facilities its run routed from, frozen with
// the run (Store.ListRunFacilities): the ingested OSM points (by feature id)
// and the city's hand-placed [[cities.facilities]] (services.ConfigFacilityID).
// A station moved, added or removed since the compute leaves the markers
// where the coverage was computed from. Points are returned sorted by id so
// the file is deterministic.
func metricPoints(ctx context.Context, entry CityEntry, metricKey string, refIDs map[string]bool) ([]MetricPointJSON, error) {
	var points []MetricPointJSON
	for _, spec := range services.Registry {
		if spec.Metric != metricKey {
			continue
		}
		facilities, err := entry.Store.ListRunFacilities(ctx, db.ServiceProducer(spec.Name))
		if err != nil {
			return nil, fmt.Errorf("listing %s facilities: %w", spec.Name, err)
		}
		for _, f := range facilities {
			if refIDs[f.ID] {
				points = append(points, MetricPointJSON{ID: f.ID, Name: f.Name, Position: [2]float64{f.Lon, f.Lat}, Source: f.Source})
			}
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].ID < points[j].ID })
	return points, nil
}

// buildMetricSummaries returns meta.json's per-metric summaries over the
// rows hex-metrics.json carries (those of the hexes hexgrid.geojson draws:
// DrawnGrid), or nil when no metric has rows there or the city has no hex
// grid. Any store error propagates.
//
// A stored row whose hex the map does not draw (a shoreline or edge sliver
// under min_hex_area, an id outside the clipped grid) is in neither file, so
// the counts, shares and totals here describe what the map shows.
func buildMetricSummaries(ctx context.Context, entry CityEntry, grid *DrawnGrid) (map[string]MetricSummaryJSON, error) {
	var out map[string]MetricSummaryJSON
	for _, spec := range hexmetric.StoredSpecs() {
		rows, err := drawnMetricRows(ctx, entry, grid, spec.Key, "")
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		sum, ok, err := metricSummary(ctx, entry, grid, spec, rows)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if out == nil {
			out = map[string]MetricSummaryJSON{}
		}
		out[spec.Key] = sum
	}
	flagSparsePoints(out)
	return out, nil
}

// flagSparsePoints sets PointsIncomplete and PointsCaveat on each service
// summary whose facility list looks incomplete: no facility inside the city
// boundary serves it, or fewer inside than the service's floor per 100,000
// residents, the population being the census summary's total over the same
// drawn hexes. OpenStreetMap's station coverage is uneven, and a missing
// station reads on the map as a slow or uncovered neighbourhood, so the
// panel says so rather than show the gap as fact. A city without census
// data gets only the none-inside test.
func flagSparsePoints(sums map[string]MetricSummaryJSON) {
	var residents float64
	if pop, ok := sums[metric.Population]; ok && pop.Total != nil {
		residents = *pop.Total
	}
	for key, s := range sums {
		svc, ok := services.ForMetric(key)
		if !ok || s.InsidePointCount == nil {
			continue
		}
		inside := *s.InsidePointCount
		reason := ""
		switch {
		case inside == 0:
			reason = "no " + svc.FacilityNoun + " inside the city serves it"
		case svc.SparsePer100k > 0 && residents > 0 && float64(inside)/residents*1e5 < svc.SparsePer100k:
			reason = "few " + svc.FacilityNoun + "s inside the city for its population"
		}
		if reason == "" {
			continue
		}
		s.PointsIncomplete = true
		s.PointsCaveat = capitalize(svc.FacilityNoun) + " list from OpenStreetMap may be incomplete: " + reason
		sums[key] = s
	}
}

// capitalize upper-cases an ASCII noun's first letter.
func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// servicePoints is a service coverage metric's points: the facilities its
// rows reference that the run recorded (Store.ListRunFacilities), which are
// the markers hex-metrics.json carries (metricPoints), split by whether they
// lie inside the city boundary. A reference to no recorded facility has no
// position, so it is no point: it is in neither the markers nor the counts,
// and inside + outside is always the point count. isService is false when
// the metric is no service's; split is false when the boundary cannot be read
// as a polygon, leaving inside and outside unknown.
func servicePoints(ctx context.Context, entry CityEntry, spec hexmetric.Spec, rows []db.HexMetric) (points, inside, outside int, isService, split bool, err error) {
	svc, isService := services.ForMetric(spec.Key)
	if !isService {
		return 0, 0, 0, false, false, nil
	}
	refs := map[string]bool{}
	for _, r := range rows {
		if r.Ref != "" {
			refs[r.Ref] = true
		}
	}
	facilities, err := entry.Store.ListRunFacilities(ctx, db.ServiceProducer(svc.Name))
	if err != nil {
		return 0, 0, 0, true, false, fmt.Errorf("listing %s facilities: %w", svc.Name, err)
	}
	var pts [][2]float64
	for _, f := range facilities {
		if refs[f.ID] {
			pts = append(pts, [2]float64{f.Lon, f.Lat})
		}
	}
	boundary, err := entry.Store.GetBoundary(ctx)
	if err != nil {
		return 0, 0, 0, true, false, fmt.Errorf("boundary for %s: %w", entry.City.Name, err)
	}
	in, err := geo.PointsInBoundary(boundary, pts)
	if err != nil {
		return len(pts), 0, 0, true, false, nil //nolint:nilerr // no readable boundary: the split is unknown, not an export failure
	}
	for _, b := range in {
		if b {
			inside++
		} else {
			outside++
		}
	}
	return len(pts), inside, outside, true, true, nil
}

// metricSummary summarises one metric's rows against its resolved
// threshold, counting a service's unreachable hexes (rows under
// services.SubjectUnreachable, of drawn hexes only) against the
// within-threshold share.
func metricSummary(ctx context.Context, entry CityEntry, grid *DrawnGrid, spec hexmetric.Spec, rows []db.HexMetric) (MetricSummaryJSON, bool, error) {
	_, threshold, hasThreshold, err := resolvedSpec(ctx, entry, spec)
	if err != nil {
		return MetricSummaryJSON{}, false, err
	}
	var unreachable []db.HexMetric
	if hasThreshold {
		if unreachable, err = drawnMetricRows(ctx, entry, grid, spec.Key, services.SubjectUnreachable); err != nil {
			return MetricSummaryJSON{}, false, fmt.Errorf("listing unreachable hexes of %s: %w", spec.Key, err)
		}
	}
	sum, ok := summarizeMetric(spec, rows, len(unreachable), threshold, hasThreshold)
	if !ok {
		return sum, false, nil
	}
	points, inside, outside, isService, split, err := servicePoints(ctx, entry, spec, rows)
	if err != nil {
		return MetricSummaryJSON{}, false, err
	}
	if isService {
		// The points the map marks, so the count, the split and the markers
		// agree (servicePoints).
		sum.PointCount = points
	}
	if split {
		svc, _ := services.ForMetric(spec.Key)
		sum.PointLabel = capitalize(svc.FacilityNoun) + "s serving the city"
		sum.InsidePointCount, sum.OutsidePointCount = &inside, &outside
	}
	return sum, true, nil
}

// Median is the middle of sorted vals, or the mean of the two middle values
// for an even count. vals must be non-empty.
func Median(vals []float64) float64 {
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2]
	}
	return (vals[n/2-1] + vals[n/2]) / 2
}

// summarizeMetric reduces one metric's rows; ok is false when there are none.
// unreachable is the count of hexes on the network with no value (a
// service's unreachable hexes): they count against the within-threshold
// share and nowhere else.
func summarizeMetric(spec hexmetric.Spec, rows []db.HexMetric, unreachable int, threshold float64, hasThreshold bool) (MetricSummaryJSON, bool) {
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
		HexCount: len(vals), Min: roundMetric(vals[0]), Median: roundMetric(Median(vals)), Max: roundMetric(vals[len(vals)-1]),
		PointCount: len(refs),
	}
	if spec.Additive {
		var total float64
		for _, v := range vals {
			total += v
		}
		total = roundMetric(total)
		sum.Total = &total
	}
	if hasThreshold {
		sum.ThresholdS = threshold
		sum.UnreachableCount = unreachable
		share := round4(float64(within) / float64(len(vals)+unreachable))
		sum.WithinThresholdShare = &share
	}
	return sum, true
}

// roundMetric rounds a stored metric's value for emission: two decimals at a
// magnitude of 1 or more (seconds, residents), four significant figures
// below it, so a small-unit value (residents per m², about 0.0003 to 0.02)
// keeps its digits instead of flattening to 0 or 0.01.
func roundMetric(v float64) float64 {
	if math.Abs(v) >= 1 {
		return round2(v)
	}
	return roundSig(v, 4)
}

// hasHexGrid reports whether hexgrid.geojson will exist for the city: it is
// written only when some resource has per-hex coverage (BuildHexGeoJSON). One
// existence query over every resource and scope, not a load of the rows.
func hasHexGrid(ctx context.Context, entry CityEntry) (bool, error) {
	types := resource.AllTypes(resource.ScopeAll, resource.ScopeCity)
	ok, err := results.HasHexCoverage(ctx, entry.Store, types)
	if err != nil {
		return false, fmt.Errorf("checking for hex stats: %w", err)
	}
	return ok, nil
}

// MetricSummaries is meta.json's per-metric summaries for entry, exactly as
// the site's Map stats card shows them (its "Within 5:20" share is
// WithinThresholdShare): `pvmt snapshots diff` compares two snapshots with it.
func MetricSummaries(ctx context.Context, entry CityEntry) (map[string]MetricSummaryJSON, error) {
	return buildMetricSummaries(ctx, entry, NewDrawnGrid(ctx, entry, nil))
}
