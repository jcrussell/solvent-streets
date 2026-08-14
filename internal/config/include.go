package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
func loadResolved(abs string, ancestors map[string]bool, depth int, cache map[string]*resolvedInclude) (*Config, [][]byte, error) {
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
	// tracks which slugs were contributed by an include (vs declared directly)
	// so the conflict check applies only to include-vs-include collisions.
	byslug := make(map[string]int, len(cfg.Cities))
	for i := range cfg.Cities {
		byslug[cfg.Cities[i].Slug()] = i
	}
	fromInclude := make(map[string]bool)

	dir := filepath.Dir(abs)
	for i, inc := range cfg.Include {
		childBlobs, err := resolveOneInclude(cfg, inc, i, dir, ancestors, depth, cache, byslug, fromInclude)
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
// its cities into parent (updating byslug/fromInclude). It returns the included
// file's transitive blob list so the caller can fold it into the content hash.
// Split out of loadResolved to keep that function's cognitive complexity in
// bounds.
func resolveOneInclude(parent *Config, inc IncludeSpec, i int, dir string,
	ancestors map[string]bool, depth int, cache map[string]*resolvedInclude,
	byslug map[string]int, fromInclude map[string]bool) ([][]byte, error) {
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
	child, childBlobs, err := loadResolved(incAbs, ancestors, depth+1, cache)
	if err != nil {
		return nil, fmt.Errorf("include %q: %w", inc.Path, err)
	}
	if err := mergeIncludedCities(parent, child, inc.Tags, byslug, fromInclude); err != nil {
		return nil, fmt.Errorf("include %q: %w", inc.Path, err)
	}
	return childBlobs, nil
}

// mergeIncludedCities folds child's cities into parent, tagging each with
// includeTags (unioned with the city's own tags) and flattening the child's
// per-metro calibration into fully self-describing per-city overrides so it
// survives the collapse into one config with an empty top-level [forecast]/
// [grid].
//
// Union is by slug: a slug new to parent is appended (and recorded in byslug /
// fromInclude); a slug already present has its tags unioned in. A slug collision
// between two *different* city names (same slug, different Name) is an error —
// that would otherwise silently drop one real city. When a slug already present
// via a prior include reappears with a *different* non-empty calibration
// (hex_edge_m/forecast), that too is an error (see checkCalibrationConflict):
// silently keeping the first and dropping the second hides a real disagreement.
// Identical or empty calibration on the later include is fine (first wins). A
// city the parent declared directly (not via include) is exempt from the
// conflict check: "parent's own city wins" is a documented local override.
func mergeIncludedCities(parent, child *Config, includeTags []string, byslug map[string]int, fromInclude map[string]bool) error {
	for i := range child.Cities {
		src := child.Cities[i]
		slug := src.Slug()

		if idx, ok := byslug[slug]; ok {
			existing := &parent.Cities[idx]
			if existing.Name != src.Name {
				return errors.Join(ErrInvalidConfig, fmt.Errorf(
					"slug collision %q maps to two different city names %q and %q; "+
						"rename one so their slugs differ", slug, existing.Name, src.Name))
			}
			if fromInclude[slug] {
				if err := checkCalibrationConflict(existing, child, &src, slug); err != nil {
					return err
				}
			}
			existing.Tags = unionTags(existing.Tags, includeTags, src.Tags)
			continue
		}

		merged := src
		merged.Tags = unionTags(includeTags, src.Tags)
		merged.HexEdgeM = child.effectiveHexEdge(&src)
		merged.Forecast = child.effectiveForecast(&src)
		parent.Cities = append(parent.Cities, merged)
		byslug[slug] = len(parent.Cities) - 1
		fromInclude[slug] = true
	}
	return nil
}

// checkCalibrationConflict errors when a city reached through a second include
// carries calibration that genuinely disagrees with what was captured on its
// first appearance. "Conflict" means both sides specify a value and they
// differ; an unset (empty) value on either side is not a conflict — it defers
// to the value already stored (the long-standing "first wins for empty or
// identical" behavior). Only a real disagreement — a different non-zero
// hex_edge_m, or a differing non-nil forecast block — is rejected, so the
// operator must reconcile the includes explicitly rather than have one silently
// dropped. existing holds the first include's flattened calibration; child/src
// describe the current include's source config and city.
func checkCalibrationConflict(existing *CityConfig, child *Config, src *CityConfig, slug string) error {
	srcEdge := child.effectiveHexEdge(src)
	if existing.HexEdgeM != 0 && srcEdge != 0 && existing.HexEdgeM != srcEdge {
		return errors.Join(ErrInvalidConfig, fmt.Errorf(
			"city %q (slug %q) is included more than once with conflicting hex_edge_m "+
				"(%g vs %g); reconcile the includes so they agree",
			existing.Name, slug, existing.HexEdgeM, srcEdge))
	}
	srcFc := child.effectiveForecast(src)
	if existing.Forecast != nil && srcFc != nil && !reflect.DeepEqual(*existing.Forecast, *srcFc) {
		return errors.Join(ErrInvalidConfig, fmt.Errorf(
			"city %q (slug %q) is included more than once with conflicting forecast "+
				"calibration; reconcile the includes so they agree", existing.Name, slug))
	}
	return nil
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
	return fc.InitialPCI == 0 && fc.DecayRate == 0 && fc.GrowthRate == 0 &&
		fc.Years == 0 && len(fc.CostTiers) == 0 &&
		fc.TreatmentCycleYears == 0 && fc.CurrentBudget == 0
}
