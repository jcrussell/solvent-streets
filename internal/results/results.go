// Package results reads and writes the pavement compute's results as the
// generic metric rows the database stores (db.CityMetric, db.HexMetric), using
// the keys and subject convention of internal/resource/metric.go. It is the one
// place that knows how a resource's city total, cohort split and per-hex
// coverage map onto metric rows.
package results

import (
	"context"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// Totals is one resource's city-wide result: the city_metrics rows of class
// "" over the resource's subject.
type Totals struct {
	Area     float64 // m^2
	Features int
	// Spaces is the estimated parking capacity (resource.EstimateParkingSpaces
	// summed over the hex-clipped lots); written for parking subjects only,
	// so 0 for every other resource.
	Spaces     int
	RunID      int64
	ComputedAt time.Time
}

// Cohort is one highway class's share of a resource's paved area: the
// city_metrics rows of that class. Resources without a cohort split carry
// one cohort named after the resource ("parking", "sidewalks").
type Cohort struct {
	Class    string
	Area     float64 // m^2
	Features int
}

// HexCoverage is one hex's paved area and paved share for a resource.
type HexCoverage struct {
	HexID      string
	Area       float64 // m^2
	PctCovered float64 // percent
}

// CityMetricLister is the read surface the city-wide loaders need.
type CityMetricLister interface {
	ListCityMetrics(ctx context.Context, subjects []string) ([]db.CityMetric, error)
}

// HexMetricLister is the read surface the per-hex loaders need.
type HexMetricLister interface {
	ListHexMetrics(ctx context.Context, metric, subject string) ([]db.HexMetric, error)
	HasHexMetric(ctx context.Context, metric string, subjects []string) (bool, error)
}

func subject(t resource.Type) string { return resource.Subject(t) }

func subjects(types []resource.Type) []string {
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = subject(t)
	}
	return out
}

// TotalRows returns t's city totals as metric rows. Every total is written,
// even when 0 (a missing row means "never computed"); Spaces only for parking.
func TotalRows(t resource.Type, tot Totals) []db.CityMetric {
	s := subject(t)
	rows := []db.CityMetric{
		{Metric: resource.MetricPavedArea, Subject: s, Value: tot.Area},
		{Metric: resource.MetricFeatureCount, Subject: s, Value: float64(tot.Features)},
	}
	if t.Bare() == resource.TypeParking {
		rows = append(rows, db.CityMetric{Metric: resource.MetricParkingSpaces, Subject: s, Value: float64(tot.Spaces)})
	}
	return rows
}

// CohortRows returns t's cohort split as metric rows. A cohort's Class must
// not be "" (that class is the city total).
func CohortRows(t resource.Type, cohorts []Cohort) []db.CityMetric {
	s := subject(t)
	rows := make([]db.CityMetric, 0, 2*len(cohorts))
	for _, c := range cohorts {
		rows = append(rows,
			db.CityMetric{Metric: resource.MetricPavedArea, Subject: s, Class: c.Class, Value: c.Area},
			db.CityMetric{Metric: resource.MetricFeatureCount, Subject: s, Class: c.Class, Value: float64(c.Features)},
		)
	}
	return rows
}

// HexCoverageRows returns t's per-hex coverage as metric rows.
func HexCoverageRows(t resource.Type, cov []HexCoverage) []db.HexMetric {
	s := subject(t)
	rows := make([]db.HexMetric, 0, 2*len(cov))
	for _, h := range cov {
		rows = append(rows,
			db.HexMetric{HexID: h.HexID, Metric: resource.MetricPavedArea, Subject: s, Value: h.Area},
			db.HexMetric{HexID: h.HexID, Metric: resource.MetricCoveragePct, Subject: s, Value: h.PctCovered},
		)
	}
	return rows
}

// Pivot splits city metric rows into per-type totals and cohort splits.
// Types missing from rows are absent from both maps. A snapshot holds one run
// per part, so one run writes each subject; should two runs both write it,
// the newer run wins. Cohorts keep the rows' order (by class).
func Pivot(rows []db.CityMetric) (map[resource.Type]Totals, map[resource.Type][]Cohort) {
	totals := map[resource.Type]Totals{}
	cohorts := map[resource.Type][]Cohort{}
	cohortRun := map[resource.Type]int64{}
	for _, m := range rows {
		t := resource.Type(m.Subject)
		if m.Class == "" {
			addTotal(totals, t, m)
		} else {
			addCohort(cohorts, cohortRun, t, m)
		}
	}
	return totals, cohorts
}

