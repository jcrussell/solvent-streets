package export

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// BuildMultiCityMeta aggregates each sub-city's per-resource compute results
// and boundary areas into a single config-wide MetaJSON for the multi-city
// dashboard. Per-resource Stats sum across entries; CityArea is the
// sum of per-city boundary areas, each computed in its own UTM zone so that
// far-apart sub-cities (different UTM zones) are not biased by a single
// shared projection. TotalPaved prefers the summed "combined" rows with
// a fallback to summed per-resource rows.
func BuildMultiCityMeta(ctx context.Context, entries []CityEntry, title string) (MetaJSON, error) {
	if len(entries) == 0 {
		return MetaJSON{}, errors.New("no entries to aggregate")
	}
	bbox, hasBBox, err := regionBBox(ctx, entries)
	if err != nil {
		return MetaJSON{}, err
	}
	if !hasBBox {
		return MetaJSON{}, errors.New("no usable boundaries across entries")
	}
	centerLon, centerLat := geo.CenterFromBBox(bbox)
	stats, err := aggregatePerResourceStats(ctx, entries)
	if err != nil {
		return MetaJSON{}, err
	}

	meta := MetaJSON{
		ProjectName:  title,
		BBox:         geoJSONBBox(bbox),
		Center:       [2]float64{centerLon, centerLat},
		SnapshotDate: time.Now().Format(time.DateOnly),
		Stats:        stats,
	}
	for _, entry := range entries {
		// Per entry, so a config where only some cities have the combined
		// total still counts the others, by their per-resource sum.
		paved, _, err := results.PavedArea(ctx, entry.Store, resource.ScopeAll)
		if err != nil {
			return MetaJSON{}, fmt.Errorf("paved area for %s: %w", entry.City.Name, err)
		}
		meta.TotalPaved += paved
	}
	if meta.CityArea, err = summedBoundaryArea(ctx, entries); err != nil {
		return MetaJSON{}, err
	}
	if meta.CityArea > 0 && meta.TotalPaved > 0 {
		meta.PavedShare = meta.TotalPaved / meta.CityArea
	}
	return meta, nil
}

// aggregatePerResourceStats sums TotalArea and EstimatedSpaces per resource
// type across all entries, returning the per-resource cards in resource.All
// order. Resources with no rows in any entry are omitted.
//
// Reads only rt.Name() (no ":city" variants): the per-resource cards on the
// dashboard surface all-jurisdiction totals, matching single-city BuildMeta.
//
// An entry whose results cannot be read is skipped, except when its pinned
// snapshot is gone (db.ErrSnapshotGone): that is returned, so a snapshot
// deleted mid-export fails the export rather than dropping the city from the
// totals. The same holds for summedBoundaryArea and regionBBox.
func aggregatePerResourceStats(ctx context.Context, entries []CityEntry) ([]StatJSON, error) {
	types := resource.AllTypes(resource.ScopeAll)
	statByType := make(map[string]*StatJSON)
	for _, entry := range entries {
		// One batched lookup per entry. A type missing from the returned map
		// has not been computed and contributes nothing.
		latestByType, err := results.LoadTotals(ctx, entry.Store, types)
		if errors.Is(err, db.ErrSnapshotGone) {
			return nil, fmt.Errorf("%s: %w", entry.City.Name, err)
		}
		if err != nil {
			continue
		}
		for _, rt := range resource.All {
			t := rt.Type()
			result, ok := latestByType[t]
			if !ok {
				continue
			}
			kindName := string(t)
			st, ok := statByType[kindName]
			if !ok {
				st = &StatJSON{ResourceType: kindName, Color: ResourceColors[kindName]}
				statByType[kindName] = st
			}
			st.TotalArea += result.Area
			st.EstimatedSpaces += result.Spaces
		}
	}
	out := []StatJSON{}
	for _, rt := range resource.All {
		if st, ok := statByType[string(rt.Type())]; ok {
			out = append(out, *st)
		}
	}
	return out, nil
}

// summedBoundaryArea computes each sub-city's boundary area in its own UTM
// zone (so far-apart sub-cities are not biased by a single shared projection)
// and sums them. Returns 0 when no usable boundaries are available.
//
// Sub-city boundaries are assumed to be disjoint, which holds for the typical
// multi-metro config (Bay Area, statewide rollups). Overlapping sub-city
// boundaries (e.g. a parent jurisdiction plus its child) would be double-
// counted; the recommended workaround for that case is to configure a single
// boundary that already contains the children.
func summedBoundaryArea(ctx context.Context, entries []CityEntry) (float64, error) {
	var total float64
	for _, entry := range entries {
		gjson, err := entry.Store.GetBoundary(ctx)
		if errors.Is(err, db.ErrSnapshotGone) {
			return 0, fmt.Errorf("%s: %w", entry.City.Name, err)
		}
		if err != nil || gjson == "" {
			continue
		}
		a, err := geo.BoundaryArea(cityLogCtx(ctx, entry), gjson)
		if err != nil {
			continue
		}
		total += a
	}
	return total, nil
}

