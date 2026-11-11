package export

import (
	"maps"
	"slices"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// totalsFixture returns the city_metrics rows of per-type city totals, for
// seeding dbtest.MockStore.CityMetrics.
func totalsFixture(totals map[resource.Type]results.Totals) []db.CityMetric {
	var rows []db.CityMetric
	for _, t := range slices.Sorted(maps.Keys(totals)) {
		rows = append(rows, results.TotalRows(t, totals[t])...)
	}
	return rows
}

// cohortsFixture returns the city_metrics rows of per-type cohort splits.
func cohortsFixture(cohorts map[resource.Type][]results.Cohort) []db.CityMetric {
	var rows []db.CityMetric
	for _, t := range slices.Sorted(maps.Keys(cohorts)) {
		rows = append(rows, results.CohortRows(t, cohorts[t])...)
	}
	return rows
}

// pavementTypes is every pavement resource in both scopes.
func pavementTypes() []resource.Type {
	var out []resource.Type
	for _, rt := range resource.All {
		out = append(out, rt.Type(), rt.Type().With(resource.ScopeCity))
	}
	return out
}

// coverageFixture returns the same per-hex coverage for each of types, for
// seeding dbtest.MockStore.HexMetrics.
func coverageFixture(types []resource.Type, cov []results.HexCoverage) []db.HexMetric {
	var rows []db.HexMetric
	for _, t := range types {
		rows = append(rows, results.HexCoverageRows(t, cov)...)
	}
	return rows
}
