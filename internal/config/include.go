package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// IncludeSpec is one [[include]] block: a path to another pvmt.toml whose
// cities are merged into this config, plus tags applied to every city that
// file contributes. See Config.Include and loadResolved.
type IncludeSpec struct {
	// Path is the included pvmt.toml, resolved relative to the including
	// file's directory (absolute paths are used as-is).
	Path string `toml:"path"`
	// Tags label every city pulled in by this include. A city reached through
	// several includes accumulates the union of their tags.
	Tags []string `toml:"tags"`
}

// maxIncludeDepth caps [[include]] recursion. Cycle detection keys on the
// cleaned absolute path, which a directory symlink pointing at its own ancestor
// (sub -> ., yielding /x/sub/pvmt.toml, /x/sub/sub/pvmt.toml, …) defeats: every
// level is a distinct string, so without a depth cap loadResolved recurses to
// stack exhaustion (an unrecoverable Go fatal, not a clean ErrInvalidConfig).
// The same bypass exists on case-insensitive filesystems (/x/A.toml vs
// /x/a.toml), which EvalSymlinks would not catch — so a depth cap, not just
// symlink resolution, is the robust guard. 32 is far beyond any real include
// tree (examples/all nests one level).
const maxIncludeDepth = 32

// resolvedInclude is a memoized loadResolved result: the fully-resolved config
// for one file and the transitive blob list rooted at it.
type resolvedInclude struct {
	cfg   *Config
	blobs [][]byte
}

// loadParseHook, when non-nil, is invoked with the absolute path of every file
// loadResolved actually parses (a cache miss). Test-only seam for the
// parse-memoization guarantee (scpj); nil in production.
var loadParseHook func(abs string)

// calibrationWarning is one recorded disagreement: a city whose calibration
// two includes both set, differently. winner/loser are the *absolute* paths of
// the included files, not the paths as spelled at the include site — a nested
// tree can reach the same file through several spellings, and "../pvmt.toml"
// alone does not say which parent declared it. They are rendered relative to
// the top-level config's directory once, in Load, where that directory is
// known; for a flat tree like examples/all that reproduces the spelling the
// user wrote.
type calibrationWarning struct {
	city, slug    string
	winner, loser string
	fields        []string
}

// calibrationWarnings accumulates the non-fatal "an earlier include already
// set this field" notices raised during one Load. It is threaded through
// loadResolved (rather than stored per-Config) for the same reason cache is:
// a file reached through several includes resolves once, so its warnings are
// recorded once, and the whole transitive set lands on the top-level Config
// that Load returns.
//
// internal/config cannot print — it has no IOStreams — so the messages are
// carried out on Config.LoadWarnings() and emitted at the CLI boundary,
// mirroring how warnInvalidEnv surfaces silently-ignored PVMT_* env values.
type calibrationWarnings struct{ items []calibrationWarning }

func (w *calibrationWarnings) add(item calibrationWarning) {
	w.items = append(w.items, item)
}

// render formats each recorded disagreement into one user-facing line, with
// include paths expressed relative to baseDir (the directory holding the
// top-level config) so every path in the output is anchored to the file the
// user invoked pvmt against.
func (w *calibrationWarnings) render(baseDir string) []string {
	if len(w.items) == 0 {
		return nil
	}
	// One line per (city, losing include), and no longer than it has to be:
	// a union of ten example files produces a dozen of these, so a repeated
	// paragraph explaining the rule on every line would be the same noise the
	// warning exists to cut through. Everything needed to act is here — city,
	// slug, both includes, each contested field with both values.
	out := make([]string, 0, len(w.items))
	for _, it := range w.items {
		out = append(out, fmt.Sprintf(
			"city %q (slug %q): keeping include %q, ignoring include %q on %s "+
				"(kept vs ignored; the first include to set a field wins)",
			it.city, it.slug, relTo(baseDir, it.winner), relTo(baseDir, it.loser),
			strings.Join(it.fields, ", ")))
	}
	return out
}

