package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// loadResolved reads and parses the config at abs, recursively resolving its
// [[include]] blocks and merging the included cities in. It returns the merged
// config and the raw bytes of abs plus every file it (transitively) includes,
// in declaration order, so the caller can fold them all into the content hash.
//
// ancestors holds the files currently being resolved on this branch; a repeat
// is an include cycle (a diamond — the same file reached via two different
// parents — is allowed and simply re-read, since the union-by-slug merge
// dedupes its cities). depth is the current nesting level, capped at
// maxIncludeDepth to stop a symlink pseudo-cycle before it exhausts the stack.
func loadResolved(abs string, ancestors map[string]bool, depth int) (*Config, [][]byte, error) {
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
	blobs := [][]byte{data}
	if len(cfg.Include) == 0 {
		return cfg, blobs, nil
	}

	dir := filepath.Dir(abs)
	for i, inc := range cfg.Include {
		if inc.Path == "" {
			return nil, nil, errors.Join(ErrInvalidConfig,
				fmt.Errorf("include[%d].path is required", i))
		}
		incAbs := inc.Path
		if !filepath.IsAbs(incAbs) {
			incAbs = filepath.Join(dir, incAbs)
		}
		incAbs, err = filepath.Abs(incAbs)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve include %q: %w", inc.Path, err)
		}
		child, childBlobs, err := loadResolved(incAbs, ancestors, depth+1)
		if err != nil {
			return nil, nil, fmt.Errorf("include %q: %w", inc.Path, err)
		}
		if err := mergeIncludedCities(cfg, child, inc.Tags); err != nil {
			return nil, nil, fmt.Errorf("include %q: %w", inc.Path, err)
		}
		blobs = append(blobs, childBlobs...)
	}
	return cfg, blobs, nil
}

// mergeIncludedCities folds child's cities into parent, tagging each with
// includeTags (unioned with the city's own tags) and flattening the child's
// per-metro calibration into fully self-describing per-city overrides so it
// survives the collapse into one config with an empty top-level [forecast]/
// [grid].
//
// Union is by slug: a slug new to parent is appended; a slug already present
// has its tags unioned in. A slug collision between two *different* city names
// (same slug, different Name) is an error — that would otherwise silently drop
// one real city. When the names match (e.g. a metro city that is also a top-50
// city, pulled in by two includes) the first include's calibration is kept and
// only tags accumulate.
func mergeIncludedCities(parent, child *Config, includeTags []string) error {
	for i := range child.Cities {
		src := child.Cities[i]
		slug := src.Slug()

		if existing := findCityBySlug(parent, slug); existing != nil {
			if existing.Name != src.Name {
				return errors.Join(ErrInvalidConfig, fmt.Errorf(
					"slug collision %q maps to two different city names %q and %q; "+
						"rename one so their slugs differ", slug, existing.Name, src.Name))
			}
			existing.Tags = unionTags(existing.Tags, includeTags, src.Tags)
			continue
		}

		merged := src
		merged.Tags = unionTags(includeTags, src.Tags)
		merged.HexEdgeM = child.effectiveHexEdge(&src)
		merged.Forecast = child.effectiveForecast(&src)
		parent.Cities = append(parent.Cities, merged)
	}
	return nil
}

// findCityBySlug returns a pointer to the parent city with the given slug, or
// nil. The pointer lets the caller mutate tags in place.
func findCityBySlug(c *Config, slug string) *CityConfig {
	for i := range c.Cities {
		if c.Cities[i].Slug() == slug {
			return &c.Cities[i]
		}
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
