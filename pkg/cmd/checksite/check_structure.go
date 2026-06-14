package checksite

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/export"
)

// rootAssets are the files every site root must carry alongside the examples.
var rootAssets = []string{"index.html", ".nojekyll", "forecast.wasm", "wasm_exec.js"}

// checkStructure verifies the site root carries its shared assets and every
// example carries the full set of per-city data files.
func (r *runner) checkStructure(s *site) {
	for _, name := range rootAssets {
		if fileExists(filepath.Join(s.dir, name)) {
			r.passf("structure: site root has %s", name)
		} else {
			r.failf("structure: site root is missing %s", name)
		}
	}

	if len(s.examples) == 0 {
		r.failf("structure: no examples found under %s (expected at least one)", s.dir)
		return
	}

	for _, ex := range s.examples {
		r.checkExampleStructure(ex)
	}
}

func (r *runner) checkExampleStructure(ex example) {
	if ex.dataDir != "" {
		r.checkDataDir(ex.slug, ex.dataDir)
		return
	}
	// Multi-city.
	if fileExists(ex.cityJSON) {
		r.passf("structure: %s has cities.json", ex.slug)
	} else {
		r.failf("structure: %s is missing cities.json", ex.slug)
	}
	if len(ex.cities) == 0 {
		r.failf("structure: %s has a cities/ dir with no city subdirectories", ex.slug)
	}
	for _, c := range ex.cities {
		r.checkDataDir(ex.slug+"/"+c.slug, c.dataDir)
	}
}

// checkDataDir confirms a data directory holds exactly the expected files:
// every name in export.DataFileNames (a missing one FAILs) and reports any
// extras as a WARN.
func (r *runner) checkDataDir(label, dataDir string) {
	present := make(map[string]bool)
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		r.failf("structure: %s: cannot read data dir %q: %v", label, dataDir, err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			present[e.Name()] = true
		}
	}

	expected := make(map[string]bool, len(export.DataFileNames))
	var missing []string
	for _, name := range export.DataFileNames {
		expected[name] = true
		if !present[name] {
			missing = append(missing, name)
		}
	}
	var extra []string
	for name := range present {
		if !expected[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	if len(missing) == 0 {
		r.passf("structure: %s has all %d data files", label, len(export.DataFileNames))
	} else {
		r.failf("structure: %s is missing data file(s): %s", label, strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		r.warnf("structure: %s has unexpected data file(s): %s", label, strings.Join(extra, ", "))
	}
}

// refAttrRe captures the value of an href= or src= attribute, single or double
// quoted. checkReferences filters the captures down to local relative paths.
var refAttrRe = regexp.MustCompile(`(?:href|src)\s*=\s*["']([^"']+)["']`)

// checkReferences scans each example index.html for local asset references and
// fails when a referenced path does not resolve on disk relative to that html.
// Absolute-URL, protocol-relative, data:, and fragment references are ignored.
func (r *runner) checkReferences(s *site) {
	for _, ex := range s.examples {
		r.checkExampleReferences(ex)
	}
}

func (r *runner) checkExampleReferences(ex example) {
	indexPath := filepath.Join(ex.dir, "index.html")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		r.failf("references: %s: cannot read index.html: %v", ex.slug, err)
		return
	}

	var dangling []string
	checked := 0
	for _, m := range refAttrRe.FindAllStringSubmatch(string(data), -1) {
		ref := strings.TrimSpace(m[1])
		if !isLocalAssetRef(ref) {
			continue
		}
		checked++
		// Resolve relative to the html file's directory. A reference can come
		// from a JS string literal that injects a <script> (script.src =
		// '..\/wasm_exec.js'), where the slash is JS-escaped — unescape it so
		// the path resolves. Then strip any query or fragment suffix.
		clean := strings.ReplaceAll(ref, `\/`, "/")
		if i := strings.IndexAny(clean, "?#"); i >= 0 {
			clean = clean[:i]
		}
		target := filepath.Join(ex.dir, filepath.FromSlash(clean))
		if !fileExists(target) {
			dangling = append(dangling, ref)
		}
	}

	if len(dangling) > 0 {
		r.failf("references: %s/index.html references missing file(s): %s", ex.slug, strings.Join(dangling, ", "))
		return
	}
	r.passf("references: %s/index.html — %d local asset reference(s) resolve", ex.slug, checked)
}

// isLocalAssetRef reports whether ref is a local relative path worth resolving:
// not an absolute URL (http://, https://, //host), not a data: URI, not a bare
// fragment, and pointing at a static asset we expect on disk (.wasm/.js/.css).
func isLocalAssetRef(ref string) bool {
	if ref == "" || strings.HasPrefix(ref, "#") || strings.HasPrefix(ref, "data:") {
		return false
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "//") {
		return false
	}
	if strings.HasPrefix(ref, "mailto:") || strings.HasPrefix(ref, "javascript:") {
		return false
	}
	lower := strings.ToLower(ref)
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	return strings.HasSuffix(lower, ".wasm") ||
		strings.HasSuffix(lower, ".js") ||
		strings.HasSuffix(lower, ".css")
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