// relTo renders path relative to baseDir, falling back to the absolute path
// when no relative form exists (different volumes on Windows).
func relTo(baseDir, path string) string {
	if r, err := filepath.Rel(baseDir, path); err == nil {
		return r
	}
	return path
}

// mergeState is the bookkeeping shared by every [[include]] of one config:
// byslug indexes parent.Cities so a slug lookup is O(1) instead of a rescan
// per child city (117k), fromInclude maps a slug contributed by an include to
// the absolute path of the include that contributed it *first* — the one whose
// already-set fields win — and warn is the Load-wide warning sink. byslug and
// fromInclude are per-file; warn is shared across the whole Load.
//
// A slug absent from fromInclude but present in byslug was declared directly
// by the parent, which is exempt from the union: "the parent's own city wins"
// is a documented local override, and it wins silently and wholesale.
type mergeState struct {
	byslug      map[string]int
	fromInclude map[string]string
	warn        *calibrationWarnings
}

// loadResolved reads and parses the config at abs, recursively resolving its
// [[include]] blocks and merging the included cities in. It returns the merged
// config and the raw bytes of abs plus every file it (transitively) includes,
// in declaration order, so the caller can fold them all into the content hash.
//
// ancestors holds the files currently being resolved on this branch; a repeat
// is an include cycle (a diamond — the same file reached via two different
// parents — is allowed: its resolution is memoized in cache and reused, and
// the union-by-slug merge dedupes its cities). depth is the current nesting
// level, capped at maxIncludeDepth to stop a symlink pseudo-cycle before it
// exhausts the stack.
//
// cache memoizes each file's resolution by absolute path within a single Load
// so a diamond/transitive include parses every file exactly once. Returning a
// cached entry cannot mask a cycle: a file only lands in cache after its
// resolution completes, and a file on the current ancestors stack has not
// completed — a true cycle is caught by the ancestors check before any cache
// entry for the cycling file exists. The merged config and content hash are
// unchanged: the cached child is treated read-only by mergeIncludedCities, and
// its blobs are re-appended at every include site so each edge still counts.
//
// warn is the Load-wide sink for first-include-wins notices; it is shared with
// every recursive call so the top-level caller sees the transitive set.
func loadResolved(abs string, ancestors map[string]bool, depth int, cache map[string]*resolvedInclude,
	warn *calibrationWarnings) (*Config, [][]byte, error) {
	if e, ok := cache[abs]; ok {
		return e.cfg, e.blobs, nil
	}
	if depth > maxIncludeDepth {
		return nil, nil, errors.Join(ErrInvalidConfig, fmt.Errorf(
			"include nesting too deep (>%d) at %s; likely a symlink cycle", maxIncludeDepth, abs))
	}
	if ancestors[abs] {
		return nil, nil, errors.Join(ErrInvalidConfig, fmt.Errorf("include cycle detected at %s", abs))
	}
	ancestors[abs] = true
	defer delete(ancestors, abs)

	// Reject a non-regular file before reading it: os.ReadFile on a FIFO
	// (path = "/dev/stdin") blocks forever rather than returning. Stat follows
	// symlinks, so a symlink to a regular file is still accepted.
	if info, err := os.Stat(abs); err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	} else if !info.Mode().IsRegular() {
		return nil, nil, errors.Join(ErrInvalidConfig,
			fmt.Errorf("include %s is not a regular file", abs))
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", abs, err)
	}
	if loadParseHook != nil {
		loadParseHook(abs)
	}
	blobs := [][]byte{data}
	if len(cfg.Include) == 0 {
		cache[abs] = &resolvedInclude{cfg: cfg, blobs: blobs}
		return cfg, blobs, nil
	}

	// Index the parent's own cities once so mergeIncludedCities can look up a
	// slug in O(1) instead of rescanning every parent city per child (117k).
	// byslug is maintained across include sites (appends update it); fromInclude
	// records which include first contributed each slug (vs declared directly)
	// so first-include-wins applies only to include-vs-include collisions.
	ms := &mergeState{
		byslug:      make(map[string]int, len(cfg.Cities)),
		fromInclude: make(map[string]string),
		warn:        warn,
	}
	for i := range cfg.Cities {
		ms.byslug[cfg.Cities[i].Slug()] = i
	}

	dir := filepath.Dir(abs)
	for i, inc := range cfg.Include {
		childBlobs, err := resolveOneInclude(cfg, inc, i, dir, ancestors, depth, cache, ms)
		if err != nil {
			return nil, nil, err
		}
		blobs = append(blobs, childBlobs...)
	}
	cache[abs] = &resolvedInclude{cfg: cfg, blobs: blobs}
	return cfg, blobs, nil
}