// addTotal folds one class-"" row into totals[t], restarting it when the row
// comes from a newer run and skipping rows of an older one.
func addTotal(totals map[resource.Type]Totals, t resource.Type, m db.CityMetric) {
	tot, ok := totals[t]
	switch {
	case !ok || m.RunID > tot.RunID:
		tot = Totals{RunID: m.RunID, ComputedAt: m.ComputedAt}
	case m.RunID < tot.RunID:
		return
	}
	switch m.Metric {
	case resource.MetricPavedArea:
		tot.Area = m.Value
	case resource.MetricFeatureCount:
		tot.Features = int(m.Value)
	case resource.MetricParkingSpaces:
		tot.Spaces = int(m.Value)
	}
	totals[t] = tot
}

// addCohort folds one cohort row into cohorts[t] (rows arrive ordered by
// class), restarting the split when the row comes from a newer run than
// cohortRun[t] and skipping rows of an older one.
func addCohort(cohorts map[resource.Type][]Cohort, cohortRun map[resource.Type]int64, t resource.Type, m db.CityMetric) {
	if m.Metric != resource.MetricPavedArea && m.Metric != resource.MetricFeatureCount {
		return
	}
	switch run, ok := cohortRun[t]; {
	case !ok || m.RunID > run:
		cohortRun[t], cohorts[t] = m.RunID, nil
	case m.RunID < run:
		return
	}
	list := cohorts[t]
	if n := len(list); n == 0 || list[n-1].Class != m.Class {
		list = append(list, Cohort{Class: m.Class})
	}
	c := &list[len(list)-1]
	if m.Metric == resource.MetricPavedArea {
		c.Area = m.Value
	} else {
		c.Features = int(m.Value)
	}
	cohorts[t] = list
}

// Load reads the totals and cohort splits of types from the resolved
// snapshot in one query.
func Load(ctx context.Context, s CityMetricLister, types []resource.Type) (map[resource.Type]Totals, map[resource.Type][]Cohort, error) {
	rows, err := s.ListCityMetrics(ctx, subjects(types))
	if err != nil {
		return nil, nil, err
	}
	totals, cohorts := Pivot(rows)
	return totals, cohorts, nil
}

// LoadTotals reads the city totals of types. Types never computed are absent.
func LoadTotals(ctx context.Context, s CityMetricLister, types []resource.Type) (map[resource.Type]Totals, error) {
	totals, _, err := Load(ctx, s, types)
	return totals, err
}

// LoadTotal reads t's city totals; ok is false when t was never computed.
func LoadTotal(ctx context.Context, s CityMetricLister, t resource.Type) (tot Totals, ok bool, err error) {
	totals, err := LoadTotals(ctx, s, []resource.Type{t})
	if err != nil {
		return Totals{}, false, err
	}
	tot, ok = totals[t]
	return tot, ok, nil
}

// LoadCohorts reads t's cohort split, ordered by class; nil when t has none.
func LoadCohorts(ctx context.Context, s CityMetricLister, t resource.Type) ([]Cohort, error) {
	_, cohorts, err := Load(ctx, s, []resource.Type{t})
	return cohorts[t], err
}

// LoadHexCoverage reads t's per-hex coverage, ordered by hex id; nil when t
// has none.
func LoadHexCoverage(ctx context.Context, s HexMetricLister, t resource.Type) ([]HexCoverage, error) {
	area, err := s.ListHexMetrics(ctx, resource.MetricPavedArea, subject(t))
	if err != nil {
		return nil, err
	}
	pct, err := s.ListHexMetrics(ctx, resource.MetricCoveragePct, subject(t))
	if err != nil {
		return nil, err
	}
	pctByHex := make(map[string]float64, len(pct))
	for _, m := range pct {
		pctByHex[m.HexID] = m.Value
	}
	var out []HexCoverage
	for _, m := range area {
		out = append(out, HexCoverage{HexID: m.HexID, Area: m.Value, PctCovered: pctByHex[m.HexID]})
	}
	return out, nil
}

// HasHexCoverage reports whether any of types has per-hex coverage.
func HasHexCoverage(ctx context.Context, s HexMetricLister, types []resource.Type) (bool, error) {
	return s.HasHexMetric(ctx, resource.MetricPavedArea, subjects(types))
}

// LoadCohortsByType reads the cohort splits of types in one query. Types
// without a split are absent.
func LoadCohortsByType(ctx context.Context, s CityMetricLister, types []resource.Type) (map[resource.Type][]Cohort, error) {
	_, cohorts, err := Load(ctx, s, types)
	return cohorts, err
}
