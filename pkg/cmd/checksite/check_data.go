package checksite

import (
	"encoding/json"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/metric"
)

// dataDirsWithLabel returns every per-city data directory in the site, each
// paired with a human label and the city slug it represents. The slug is the
// example name for a single-city example and the city subdir name for a
// multi-city example — the key the consistency check groups on.
func (s *site) dataDirsWithLabel() []struct{ label, slug, dir string } {
	var out []struct{ label, slug, dir string }
	for _, ex := range s.examples {
		if ex.dataDir != "" {
			out = append(out, struct{ label, slug, dir string }{ex.slug, ex.slug, ex.dataDir})
			continue
		}
		for _, c := range ex.cities {
			out = append(out, struct{ label, slug, dir string }{ex.slug + "/" + c.slug, c.slug, c.dataDir})
		}
	}
	return out
}

// checkReasonableness validates every meta.json (plausible paved_share) and
// forecast.json (monotonic baseline) found in the tree.
func (r *runner) checkReasonableness(s *site) {
	for _, d := range s.dataDirsWithLabel() {
		r.checkMeta(d.label, filepath.Join(d.dir, export.MetaFileName))
		r.checkForecast(d.label, d.slug, filepath.Join(d.dir, export.ForecastFileName))
	}
}

// minPlausiblePavedShare is the paved_share below which a positive value
// signals the near-zero-area geometry bug.
const minPlausiblePavedShare = 0.01

// minPlausiblePavedPerResident (m²) tells a rural city from that bug. A city
// can pave under 1% of its land and still be whole: Juneau's borough is mostly
// icefield (0.06% paved, 124 m² per resident), and a New England township is
// mostly woods. Lost geometry leaves the residents and drops the pavement, so
// a low share with at least this much per resident is a WARN, not a FAIL. The
// release site's median is about 70 m² per resident.
const minPlausiblePavedPerResident = 10.0

// checkMeta flags the near-zero-area geometry bug: a city whose paved_share
// (0..1) is positive but under minPlausiblePavedShare. An absent/zero value is
// WARNed, not FAILed — it can be legitimately omitted for a not-yet-computed
// city.
func (r *runner) checkMeta(label, path string) {
	var meta export.MetaJSON
	if !readJSON(r, chkReasonableness, label, export.MetaFileName, path, &meta) {
		return
	}
	switch {
	case meta.PavedShare <= 0:
		r.warnf(chkReasonableness, "%s: paved_share is absent/zero — cannot validate paved area", label)
	case meta.PavedShare < minPlausiblePavedShare && pavedPerResident(meta) >= minPlausiblePavedPerResident:
		r.warnf(chkReasonableness, "%s: paved %.3f%% < %.0f%%, but %.0f m² per resident is plausible — a rural or largely undeveloped city, not lost geometry",
			label, meta.PavedShare*100, minPlausiblePavedShare*100, pavedPerResident(meta))
	case meta.PavedShare < minPlausiblePavedShare:
		r.failf(chkReasonableness, "%s: paved %.3f%% < %.0f%% — possible near-zero-area geometry bug", label, meta.PavedShare*100, minPlausiblePavedShare*100)
	default:
		r.passf(chkReasonableness, "%s: paved %.2f%% is plausible", label, meta.PavedShare*100)
	}
	// A layer whose ingest failed or never ran ships as missing, not as a
	// zero; a WARN here is the list of cities to re-ingest.
	// A layer every source answered empty for is not one: upstream maps none
	// there, and a re-ingest cannot fill it.
	var reingest []string
	for _, rt := range meta.MissingResources {
		if sources, ok := meta.EmptySources[rt]; ok {
			r.passf(chkReasonableness, "%s: no %s mapped upstream (%s answered empty)", label, rt, strings.Join(sources, ", "))
			continue
		}
		reingest = append(reingest, rt)
	}
	if len(reingest) > 0 {
		r.warnf(chkReasonableness, "%s: no data for %s — re-ingest before publishing", label, strings.Join(reingest, ", "))
	}
	// A layer with data whose other configured source failed is partial: its
	// totals and rankings would undercount the city.
	for _, rt := range slices.Sorted(maps.Keys(meta.FailingSources)) {
		if slices.Contains(meta.MissingResources, rt) {
			continue
		}
		r.warnf(chkReasonableness, "%s: partial %s: no %s data (its last ingest failed) — re-ingest before publishing",
			label, rt, strings.Join(meta.FailingSources[rt], ", "))
	}
}

// pavedPerResident is the city's paved area per resident in m², or 0 when
// the site carries no population for it.
func pavedPerResident(meta export.MetaJSON) float64 {
	pop, ok := meta.Metrics[metric.Population]
	if !ok || pop.Total == nil || *pop.Total <= 0 {
		return 0
	}
	return meta.TotalPaved / *pop.Total
}