// resolveOneInclude resolves a single [[include]] entry of parent: it validates
// and absolutizes the path, recursively loads the included config, and merges
// its cities into parent (updating ms). It returns the included file's
// transitive blob list so the caller can fold it into the content hash.
// Split out of loadResolved to keep that function's cognitive complexity in
// bounds.
func resolveOneInclude(parent *Config, inc IncludeSpec, i int, dir string,
	ancestors map[string]bool, depth int, cache map[string]*resolvedInclude,
	ms *mergeState) ([][]byte, error) {
	if inc.Path == "" {
		return nil, errors.Join(ErrInvalidConfig,
			fmt.Errorf("include[%d].path is required", i))
	}
	incAbs := inc.Path
	if !filepath.IsAbs(incAbs) {
		incAbs = filepath.Join(dir, incAbs)
	}
	incAbs, err := filepath.Abs(incAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve include %q: %w", inc.Path, err)
	}
	child, childBlobs, err := loadResolved(incAbs, ancestors, depth+1, cache, ms.warn)
	if err != nil {
		return nil, fmt.Errorf("include %q: %w", inc.Path, err)
	}
	if err := mergeIncludedCities(parent, child, inc.Tags, incAbs, childBlobs, ms); err != nil {
		return nil, fmt.Errorf("include %q: %w", inc.Path, err)
	}
	return childBlobs, nil
}

// sourceIdentity is the (config id, content hash) pair a config's own cities
// are stored under, derived for an INCLUDED file. Both have to reproduce what
// Load would compute for that file if you ran pvmt from its directory —
// otherwise the union reads a different namespace, or worse, a real-but-stale
// snapshot.
//
// Neither can be read off the child *Config:
//
//   - ConfigID's absolute-path fallback lives in Load, which an included file
//     never goes through, so child.ConfigID is "" for any file that omits
//     config_id. Left unhandled, CityConfigID falls back to the parent's id and
//     the whole feature silently no-ops for exactly those users.
//
//   - child.Hash() is worse than useless. Load sets contentHash = hashBlobs
//     (length-prefixed, covering transitive includes); an included file only
//     passes through parseConfig, which sets contentHash = hashBytes over its
//     own bytes. Those differ by construction (TestInclude_NoIncludeHashUnchanged
//     pins it), and both generations exist in real databases — so the bare-bytes
//     hash does not fail to match, it matches an OLDER snapshot. That fails by
//     producing a plausible site from stale data, which is the worst way to
//     fail. Recompute from childBlobs, which is what Load hashes.
func sourceIdentity(child *Config, incAbs string, childBlobs [][]byte) (configID, contentHash string) {
	configID = child.ConfigID
	if configID == "" {
		configID = hashBytes([]byte(incAbs))
	}
	return configID, hashBlobs(childBlobs)
}

