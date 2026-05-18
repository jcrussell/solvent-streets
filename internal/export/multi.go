package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// BuildMultiCityMeta aggregates each sub-city's per-resource compute results
// and boundary areas into a single regional MetaJSON for the multi-city
// landing page. Per-resource Stats sum across entries; CityAreaSqM is the
// sum of per-city boundary areas, each computed in its own UTM zone so that
// far-apart sub-cities (different UTM zones) are not biased by a single
// shared projection. TotalPavedSqM prefers the summed "combined" rows with
// a fallback to summed per-resource rows.
func BuildMultiCityMeta(ctx context.Context, entries []CityEntry, regionName string) (MetaJSON, error) {
	if len(entries) == 0 {
		return MetaJSON{}, errors.New("no entries to aggregate")
	}
	bbox, hasBBox := regionBBox(ctx, entries)
	if !hasBBox {
		return MetaJSON{}, errors.New("no usable boundaries across entries")
	}
	centerLon, centerLat := geo.CenterFromBBox(bbox)

	meta := MetaJSON{
		ProjectName:  regionName,
		BBox:         bbox,
		CenterLon:    centerLon,
		CenterLat:    centerLat,
		SnapshotDate: time.Now().Format("2006-01-02"),
		Stats:        aggregatePerResourceStats(ctx, entries),
	}
	meta.TotalPavedSqM = aggregateTotalPaved(ctx, entries)
	meta.CityAreaSqM = summedBoundaryArea(ctx, entries)
	if meta.CityAreaSqM > 0 && meta.TotalPavedSqM > 0 {
		meta.PctPaved = meta.TotalPavedSqM / meta.CityAreaSqM * 100
	}
	return meta, nil
}

// aggregatePerResourceStats sums TotalAreaSqM and FeatureCount per resource
// type across all entries, returning the per-resource cards in resource.All
// order. Resources with no rows in any entry are omitted.
//
// Reads only rt.Name() (no ":city" variants): the per-resource cards on the
// landing page surface all-jurisdiction totals, matching single-city BuildMeta.
func aggregatePerResourceStats(ctx context.Context, entries []CityEntry) []StatJSON {
	statByType := make(map[string]*StatJSON)
	for _, entry := range entries {
		for _, rt := range resource.All {
			kindName := string(rt.Type())
			result, err := entry.Store.LatestComputeResult(ctx, rt.Type())
			if err != nil || result == nil {
				continue
			}
			st, ok := statByType[kindName]
			if !ok {
				st = &StatJSON{Type: kindName, Color: ResourceColors[kindName]}
				statByType[kindName] = st
			}
			st.TotalAreaSqM += result.TotalAreaSqM
			st.FeatureCount += result.FeatureCount
		}
	}
	var out []StatJSON
	for _, rt := range resource.All {
		if st, ok := statByType[string(rt.Type())]; ok {
			out = append(out, *st)
		}
	}
	return out
}

