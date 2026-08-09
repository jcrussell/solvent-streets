package checksite

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// site is the discovered shape of a built static-site tree. Discovery is
// structural only — it locates examples and their per-city data directories
// without reading file contents; the individual checks do the reading.
type site struct {
	dir      string
	examples []example
	// shadowed names child directories that classify as exports but were
	// skipped because the root itself classified first (see discoverSite's
	// early return). Populated only in that root-as-sole-example case; a
	// non-empty value means the audit is covering only the root and silently
	// ignoring real child exports. checkStructure surfaces it as a WARN.
	shadowed []string
}

// example is one exported tree. A current `pvmt export` produces exactly one,
// rooted at the site root itself (see discoverSite); the multi-example form
// survives for older trees that nested one export per subdirectory. Either way
// it is single-city (dataDir set, the data/ directory directly under the
// example) or multi-city (cities set, one entry per cities/<slug>/).
type example struct {
	slug     string // directory name under the site root
	dir      string // absolute path to the example directory
	dataDir  string // <dir>/data for a single-city example; "" for multi-city
	cities   []cityDir
	cityJSON string // <dir>/cities.json for a multi-city example; "" for single-city
}

// cityDir is one city's data directory within a multi-city example.
type cityDir struct {
	slug    string
	dataDir string // <example>/cities/<slug>/data
}

// discoverSite walks the immediate children of dir, classifying each as a
// single-city or multi-city example. It returns a clear error if dir is
// missing so the user is pointed at `make site`.
func discoverSite(dir string) (*site, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("site directory %q not found — run 'make site' first", dir)
	}

	// A single `pvmt export` writes one site rooted at dir itself: data/ for a
	// single-city export, or cities/ + cities.json for multi-city. Treat that
	// root as the sole example so the audit runs against the real cities. (Older
	// trees nested one example per subdirectory; the child walk below is the
	// fallback when the root itself is not an example.)
	if ex, ok := classifyExample(filepath.Base(dir), dir); ok {
		return &site{dir: dir, examples: []example{ex}, shadowed: shadowedChildren(dir)}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read site directory %q: %w", dir, err)
	}

	s := &site{dir: dir}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		exDir := filepath.Join(dir, e.Name())
		if ex, ok := classifyExample(e.Name(), exDir); ok {
			s.examples = append(s.examples, ex)
		}
	}
	sort.Slice(s.examples, func(i, j int) bool { return s.examples[i].slug < s.examples[j].slug })
	return s, nil
}

// classifyExample inspects one site-root subdirectory. It is an example when it
// has a data/ directory (single-city) or a cities/ directory (multi-city);
// anything else (an asset dir, say) is skipped.
func classifyExample(slug, exDir string) (example, bool) {
	dataDir := filepath.Join(exDir, "data")
	if isDir(dataDir) {
		return example{slug: slug, dir: exDir, dataDir: dataDir}, true
	}
	citiesDir := filepath.Join(exDir, "cities")
	if isDir(citiesDir) {
		ex := example{slug: slug, dir: exDir, cityJSON: filepath.Join(exDir, "cities.json")}
		cityEntries, err := os.ReadDir(citiesDir)
		if err == nil {
			for _, ce := range cityEntries {
				if ce.IsDir() {
					ex.cities = append(ex.cities, cityDir{
						slug:    ce.Name(),
						dataDir: filepath.Join(citiesDir, ce.Name(), "data"),
					})
				}
			}
		}
		return ex, true
	}
	return example{}, false
}

// shadowedChildren returns the names of dir's immediate child directories that
// would themselves classify as exports. It runs only when the root already
// classified as the sole example, so any hit is a directory the audit is about
// to skip — a tree with a genuine per-example layout under a root that also
// looks like an export. Returns nil (the normal single-root case) when no child
// classifies. A read error is treated as "no children" — checkStructure already
// FAILs a root with no readable examples.
func shadowedChildren(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := classifyExample(e.Name(), filepath.Join(dir, e.Name())); ok {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