// checkForecast validates each resource's baseline projection: PCI must fall
// strictly year over year (do-nothing decays) and deferred backlog must never
// fall (unmet need accumulates). Short (<2-year) baselines are passed as having
// nothing to compare.
func (r *runner) checkForecast(label, slug, path string) {
	var forecasts []export.ForecastExport
	if !readJSON(r, chkReasonableness, label, export.ForecastFileName, path, &forecasts) {
		return
	}
	if len(forecasts) == 0 {
		r.warnf(chkReasonableness, "%s: forecast.json has no resources", label)
		return
	}
	for _, fc := range forecasts {
		// Per-resource zero-area blind spot: a single resource erroneously
		// zeroed (while other resources keep area) passes both monotonicity and
		// the whole-city paved_share check. WARN — not FAIL — because an all-flat/
		// all-zero baseline is also the legitimate "empty resource" state, and a
		// hard failure here would collide with those legit-empty resources.
		// The site carries no feature counts, so the warning names both
		// causes rather than guessing: ingest fetches by bounding box, and
		// features that all lie outside the city boundary are clipped to
		// zero area, which is not a geometry bug. `<resource> status` shows
		// the ingested count beside the area, read-only. With a city
		// baseline present, the primary scope is the city-maintained one, so
		// only that part is known to be zero.
		if baselineAreaAllZero(fc) {
			what := "resource has zero paved area"
			if fc.CityBaseline != nil {
				what = "resource has zero city-maintained paved area"
			}
			r.warnf(chkReasonableness, "%s [%s]: %s — every feature may lie outside the city boundary, or its geometry was dropped (compare the ingested count and area with 'pvmt --city %s %s status')",
				label, fc.ResourceType, what, slug, fc.ResourceType)
		}
		if msg := baselineViolation(fc); msg != "" {
			r.failf(chkReasonableness, "%s [%s]: %s", label, fc.ResourceType, msg)
		} else {
			r.passf(chkReasonableness, "%s [%s]: baseline PCI non-increasing and backlog non-decreasing", label, fc.ResourceType)
		}
	}
}

// baselineAreaAllZero reports whether fc's primary baseline carries no paved area:
// the year-0 area is zero (equivalently, every baseline year's area is zero,
// since do-nothing area only grows). A baseline with no years is treated as
// zero-area. Callers use this to surface a zero-area warning without
// false-failing a legitimately empty resource.
func baselineAreaAllZero(fc export.ForecastExport) bool {
	years := fc.Primary().Years
	if len(years) == 0 {
		return true
	}
	for _, y := range years {
		if y.Area != 0 {
			return false
		}
	}
	return true
}

// baselineViolation returns a non-empty description when one of fc's
// baselines (every jurisdiction's, and the city-maintained one when present)
// violates the expected do-nothing physics, or "" when they hold (or there
// is nothing to validate). The description names the baseline's key.
//
// A genuine regression is an *increase* in PCI or a *decrease* in deferred
// backlog under do-nothing — physically impossible, so always a FAIL. A flat
// series (PCI and backlog both constant across the horizon) is the degenerate
// "empty resource" state — a city with no area of this type, whose baseline is
// all-zero — and is not a regression, so it is tolerated rather than FAILed on
// the strict-decrease rule.
func baselineViolation(fc export.ForecastExport) string {
	if msg := yearsViolation("baseline", fc.Baseline.Years); msg != "" {
		return msg
	}
	if fc.CityBaseline != nil {
		return yearsViolation("city_baseline", fc.CityBaseline.Years)
	}
	return ""
}

func yearsViolation(name string, years []forecast.ScenarioYear) string {
	for i := 1; i < len(years); i++ {
		prev, cur := years[i-1], years[i]
		if cur.PCI > prev.PCI {
			return name + " PCI increased year over year (do-nothing cannot improve)"
		}
		if cur.DeferredBacklog < prev.DeferredBacklog {
			return name + " deferred_backlog decreased year over year"
		}
	}
	return ""
}

// checkConsistency warns when a city slug appearing in more than one example
// reports paved areas that diverge by 0.01% or more (relative). A slug seen in
// only one example has nothing to compare and is skipped silently — which is
// every slug on a single-root export, where the [[include]] merge already
// dedupes by slug. This only bites on a nested multi-export tree.
//
// This is a WARN, not a FAIL: check-site reads only the built tree and cannot
// see each example's pvmt.toml, so it can't tell an accidental divergence from
// an intentional one. Examples legitimately ingest the same city with different
// sources (e.g. bay-area-ca gives Oakland an arcgis_url + overpass while
// top-50-cities uses overpass only), which yields different — but correct —
// paved totals. Surface the divergence for a human to judge without blocking
// publish.
func (r *runner) checkConsistency(s *site) {
	type seen struct {
		label string
		paved float64
	}
	bySlug := make(map[string][]seen)
	for _, d := range s.dataDirsWithLabel() {
		var meta export.MetaJSON
		if !readJSON(r, chkConsistency, d.label, export.MetaFileName, filepath.Join(d.dir, export.MetaFileName), &meta) {
			continue
		}
		bySlug[d.slug] = append(bySlug[d.slug], seen{label: d.label, paved: meta.TotalPaved})
	}

	for slug, group := range bySlug {
		if len(group) < 2 {
			continue
		}
		base := group[0].paved
		diverged := false
		for _, g := range group[1:] {
			if relDiff(base, g.paved) >= 0.0001 {
				diverged = true
				r.warnf(chkConsistency, "%s: total_paved diverges between %s (%.2f) and %s (%.2f) — check the examples use the same ingest sources",
					slug, group[0].label, base, g.label, g.paved)
			}
		}
		if !diverged {
			r.passf(chkConsistency, "%s: total_paved agrees across %d examples", slug, len(group))
		}
	}
}

// relDiff is the relative difference between a and b. When both are zero it is
// zero; when only the reference is zero it is treated as fully divergent.
func relDiff(a, b float64) float64 {
	if a == 0 && b == 0 {
		return 0
	}
	denom := math.Max(math.Abs(a), math.Abs(b))
	return math.Abs(a-b) / denom
}

// readJSON reads and unmarshals path into v. A missing/unreadable file or
// malformed JSON FAILs under the given check name and returns false; the
// caller skips that file. (The structure check separately fails on a missing
// file, but malformed JSON only surfaces here.)
func readJSON(r *runner, check checkName, label, file, path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		r.failf(check, "%s: cannot read %s: %v", label, file, err)
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		r.failf(check, "%s: malformed %s: %v", label, file, err)
		return false
	}
	return true
}