// mergeIncludedCities folds child's cities into parent, tagging each with
// includeTags (unioned with the city's own tags) and flattening the child's
// per-metro calibration into fully self-describing per-city overrides so it
// survives the collapse into one config with an empty top-level [forecast]/
// [grid].
//
// Union is by slug: a slug new to parent is appended (and recorded in ms); a
// slug already present has its tags unioned in and its calibration folded per
// field. A slug collision between two *different* city names (same slug,
// different Name) is an error — that would otherwise silently drop one real
// city.
//
// Calibration is a PER-FIELD union, not a block-atomic first-wins: for each of
// hex_edge_m, min_hex_area and every field of the forecast block, the first
// include to SET the field wins it, and a later include fills only the fields
// the winner left UNSET. So a city in a metro example and in a broad national
// list keeps the metro's local decay/grid tuning *and* gains the national
// list's cited initial_pci and current_budget instead of losing them. No city
// ends up calibrated worse than either input file.
//
// Two includes that both set a field to different values is the one case where
// something is genuinely dropped: the first still wins, and unionCalibration
// reports the field so mergeIncludedCities can record a warning naming the
// city, the field, both values, and both includes. A backfill is not a
// disagreement and is silent — it is the deferral case, in the direction the
// merge used to handle only for a *later* unset field.
//
// A city the parent declared directly (not via include) is exempt entirely:
// "the parent's own city wins" is a documented local override, and it wins
// wholesale and silently rather than being unioned with an include's values.
//
// incAbs is the absolute path of the included file; see calibrationWarning for
// why the merge records that rather than the path as spelled at the include
// site.
func mergeIncludedCities(parent, child *Config, includeTags []string, incAbs string,
	childBlobs [][]byte, ms *mergeState) error {
	srcConfigID, srcHash := sourceIdentity(child, incAbs, childBlobs)
	for i := range child.Cities {
		src := child.Cities[i]
		slug := src.Slug()

		if idx, ok := ms.byslug[slug]; ok {
			existing := &parent.Cities[idx]
			if existing.Name != src.Name {
				return errors.Join(ErrInvalidConfig, fmt.Errorf(
					"slug collision %q maps to two different city names %q and %q; "+
						"rename one so their slugs differ", slug, existing.Name, src.Name))
			}
			if winner, viaInclude := ms.fromInclude[slug]; viaInclude {
				if fields := unionCalibration(existing, child, &src); len(fields) > 0 {
					ms.warn.add(calibrationWarning{
						city: existing.Name, slug: slug,
						winner: winner, loser: incAbs, fields: fields,
					})
				}
			}
			existing.Tags = unionTags(existing.Tags, includeTags, src.Tags)
			continue
		}

		merged := src
		merged.Tags = unionTags(includeTags, src.Tags)
		merged.HexEdgeM = child.effectiveHexEdge(&src)
		merged.MinHexArea = child.effectiveMinHexArea(&src)
		merged.Forecast = child.effectiveForecast(&src)
		// Stamp source identity, but only where it is not already set. `merged`
		// is a wholesale copy of src, so a city that reached child through
		// child's OWN includes arrives already stamped with the file that
		// declared it — overwriting here would relabel a grandchild's city as
		// belonging to the middle file, and it would do so silently.
		if merged.SourceConfigID == "" {
			merged.SourceConfigID = srcConfigID
		}
		if merged.SourceConfigHash == "" {
			merged.SourceConfigHash = srcHash
		}
		if merged.SourceHexEdgeM == 0 {
			// The fully RESOLVED edge (including the package default), not the
			// flattened file-layer value above: it is compared against the
			// union's own ResolvedHexEdge, and both sides have to have been
			// through the same defaulting for that comparison to mean anything.
			merged.SourceHexEdgeM = child.ResolvedHexEdge(&src)
		}
		parent.Cities = append(parent.Cities, merged)
		ms.byslug[slug] = len(parent.Cities) - 1
		ms.fromInclude[slug] = incAbs
	}
	return nil
}

// unionCalibration folds the calibration src carries (flattened through its own
// config, child) into the values already captured on existing, per field:
// a field existing has not set takes src's value, a field both set identically
// is a no-op, and a field both set differently keeps existing's value and is
// described in the returned slice as "field (kept vs ignored)". An empty return
// therefore means nothing was discarded — which is what makes a warning mean
// something when there is one.
//
// existing is parent-owned throughout: HexEdgeM/MinHexArea are plain fields and
// Forecast was freshly allocated by effectiveForecast when this city was first
// appended, so mutating it here cannot reach back into a memoized child config.
//
// min_hex_area is unioned for the same reason it is flattened: it is coupled to
// hex_edge_m, so backfilling the edge while dropping the threshold sized for it
// would silently pair a fine grid with a coarse sliver filter.
func unionCalibration(existing *CityConfig, child *Config, src *CityConfig) []string {
	var conflicts []string
	unionFloat(&existing.HexEdgeM, child.effectiveHexEdge(src), "hex_edge_m", &conflicts)
	unionFloat(&existing.MinHexArea, child.effectiveMinHexArea(src), "min_hex_area", &conflicts)

	srcFc := child.effectiveForecast(src)
	if srcFc == nil {
		return conflicts
	}
	if existing.Forecast == nil {
		// Nothing set, so nothing to disagree with: adopt src's block whole.
		// effectiveForecast allocates per call, so this pointer is not shared
		// with the child config.
		existing.Forecast = srcFc
		return conflicts
	}
	return append(conflicts, unionForecast(existing.Forecast, srcFc)...)
}

