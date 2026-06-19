package checksite

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jcrussell/solvent-streets/internal/export"
)

// checkWasmFreshness compares the site-root WASM assets against the copies
// embedded in this binary. A mismatch means the site was built by a different
// (older) binary and must be regenerated.
func (r *runner) checkWasmFreshness(s *site) {
	for name, embedded := range export.EmbeddedWasmAssets() {
		path := filepath.Join(s.dir, name)
		onDisk, err := os.ReadFile(path)
		if err != nil {
			r.failf("wasm: cannot read %s: %v", name, err)
			continue
		}
		if sha256.Sum256(onDisk) == sha256.Sum256(embedded) {
			r.passf("wasm: %s matches this binary's embedded copy", name)
		} else {
			r.failf("wasm: %s is stale vs this binary's embedded copy — rebuild with make site", name)
		}
	}
}

// binaryExts are extensions skipped by the hygiene text scan: assets that are
// legitimately non-text and would otherwise produce noise or huge reads.
var binaryExts = map[string]bool{
	".wasm": true, ".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true, ".ico": true, ".woff": true,
	".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".gz": true, ".zip": true, ".pdf": true,
}

// hygienePatterns flag content that must never ship in a published tree: host
// filesystem paths, the author's email, and common secret tokens. config_id is
// deliberately NOT matched — it appears verbatim in the Input-Configuration
// HTML block and is not a secret.
//
// The /home/ and /Users/ path patterns require a path-boundary delimiter
// before the segment — start of text, whitespace, a quote, '=', etc. — and
// explicitly NOT a URL/domain character ([\w./-]). This distinguishes a real
// leaked absolute path ("/home/ubuntu/...") from a legitimate URL path segment
// such as a citation link "https://www.newtonma.gov/home/showpublisheddocument".
var hygienePatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"host path /home/", regexp.MustCompile(`(^|[^\w./-])/home/`)},
	{"host path /Users/", regexp.MustCompile(`(^|[^\w./-])/Users/`)},
	{"author email", regexp.MustCompile(`(?i)joncrussell|@gmail`)},
	{"secret token", regexp.MustCompile(`(?i)(api[_-]?key|aws_|password|secret)`)},
}

// maxHygieneRead caps how much of any single file the hygiene scan reads. Site
// .geojson files can be large; a leak would appear in head text (paths, emails,
// secrets do not live deep inside coordinate arrays), so a generous prefix is
// sufficient and bounds memory.
const maxHygieneRead = 4 << 20 // 4 MiB

// checkHygiene walks every file under the site and fails when a text file
// contains a host path, an author email, or a secret token.
func (r *runner) checkHygiene(s *site) {
	var hits []string
	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if binaryExts[ext] {
			return nil
		}
		if name := scanHygiene(s.dir, path); name != "" {
			hits = append(hits, name)
		}
		return nil
	})
	if err != nil {
		r.failf("hygiene: walk failed: %v", err)
		return
	}
	if len(hits) == 0 {
		r.passf("hygiene: no host paths, emails, or secrets found")
		return
	}
	for _, h := range hits {
		r.failf("hygiene: %s", h)
	}
}

// scanHygiene reads up to maxHygieneRead bytes of path and returns a
// "<rel>: matched <pattern>" description for the first pattern that hits, or ""
// when clean / unreadable.
func scanHygiene(root, path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, maxHygieneRead)
	n, _ := f.Read(buf)
	content := buf[:n]

	for _, p := range hygienePatterns {
		if p.re.Match(content) {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = path
			}
			return rel + ": matched " + p.name
		}
	}
	return ""
}
