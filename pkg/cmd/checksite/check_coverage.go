package checksite

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/population"
)

// minCoverageCorpus is the fewest cities, each with residents and the
// resource, a site needs before checkCoverage compares a city against the
// rest. A small site has no meaningful median.
const minCoverageCorpus = 10

// lowCoverageFraction is the share of the corpus median per-resident area
// below which a city's resource is WARNed as a likely mapping gap. At 10%
// the 407-city release site flags about 12 parking and 21 sidewalk layers,
// mostly LA County cities where OpenStreetMap barely maps lots or
// sidewalks.
const lowCoverageFraction = 0.10

// checkCoverage WARNs on a resource whose paved area per resident is far
// below the same resource's median across the site. Such a layer was
// ingested, so missing_resources does not name it, but it is usually
// OpenStreetMap leaving the city's lots or sidewalks unmapped rather than a
// city that has none. The per-resident figures, rankings and forecasts then
// undercount the city. It is a WARN: some cities really have almost none
// (Rolling Hills has no sidewalks).
//
// A city that appears in several examples counts once. A resource that is
// missing, partial (ingest_failures) or zero is skipped: checkMeta and the
// zero-area forecast warning already name it, with their own cause. A
// meta.json that cannot be read is skipped quietly, since checkMeta fails it.
func (r *runner) checkCoverage(s *site) {
	byRes := r.perResidentByResource(s)
	for _, rt := range slices.Sorted(maps.Keys(byRes)) {
		r.judgeCoverage(rt, byRes[rt])
	}
}

// cityCoverage is one city's paved area per resident for one resource.
type cityCoverage struct {
	label    string
	perResid float64
}

// perResidentByResource reads every city's meta.json once (by slug) and
// returns each resource's per-resident areas, leaving out cities without
// residents and resources the city is missing.
func (r *runner) perResidentByResource(s *site) map[string][]cityCoverage {
	byRes := make(map[string][]cityCoverage)
	seen := make(map[string]bool)
	for _, d := range s.dataDirsWithLabel() {
		if seen[d.slug] {
			continue
		}
		var meta export.MetaJSON
		data, err := os.ReadFile(filepath.Join(d.dir, export.MetaFileName))
		if err != nil || json.Unmarshal(data, &meta) != nil {
			continue
		}
		seen[d.slug] = true
		pop, ok := meta.Metrics[population.KeyPopulation]
		if !ok || pop.Total == nil || *pop.Total <= 0 {
			continue
		}
		for _, st := range meta.Stats {
			_, partial := meta.IngestFailures[st.ResourceType]
			if st.TotalArea > 0 && !partial && !slices.Contains(meta.MissingResources, st.ResourceType) {
				byRes[st.ResourceType] = append(byRes[st.ResourceType], cityCoverage{d.label, st.TotalArea / *pop.Total})
			}
		}
	}
	return byRes
}

// judgeCoverage WARNs on each city whose per-resident area for rt is under
// lowCoverageFraction of the corpus median, or PASSes the resource.
func (r *runner) judgeCoverage(rt string, cities []cityCoverage) {
	if len(cities) < minCoverageCorpus {
		return
	}
	vals := make([]float64, len(cities))
	for i, c := range cities {
		vals[i] = c.perResid
	}
	sort.Float64s(vals)
	med := export.Median(vals)
	if med <= 0 {
		return
	}
	floor := lowCoverageFraction * med
	low := 0
	sort.Slice(cities, func(i, j int) bool { return cities[i].label < cities[j].label })
	for _, c := range cities {
		if c.perResid >= floor {
			continue
		}
		low++
		r.warnf("coverage: %s [%s]: %.2f m² per resident, under %.0f%% of the site median (%.1f) — likely unmapped in OpenStreetMap; its per-resident figures undercount",
			c.label, rt, c.perResid, lowCoverageFraction*100, med)
	}
	if low == 0 {
		r.passf("coverage: [%s]: every city is at least %.0f%% of the site median (%.1f m² per resident)", rt, lowCoverageFraction*100, med)
	}
}