// unionForecast applies the per-field union to a forecast block, returning the
// fields on which the two genuinely disagree. Zero is the "unset" sentinel for
// every field, matching the resolution layers in provenance.go
// (applyCityForecastProv) — including GrowthRate, where 0 is indistinguishable
// from "no growth" and a negative rate is a legitimate shrinking network.
func unionForecast(dst, src *ForecastConfig) []string {
	var conflicts []string
	unionFloat(&dst.InitialPCI, src.InitialPCI, "forecast.initial_pci", &conflicts)
	unionFloat(&dst.DecayRate, src.DecayRate, "forecast.decay_rate", &conflicts)
	unionGrowthRate(dst, src, &conflicts)
	unionFloat(&dst.TreatmentCycleYears, src.TreatmentCycleYears, "forecast.treatment_cycle_years", &conflicts)
	unionFloat(&dst.CurrentBudget, src.CurrentBudget, "forecast.current_budget", &conflicts)
	unionFloat(&dst.CostOverhead, src.CostOverhead, "forecast.cost_overhead", &conflicts)

	switch {
	case src.Years == 0: // unset upstream; nothing to fold in
	case dst.Years == 0:
		dst.Years = src.Years
	case dst.Years != src.Years:
		conflicts = append(conflicts, fmt.Sprintf("forecast.years (%d vs %d)", dst.Years, src.Years))
	}

	switch {
	case len(src.CostTiers) == 0: // unset upstream; nothing to fold in
	case len(dst.CostTiers) == 0:
		dst.CostTiers = src.CostTiers
	case !slices.Equal(dst.CostTiers, src.CostTiers):
		// Deliberately not "(4 vs 4 tiers)": two different 4-tier sets would
		// print an identical-looking pair and read as a formatting bug. Say
		// the sets differ and give both sizes as context, not as the finding.
		conflicts = append(conflicts, fmt.Sprintf(
			"forecast.cost_tiers (different tier sets: kept %d tiers, ignored %d)",
			len(dst.CostTiers), len(src.CostTiers)))
	}
	return conflicts
}

// unionFloat applies the per-field rule to one float calibration field. Zero is
// the unset sentinel everywhere in this package, so: an unset incoming value
// changes nothing, an unset destination takes the incoming value (a backfill —
// nothing is lost, so nothing is reported), equal values are a no-op, and a
// genuine disagreement keeps the destination and appends a description.
func unionFloat(dst *float64, incoming float64, name string, conflicts *[]string) {
	switch {
	case incoming == 0: // unset upstream; nothing to fold in
	case *dst == 0:
		*dst = incoming
	case *dst != incoming:
		*conflicts = append(*conflicts,
			fmt.Sprintf("%s (%s vs %s)", name, plainFloat(*dst), plainFloat(incoming)))
	}
}

// unionGrowthRate is unionFloat for growth_rate, which cannot use zero as its
// unset sentinel: an explicit `growth_rate = 0` is a real statement ("this city
// does not grow") that has to beat a positive value from a later include, and
// plain unionFloat would treat it as unset in both directions — never folding it
// in, and letting a later nonzero backfill silently overwrite it.
//
// Presence, not value, decides: an incoming field that was never stated changes
// nothing; a destination that never stated it takes the incoming value and its
// presence with it; two that both stated it and disagree keep the destination
// and report, exactly like every other field.
func unionGrowthRate(dst, src *ForecastConfig, conflicts *[]string) {
	switch {
	case !src.growthRateSet && src.GrowthRate == 0: // unset upstream
	case !dst.growthRateSet && dst.GrowthRate == 0:
		dst.GrowthRate = src.GrowthRate
		dst.growthRateSet = src.growthRateSet
	case dst.GrowthRate != src.GrowthRate:
		*conflicts = append(*conflicts, fmt.Sprintf("forecast.growth_rate (%s vs %s)",
			plainFloat(dst.GrowthRate), plainFloat(src.GrowthRate)))
	}
}

