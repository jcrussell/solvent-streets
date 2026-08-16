package checksite

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// sizeBudgets caps how many bytes one city may contribute to a given per-city
// data file. The budget is enforced against the *worst* city, not the mean:
// a mean lets a single bloated city hide behind a hundred small ones, and it
// moves the wrong way — adding cities would un-trip an existing breach. The
// worst-city figure is scale-invariant in both directions, which is the
// property the check needs.
//
// Only the files that scale with the city count are budgeted. The shared root
// assets (*.wasm, *.js, index.html) are a fixed cost the export pays once, so
// they are reported as tree totals and carry no per-city budget.
//
// The four heavy files are seeded LOOSE, at roughly 2-3x what the current
// export writes. The four small ones (forecast_seed, meta, hex-cost-summary,
// and to a lesser extent scenarios) are nominal ceilings, not calibrated
// values — they sit orders of magnitude above today's bytes and exist to catch
// a runaway, not to track growth. Six of the eight files are still
// pretty-printed at this commit, so every number here is measured against a
// baseline that is about to move; re-seed the whole map against a real export
// once B7 lands.
var sizeBudgets = map[string]int64{
	"boundary.geojson":      2 << 20,   // 2 MiB
	"forecast.json":         512 << 10, // 512 KiB
	"forecast_seed.json":    64 << 10,  // 64 KiB  (nominal)
	"hex-cost-summary.json": 64 << 10,  // 64 KiB  (nominal)
	"hexgrid.geojson":       3 << 20,   // 3 MiB
	"meta.json":             64 << 10,  // 64 KiB  (nominal)
	"play-hexes.json":       1 << 20,   // 1 MiB
	"scenarios.json":        256 << 10, // 256 KiB (nominal)
}

// dataFileNameSet is export.DataFileNames as a set, so the shared-asset walk
// can skip in one lookup the files the per-city pass already accounted for.
var dataFileNameSet = func() map[string]bool {
	m := make(map[string]bool, len(export.DataFileNames))
	for _, name := range export.DataFileNames {
		m[name] = true
	}
	return m
}()

// citySizes is one data file's accounting across every discovered city: the
// tree total and mean for context, and the single worst city — the value the
// budget is enforced against.
type citySizes struct {
	total    int64
	max      int64
	maxLabel string
}

func (c *citySizes) add(label string, n int64) {
	c.total += n
	if n > c.max {
		c.max, c.maxLabel = n, label
	}
}

// assetSizes is one shared-asset bucket: a tree total and a file count. These
// buckets are deliberately not divided by the city count — dividing a fixed
// cost by however many cities happen to be in the tree produces a figure that
// says nothing about either.
type assetSizes struct {
	total int64
	files int
}

// checkSizes reports where the tree's bytes actually live: one line per bucket,
// descending by total. Per-city data files are measured per city and checked
// against sizeBudgets on their worst city; shared root assets are reported as
// tree totals.
func (r *runner) checkSizes(s *site) {
	data, cities := perCityDataSizes(s)
	if cities == 0 {
		r.warnf("sizes: no per-city data directories found — reporting shared-asset totals only, budgets not evaluated")
	}

	shared, err := sharedAssetSizes(s.dir)
	if err != nil {
		r.failf("sizes: size walk failed: %v", err)
		return
	}

	for _, bucket := range bucketsByTotalDesc(data, shared) {
		if c, ok := data[bucket]; ok {
			r.reportDataFile(bucket, c, cities)
			continue
		}
		a := shared[bucket]
		r.passf("sizes: %s is %s total across %d file(s)", bucket, cmdutil.HumanSize(a.total), a.files)
	}
}

// reportDataFile emits one per-city data file's line, WARNing when its worst
// city is over budget. The mean rides along as context: a max far above the
// mean is one outlier city, a max near the mean is the whole tree.
func (r *runner) reportDataFile(bucket string, c *citySizes, cities int) {
	mean := c.total / int64(cities)
	context := fmt.Sprintf("%s mean over %d cities, %s total",
		cmdutil.HumanSize(mean), cities, cmdutil.HumanSize(c.total))

	budget, budgeted := sizeBudgets[bucket]
	switch {
	case budgeted && c.max > budget:
		r.warnf("sizes: %s is %s in %s — over the %s per-city budget (%s)",
			bucket, cmdutil.HumanSize(c.max), c.maxLabel, cmdutil.HumanSize(budget), context)
	case budgeted:
		r.passf("sizes: %s is at most %s (%s), budget %s (%s)",
			bucket, cmdutil.HumanSize(c.max), c.maxLabel, cmdutil.HumanSize(budget), context)
	default:
		r.passf("sizes: %s is at most %s (%s) (%s)",
			bucket, cmdutil.HumanSize(c.max), c.maxLabel, context)
	}
}

// perCityDataSizes measures each export.DataFileNames entry inside each
// discovered city data directory, returning the per-bucket accounting and the
// number of cities it covers.
//
// It reads only the expected filenames under the directories
// dataDirsWithLabel reports, so numerator and divisor come from the same set of
// cities — a tree walk would fold in bytes from directories that contribute no
// city (a shadowed child export) and skew every figure. A data directory that
// does not exist on disk is not a city: classifyExample admits any subdirectory
// of cities/, so a stray one would otherwise inflate the divisor. A missing
// individual file simply contributes nothing; the structure check owns that
// failure.
func perCityDataSizes(s *site) (map[string]*citySizes, int) {
	out := make(map[string]*citySizes, len(export.DataFileNames))
	cities := 0
	for _, d := range s.dataDirsWithLabel() {
		if !isDir(d.dir) {
			continue
		}
		cities++
		for _, name := range export.DataFileNames {
			info, err := os.Stat(filepath.Join(d.dir, name))
			if err != nil || info.IsDir() {
				continue
			}
			c := out[name]
			if c == nil {
				c = &citySizes{}
				out[name] = c
			}
			c.add(d.label, info.Size())
		}
	}
	return out, cities
}

// sharedAssetSizes walks the tree and buckets everything the per-city pass did
// not account for by extension. Files named like a per-city data file are
// skipped so the two passes cannot double-count.
func sharedAssetSizes(dir string) (map[string]*assetSizes, error) {
	out := make(map[string]*assetSizes)
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || dataFileNameSet[d.Name()] {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		bucket := assetBucket(d.Name())
		a := out[bucket]
		if a == nil {
			a = &assetSizes{}
			out[bucket] = a
		}
		a.total += info.Size()
		a.files++
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// bucketsByTotalDesc orders every bucket from both passes largest-first so the
// report reads as a worklist, with the name as a tiebreaker to keep the output
// deterministic.
func bucketsByTotalDesc(data map[string]*citySizes, shared map[string]*assetSizes) []string {
	totals := make(map[string]int64, len(data)+len(shared))
	for name, c := range data {
		totals[name] = c.total
	}
	for name, a := range shared {
		totals[name] = a.total
	}
	names := make([]string, 0, len(totals))
	for name := range totals {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if totals[names[i]] != totals[names[j]] {
			return totals[names[i]] > totals[names[j]]
		}
		return names[i] < names[j]
	})
	return names
}

// assetBucket classifies one shared asset by extension, so the report separates
// the markup, the WASM pair and the browser JS from everything else.
func assetBucket(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html":
		return "*.html"
	case ".wasm":
		return "*.wasm"
	case ".js":
		return "*.js"
	default:
		return "other"
	}
}
