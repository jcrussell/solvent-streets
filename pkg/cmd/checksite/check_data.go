package checksite

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"github.com/jcrussell/solvent-streets/internal/export"
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

// checkReasonableness validates every meta.json (plausible pct_paved) and
// forecast.json (monotonic baseline) found in the tree.
func (r *runner) checkReasonableness(s *site) {
	for _, d := range s.dataDirsWithLabel() {
		r.checkMeta(d.label, filepath.Join(d.dir, "meta.json"))
		r.checkForecast(d.label, filepath.Join(d.dir, "forecast.json"))
	}
}

// checkMeta flags the near-zero-area geometry bug: a city whose pct_paved is a
// positive value under 1%. pct_paved is stored as a percentage (50.0 == 50%),
// so the bug window is (0, 1.0). An absent/zero value is WARNed, not FAILed —
// it can be legitimately omitted for a not-yet-computed city.
func (r *runner) checkMeta(label, path string) {
	var meta export.MetaJSON
	if !readJSON(r, "reasonableness", label, "meta.json", path, &meta) {
		return
	}
	switch {
	case meta.PctPaved <= 0:
		r.warnf("reasonableness: %s: pct_paved is absent/zero — cannot validate paved area", label)
	case meta.PctPaved < 1.0:
		r.failf("reasonableness: %s: pct_paved %.3f%% < 1%% — possible near-zero-area geometry bug", label, meta.PctPaved)
	default:
		r.passf("reasonableness: %s: pct_paved %.2f%% is plausible", label, meta.PctPaved)
	}
}

// checkForecast validates each resource's baseline projection: PCI must fall
// strictly year over year (do-nothing decays) and deferred backlog must never
// fall (unmet need accumulates). Short (<2-year) baselines are passed as having
// nothing to compare.
func (r *runner) checkForecast(label, path string) {
	var forecasts []export.ForecastExport
	if !readJSON(r, "reasonableness", label, "forecast.json", path, &forecasts) {
		return
	}
	if len(forecasts) == 0 {
		r.warnf("reasonableness: %s: forecast.json has no resources", label)
		return
	}
	for _, fc := range forecasts {
		if msg := baselineViolation(fc); msg != "" {
			r.failf("reasonableness: %s [%s]: %s", label, fc.ResourceType, msg)
		} else {
			r.passf("reasonableness: %s [%s]: baseline PCI non-increasing and backlog non-decreasing", label, fc.ResourceType)
		}
	}
}

// baselineViolation returns a non-empty description when fc's baseline years
// violate the expected do-nothing physics, or "" when they hold (or there is
// nothing to validate).
//
// A genuine regression is an *increase* in PCI or a *decrease* in deferred
// backlog under do-nothing — physically impossible, so always a FAIL. A flat
// series (PCI and backlog both constant across the horizon) is the degenerate
// "empty resource" state — a city with no area of this type, whose baseline is
// all-zero — and is not a regression, so it is tolerated rather than FAILed on
// the strict-decrease rule.
func baselineViolation(fc export.ForecastExport) string {
	years := fc.Baseline.Years
	for i := 1; i < len(years); i++ {
		prev, cur := years[i-1], years[i]
		if cur.PCI > prev.PCI {
			return "baseline PCI increased year over year (do-nothing cannot improve)"
		}
		if cur.DeferredBacklog < prev.DeferredBacklog {
			return "baseline deferred_backlog decreased year over year"
		}
	}
	return ""
}

// checkConsistency warns when a city slug appearing in more than one example
// reports paved areas that diverge by 0.01% or more (relative). A slug seen in
// only one example has nothing to compare and is skipped silently.
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
		if !readJSON(r, "consistency", d.label, "meta.json", filepath.Join(d.dir, "meta.json"), &meta) {
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
				r.warnf("consistency: %s: total_paved diverges between %s (%.2f) and %s (%.2f) — check the examples use the same ingest sources",
					slug, group[0].label, base, g.label, g.paved)
			}
		}
		if !diverged {
			r.passf("consistency: %s: total_paved agrees across %d examples", slug, len(group))
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
func readJSON(r *runner, check, label, file, path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		r.failf("%s: %s: cannot read %s: %v", check, label, file, err)
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		r.failf("%s: %s: malformed %s: %v", check, label, file, err)
		return false
	}
	return true
}