// plainFloat renders a config float in positional notation with no trailing
// zeros. %g/%v would print a city budget of $83,100,000 as "8.31e+07", which
// is unreadable in a warning the user is meant to act on; the same %g also
// renders 0.03 as "0.03", so only the large end actually needs fixing.
func plainFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// unionTags concatenates tag lists preserving first-seen order and dropping
// duplicates and empties. Returns nil for an all-empty result so the field
// stays omitted from JSON/TOML.
func unionTags(lists ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range lists {
		for _, t := range list {
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// effectiveHexEdge returns the hex edge that resolveHexEdgeForCity would pick
// for city from file layers alone (per-city override, else this config's
// top-level grid) — no env, no package default. Storing it as the merged
// city's HexEdgeM keeps the value under a merged config whose top-level grid is
// empty. A zero result means "unset" and lets the package default apply at
// runtime, exactly as it would have here.
func (c *Config) effectiveHexEdge(city *CityConfig) float64 {
	if city != nil && city.HexEdgeM > 0 {
		return city.HexEdgeM
	}
	return c.Grid.HexEdgeM
}

// effectiveMinHexArea is effectiveHexEdge for the heatmap sliver threshold:
// the value ResolvedMinHexArea would pick for city from this config's file
// layers alone (per-city override, else this config's top-level [display]) —
// no package default. Storing it as the merged city's MinHexArea is what keeps
// an included example's tuned threshold alive under a merged config whose
// top-level [display] belongs to the parent. It has to be flattened for the
// same reason hex_edge_m is, and *because* hex_edge_m is: the two are coupled
// (a finer grid wants a smaller sliver threshold), so flattening only the edge
// would silently pair an example's fine grid with the parent's coarse
// threshold. A zero result means "unset" and defers to the parent's top-level
// value, then the package default, at runtime.
func (c *Config) effectiveMinHexArea(city *CityConfig) float64 {
	if city != nil && city.MinHexArea > 0 {
		return city.MinHexArea
	}
	return c.Display.MinHexArea
}

// effectiveForecast folds this config's top-level [forecast] with city's
// per-city override into one ForecastConfig using the same sentinel precedence
// as resolveForecast, but WITHOUT the env and default layers. The result is the
// city's file-level calibration; stored as its per-city override it reproduces
// the original resolution under a merged config with an empty top-level
// [forecast] (env and defaults are re-applied identically at runtime). Returns
// nil when neither layer set anything, so no override is stored.
func (c *Config) effectiveForecast(city *CityConfig) *ForecastConfig {
	fc := c.Forecast
	var prov forecastProvenance
	applyCityForecastProv(&fc, &prov, city)
	if forecastIsZero(fc) {
		return nil
	}
	return &fc
}

// forecastIsZero reports whether every ForecastConfig field is at its zero
// value. A struct literal comparison can't be used because CostTiers is a slice.
func forecastIsZero(fc ForecastConfig) bool {
	// An explicitly-stated growth_rate = 0 is NOT "nothing set" — it is a city
	// opting out of a positive top-level rate. Treating it as zero here would
	// erase the override at flatten time and undo the whole presence-bit fix,
	// so the bit is checked before the values.
	if fc.growthRateSet {
		return false
	}
	return fc.InitialPCI == 0 && fc.DecayRate == 0 && fc.GrowthRate == 0 &&
		fc.Years == 0 && len(fc.CostTiers) == 0 &&
		fc.TreatmentCycleYears == 0 && fc.CurrentBudget == 0 &&
		fc.CostOverhead == 0
}