// regionBBox returns the union of sub-city bboxes ([south, west, north, east]).
func regionBBox(ctx context.Context, entries []CityEntry) ([4]float64, bool, error) {
	var out [4]float64
	first := true
	for _, entry := range entries {
		bb, _, _, err := entry.BBoxAndCenter(ctx)
		if errors.Is(err, db.ErrSnapshotGone) {
			return out, false, err
		}
		if err != nil {
			continue
		}
		if first {
			out = bb
			first = false
			continue
		}
		if bb[0] < out[0] {
			out[0] = bb[0]
		}
		if bb[1] < out[1] {
			out[1] = bb[1]
		}
		if bb[2] > out[2] {
			out[2] = bb[2]
		}
		if bb[3] > out[3] {
			out[3] = bb[3]
		}
	}
	return out, !first, nil
}

// BuildMultiCityForecastSeed aggregates per-city forecast seeds into one
// regional ForecastSeedJSON. MaintainedArea and CityMaintainedArea sum each
// entry's maintained areas (maintainedAreas).
// Cohort areas are summed across cities for each (resource_type, classification)
// pair — cross-resource collisions stay as separate cohorts to match the
// single-city collectCohortSeeds shape.
func BuildMultiCityForecastSeed(ctx context.Context, fc *config.ForecastConfig, entries []CityEntry) (template.JS, error) {
	var areas scenarioAreas
	for _, entry := range entries {
		a, err := maintainedAreas(ctx, entry.Store)
		if err != nil {
			return "", fmt.Errorf("maintained area for %s: %w", entry.City.Name, err)
		}
		areas.allArea += a.allArea
		areas.cityArea += a.cityArea
	}
	var cohorts cohortSet
	var err error
	if cohorts.All, cohorts.AllAsphaltShare, err = mergeCohortSeeds(ctx, entries, fc, false); err != nil {
		return "", err
	}
	if cohorts.City, cohorts.CityAsphaltShare, err = mergeCohortSeeds(ctx, entries, fc, true); err != nil {
		return "", err
	}
	return marshalForecastSeed(fc, areas, cohorts)
}

// mergeCohortSeeds collects cohort stats across entries and sums areas for
// the same (resource_type, classification, decay rate). cityScope=true reads ":city"
// cohort rows. Cross-resource collisions (e.g. roads "default" vs parking
// "default") stay as separate cohorts — matching collectCohortSeeds' single-
// city shape, where each cohort row of rt appears verbatim and a
// classification can recur across resources.
func mergeCohortSeeds(ctx context.Context, entries []CityEntry, fc *config.ForecastConfig, cityScope bool) ([]CohortSeed, float64, error) {
	// DecayRate is part of the key: a city whose [cities.forecast] (or whose
	// included file's [forecast]) sets its own decay_rate keeps its cohorts
	// apart at that rate, as its own forecast.json prices them, instead of
	// being folded into the region's rate.
	type key struct {
		Resource       string
		Classification string
		DecayRate      float64
	}
	type bucket struct {
		Order int
		Seed  CohortSeed
	}
	buckets := make(map[key]*bucket)
	// Asphalt-vs-total cohort area for this scope, summed across every entry —
	// same basis and same purpose as the single-city split in
	// collectCohortSeeds. A region's sidewalk share is not the average of its
	// cities' shares, so it has to be accumulated here rather than blended.
	var split areaSplit
	nextOrder := 0
	scope := resource.ScopeAll
	if cityScope {
		scope = resource.ScopeCity
	}
	scopedTypes := resource.AllTypes(scope)
	for _, entry := range entries {
		// One batched cohort-stats lookup per entry; a scoped type absent
		// from the map is skipped, the same as a type with no stats.
		// Iterate resource.All (not the map) so first-seen Order stays
		// deterministic, in resource.All order.
		statsByType, err := results.LoadCohortsByType(ctx, entry.Store, scopedTypes)
		if err != nil {
			return nil, 0, fmt.Errorf("list cohort stats for %s: %w", entry.City.Name, err)
		}
		decayRate := entryDecayRate(entry, fc)
		for _, rt := range resource.All {
			t := rt.Type()
			stats := statsByType[t.With(scope)]
			asphalt := rt.AsphaltSurfaced()
			for _, st := range stats {
				split.add(st.Area, asphalt)
				rate := forecast.ClassDecayRate(st.Class, decayRate)
				k := key{Resource: string(t), Classification: st.Class, DecayRate: rate}
				b, ok := buckets[k]
				if !ok {
					b = &bucket{
						Order: nextOrder,
						Seed: CohortSeed{
							ResourceType:   k.Resource,
							Classification: st.Class,
							DecayRate:      rate,
						},
					}
					buckets[k] = b
					nextOrder++
				}
				b.Seed.Area += st.Area
			}
		}
	}
	if len(buckets) == 0 {
		return nil, split.share(), nil
	}
	out := make([]CohortSeed, len(buckets))
	for _, b := range buckets {
		out[b.Order] = b.Seed
	}
	return out, split.share(), nil
}

// entryDecayRate is the decay_rate a city's own forecast resolves to — the
// rate its forecast.json uses — falling back to the region's fc when the
// entry carries no config.
func entryDecayRate(entry CityEntry, fc *config.ForecastConfig) float64 {
	if entry.Config == nil {
		return fc.DecayRate
	}
	return entry.Config.ResolvedForecast(&entry.City).DecayRate
}