// aggregateTotalPaved sums each entry's cross-resource paved area: the
// "combined" row when present, otherwise that entry's per-resource sum.
// Falling back per-entry (rather than flipping a global flag) keeps mixed
// rollout states correct: when only some entries have been re-run through
// `pvmt all compute`, the not-yet-computed entries still contribute via
// their per-resource sum instead of dropping out of the regional total.
func aggregateTotalPaved(ctx context.Context, entries []CityEntry) float64 {
	var sum float64
	for _, entry := range entries {
		sum += entryAreaWithFallback(ctx, entry.Store, resource.CombinedAll, resource.ScopeAll)
	}
	return sum
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
func summedBoundaryArea(ctx context.Context, entries []CityEntry) float64 {
	var total float64
	for _, entry := range entries {
		gjson, err := entry.Store.GetBoundary(ctx)
		if err != nil || gjson == "" {
			continue
		}
		a, err := geo.BoundaryAreaSqM(gjson)
		if err != nil {
			continue
		}
		total += a
	}
	return total
}

// regionBBox returns the union of sub-city bboxes ([south, west, north, east]).
func regionBBox(ctx context.Context, entries []CityEntry) ([4]float64, bool) {
	var out [4]float64
	first := true
	for _, entry := range entries {
		bb, _, _, err := entry.BBoxAndCenter(ctx)
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
	return out, !first
}

// BuildMultiCityForecastSeed aggregates per-city forecast seeds into one
// regional ForecastSeedJSON. TotalAreaSqM and CityPavedSqM sum the "combined"
// and "combined:city" rows across entries (with per-resource fallback).
// Cohort areas are summed across cities for each (resource_type, classification)
// pair — cross-resource collisions stay as separate cohorts to match the
// single-city collectCohortSeeds shape.
func BuildMultiCityForecastSeed(ctx context.Context, fc *config.ForecastConfig, entries []CityEntry) (template.JS, error) {
	costTiers := ConvertCostTiers(fc)
	if len(costTiers) == 0 {
		costTiers = forecast.DefaultCostTiers
	}

	var totalArea, cityArea float64
	for _, entry := range entries {
		totalArea += entryAreaWithFallback(ctx, entry.Store, resource.CombinedAll, resource.ScopeAll)
		cityArea += entryAreaWithFallback(ctx, entry.Store, resource.CombinedCity, resource.ScopeCity)
	}

	decayRate := fc.DecayRate
	if decayRate <= 0 {
		decayRate = forecast.DefaultDecayRates["default"]
	}
	seed := ForecastSeedJSON{
		InitialPCI:   fc.InitialPCI,
		DecayRate:    decayRate,
		GrowthRate:   fc.GrowthRate,
		Years:        fc.Years,
		TotalAreaSqM: totalArea,
		CityPavedSqM: cityArea,
		CostTiers:    costTiers,
		Cohorts:      mergeCohortSeeds(ctx, entries, fc, false),
		CityCohorts:  mergeCohortSeeds(ctx, entries, fc, true),
	}
	data, err := json.Marshal(seed)
	if err != nil {
		return "", fmt.Errorf("marshal multi-city forecast seed: %w", err)
	}
	return template.JS(data), nil
}

// entryAreaWithFallback reads the combined row for a single entry; if absent,
// sums the per-resource rows in the matching scope.
func entryAreaWithFallback(ctx context.Context, store db.Store, combinedLabel resource.Type, scope resource.Scope) float64 {
	if r, err := store.LatestComputeResult(ctx, combinedLabel); err == nil && r != nil {
		return r.TotalAreaSqM
	}
	var sum float64
	for _, rt := range resource.All {
		r, err := store.LatestComputeResult(ctx, rt.Type().With(scope))
		if err == nil && r != nil {
			sum += r.TotalAreaSqM
		}
	}
	return sum
}

// mergeCohortSeeds collects cohort stats across entries and sums areas for
// the same (resource_type, classification) pair. cityScope=true reads ":city"
// cohort rows. Cross-resource collisions (e.g. roads "default" vs parking
// "default") stay as separate cohorts — matching collectCohortSeeds' single-
// city shape, where each ListCohortStats(rt) row appears verbatim and a
// classification can recur across resources.
func mergeCohortSeeds(ctx context.Context, entries []CityEntry, fc *config.ForecastConfig, cityScope bool) []CohortSeed {
	type key struct {
		Resource       string
		Classification string
	}
	type bucket struct {
		Order int
		Seed  CohortSeed
	}
	buckets := make(map[key]*bucket)
	nextOrder := 0
	scope := resource.ScopeAll
	if cityScope {
		scope = resource.ScopeCity
	}
	for _, entry := range entries {
		for _, rt := range resource.All {
			t := rt.Type()
			stats, err := entry.Store.ListCohortStats(ctx, t.With(scope))
			if err != nil {
				continue
			}
			for _, st := range stats {
				k := key{Resource: string(t), Classification: st.Classification}
				b, ok := buckets[k]
				if !ok {
					b = &bucket{
						Order: nextOrder,
						Seed: CohortSeed{
							Classification: st.Classification,
							DecayRate:      resolvedDecayRate(st.Classification, fc.DecayRate),
						},
					}
					buckets[k] = b
					nextOrder++
				}
				b.Seed.AreaSqM += st.AreaSqM
			}
		}
	}
	if len(buckets) == 0 {
		return nil
	}
	out := make([]CohortSeed, len(buckets))
	for _, b := range buckets {
		out[b.Order] = b.Seed
	}
	return out
}
