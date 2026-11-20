package export

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/fsutil"
	"github.com/jcrussell/solvent-streets/internal/hint"
)

// ManifestFileName is the file at the top of a generated site that lists
// every file pvmt export wrote there, so that 'pvmt export --clean' removes
// exactly those and nothing else. Its format, one line each:
//
//	pvmt export manifest v1
//	state: complete
//	index.html
//	data/meta.json
//	...
//
// The header names the format version. The state is "in-progress" while an
// export runs, which appends each path before it writes the file, and
// "complete" once the export finished, when the whole file is replaced
// atomically. Every further line is a path relative to the site root, with
// forward slashes. The manifest never lists itself. Two exports into one
// directory at once are not supported: they would interleave their
// manifests.
const ManifestFileName = ".pvmt-export"

const (
	manifestHeaderPrefix = "pvmt export manifest v"
	manifestHeader       = manifestHeaderPrefix + "1"
	manifestInProgress   = "state: in-progress"
	manifestComplete     = "state: complete"
)

// errLegacyMarker is a .pvmt-export written by an export from before the
// manifest: it names no files, so nothing it covers can be removed.
var errLegacyMarker = errors.New("legacy export marker")

// errNewerManifest is a manifest in a format version this pvmt does not read.
var errNewerManifest = errors.New("manifest in a newer format")

// siteManifest is a parsed ManifestFileName.
type siteManifest struct {
	complete bool
	files    []string
}

// readManifest reads root's manifest. It returns fs.ErrNotExist when there is
// none, errLegacyMarker for a marker without a manifest header and
// errNewerManifest for a version other than v1.
func readManifest(root *os.Root) (*siteManifest, error) {
	data, err := root.ReadFile(ManifestFileName)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	switch {
	case !sc.Scan() || !strings.HasPrefix(sc.Text(), manifestHeaderPrefix):
		return nil, errLegacyMarker
	case sc.Text() != manifestHeader:
		return nil, fmt.Errorf("%w (%q)", errNewerManifest, sc.Text())
	}
	m := &siteManifest{}
	if !sc.Scan() {
		return nil, fmt.Errorf("%s has no state line", ManifestFileName)
	}
	switch sc.Text() {
	case manifestComplete:
		m.complete = true
	case manifestInProgress:
	default:
		return nil, fmt.Errorf("%s has an unknown state %q", ManifestFileName, sc.Text())
	}
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			m.files = append(m.files, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", ManifestFileName, err)
	}
	return m, nil
}

// checkManifestPath refuses a manifest entry that is not a plain relative
// path inside root: absolute, with a ".." or "." element, a backslash, or
// one that resolves outside root through a symlink. pvmt writes no such
// path, so one means the manifest was edited or planted.
func checkManifestPath(root, rel string) error {
	switch {
	case rel == "" || strings.ContainsAny(rel, "\\\x00") || path.IsAbs(rel) || filepath.IsAbs(rel):
		return fmt.Errorf("%s lists %q, which is not a relative path", ManifestFileName, rel)
	case path.Clean(rel) != rel || rel == "." || slices.Contains(strings.Split(rel, "/"), ".."):
		return fmt.Errorf("%s lists %q, which is not a clean path inside the site", ManifestFileName, rel)
	case rel == ManifestFileName:
		return fmt.Errorf("%s lists itself", ManifestFileName)
	}
	if _, err := fsutil.SafePath(root, filepath.FromSlash(rel)); err != nil {
		return fmt.Errorf("%s lists %q: %w", ManifestFileName, rel, err)
	}
	return nil
}

// PrepareOutput readies the output directory before Run writes into it. A
// missing directory is created and an empty one is used as is. A non-empty
// one is refused unless clean is set, and then only when it holds a manifest
// (ManifestFileName) from an earlier export: the files that manifest lists
// are removed, with the directories they imply once empty and the temp files
// an interrupted write of one left, and every other file is left where it
// is. A directory without a manifest, a manifest entry outside the directory,
// or a file this export would write that the manifest does not list refuses
// the export before anything is removed. Every removal goes through an
// os.Root, so a directory swapped for a symlink after the check cannot steer
// it outside. The os.Root it opens is the one Run then writes through.
func (e *Exporter) PrepareOutput(clean bool) (err error) {
	dir := e.outputDir
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("output path %q exists but is not a directory", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = root.Close()
			return
		}
		e.root = root
	}()
	if fsutil.IsEmptyDir(dir) {
		return nil
	}
	if !clean {
		return hint.Withf(fmt.Errorf("output directory %q already exists and is not empty", dir),
			"pass --clean to replace a site pvmt export wrote there, or name another --output")
	}
	m, err := readManifest(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return hint.Withf(fmt.Errorf("refusing to clean output directory %q: it is not empty and holds no %s manifest, so pvmt export did not write it", dir, ManifestFileName),
			"name another --output, or remove the directory yourself")
	case errors.Is(err, errLegacyMarker):
		return hint.Withf(fmt.Errorf("refusing to clean output directory %q: its %s is from an older pvmt export and lists no files", dir, ManifestFileName),
			"export into a new --output, or remove the old site yourself once")
	case errors.Is(err, errNewerManifest):
		return hint.Withf(fmt.Errorf("refusing to clean output directory %q: its %s was written by a newer pvmt: %w", dir, ManifestFileName, err),
			"clean it with that pvmt, or export into a new --output")
	case err != nil:
		return fmt.Errorf("refusing to clean output directory %q: %w", dir, err)
	}
	listed := make(map[string]bool, len(m.files))
	for _, rel := range m.files {
		if err := checkManifestPath(dir, rel); err != nil {
			return fmt.Errorf("refusing to clean output directory %q: %w", dir, err)
		}
		if fi, err := root.Lstat(filepath.FromSlash(rel)); err == nil && fi.IsDir() {
			return fmt.Errorf("refusing to clean output directory %q: %s lists %q, which is a directory", dir, ManifestFileName, rel)
		}
		listed[rel] = true
	}
	if err := e.checkForeign(root, listed); err != nil {
		return err
	}
	if afterCleanCheck != nil {
		afterCleanCheck()
	}
	return removeListed(root, m.files)
}

// afterCleanCheck, when set, runs between PrepareOutput's checks and its
// removals. Tests use it to swap a directory for a symlink in that gap.
var afterCleanCheck func()

// checkForeign refuses when a file this export writes already exists in the
// output directory and the previous manifest (listed) does not name it.
func (e *Exporter) checkForeign(root *os.Root, listed map[string]bool) error {
	for _, rel := range e.plannedPaths() {
		if listed[rel] {
			continue
		}
		if _, err := root.Lstat(filepath.FromSlash(rel)); err == nil {
			return hint.Withf(fmt.Errorf("refusing to export into %q: it would overwrite %s, which pvmt export did not write", e.outputDir, rel),
				"move %s aside, or name another --output", rel)
		}
	}
	return nil
}

// plannedPaths are the files Run may write, relative to the output
// directory with forward slashes.
func (e *Exporter) plannedPaths() []string {
	paths := SiteRootFiles()
	data := []string{DataDirName}
	if len(e.entries) > 1 {
		paths = append(paths, CitiesFileName, CompareFileName)
		data = data[:0]
		for _, entry := range e.entries {
			data = append(data, path.Join(CitiesDirName, entry.Slug, DataDirName))
		}
	}
	for _, dir := range data {
		for _, name := range AllDataFileNames() {
			paths = append(paths, path.Join(dir, name))
		}
	}
	return paths
}

// tempSuffix starts the name of the temp file writeAtomic writes beside its
// target: "<name>.tmp-<digits>", fsutil.WriteFile's shape (fsutil.TempPattern).
var tempSuffix = strings.TrimSuffix(fsutil.TempPattern(""), "*")

// tempOf reports the file a temp file name stands in for: "a.json" for
// "a.json.tmp-123".
func tempOf(name string) (string, bool) {
	i := strings.LastIndex(name, tempSuffix)
	if i <= 0 {
		return "", false
	}
	digits := name[i+len(tempSuffix):]
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return "", false
	}
	return name[:i], true
}

// removeListed removes, under root, the files a checked manifest lists (a
// symlink is removed, never followed) and the temp files an interrupted write
// of one left, then each directory those paths imply once it is empty,
// deepest first, then the manifest.
func removeListed(root *os.Root, files []string) error {
	for _, rel := range files {
		if err := root.Remove(filepath.FromSlash(rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("clean %s: %w", rel, err)
		}
	}
	dirs := impliedDirs(files)
	for d, names := range dirs {
		if err := removeTemps(root, d, names); err != nil {
			return err
		}
	}
	removeEmptyDirs(root, dirs)
	if err := root.Remove(ManifestFileName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("clean %s: %w", ManifestFileName, err)
	}
	return nil
}

// impliedDirs maps each directory the manifest paths files imply, the site
// root (".") among them, to the names files lists in it; the root also
// holds the manifest.
func impliedDirs(files []string) map[string]map[string]bool {
	dirs := map[string]map[string]bool{".": {ManifestFileName: true}}
	for _, rel := range files {
		d, base := path.Split(rel)
		d = path.Clean(d)
		if dirs[d] == nil {
			dirs[d] = map[string]bool{}
		}
		dirs[d][base] = true
		for p := path.Dir(d); p != "."; p = path.Dir(p) {
			if dirs[p] == nil {
				dirs[p] = map[string]bool{}
			}
		}
	}
	return dirs
}

// removeEmptyDirs removes each of dirs but the root that is an empty
// directory, deepest first. A directory that is not empty stays.
func removeEmptyDirs(root *os.Root, dirs map[string]map[string]bool) {
	ordered := make([]string, 0, len(dirs))
	for d := range dirs {
		if d != "." {
			ordered = append(ordered, d)
		}
	}
	slices.SortFunc(ordered, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	for _, d := range ordered {
		if fi, err := root.Lstat(filepath.FromSlash(d)); err == nil && fi.IsDir() {
			_ = root.Remove(filepath.FromSlash(d)) // fails, harmlessly, when not empty
		}
	}
}

// removeTemps removes the temp files in dir that stand in for one of names.
func removeTemps(root *os.Root, dir string, names map[string]bool) error {
	f, err := root.Open(filepath.FromSlash(dir))
	if err != nil {
		return nil //nolint:nilerr // a directory already gone holds no temp files
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return nil //nolint:nilerr // best-effort, as for a directory already gone
	}
	for _, ent := range entries {
		if base, ok := tempOf(ent.Name()); ok && names[base] && ent.Type().IsRegular() {
			rel := path.Join(dir, ent.Name())
			if err := root.Remove(filepath.FromSlash(rel)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("clean %s: %w", rel, err)
			}
		}
	}
	return nil
}

// writeAtomic writes data to rel under root: a temp file beside it, fsync,
// rename, then an fsync of the directory, as fsutil.WriteFile does, but every
// step through root, so no symlink can take the write outside it.
func writeAtomic(root *os.Root, rel string, data []byte) error {
	name := filepath.FromSlash(rel)
	dir := filepath.Dir(name)
	if dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var (
		tmp string
		f   *os.File
		err error
	)
	for range 100 {
		tmp = name + tempSuffix + strconv.FormatUint(rand.Uint64N(1e10), 10) //nolint:gosec // O_EXCL settles a clash; the name need not be unguessable
		f, err = root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", rel, err)
	}
	cleanup := func() { _ = root.Remove(tmp) }
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("sync %s: %w", rel, err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", rel, err)
	}
	if err := root.Rename(tmp, name); err != nil {
		cleanup()
		return fmt.Errorf("rename %s: %w", rel, err)
	}
	return syncRootDir(root, dir)
}

// syncRootDir fsyncs dir under root so a rename into it survives a power
// failure. A failure to sync is not reported: some filesystems refuse fsync
// on a directory (fsutil.WriteFile's rule).
func syncRootDir(root *os.Root, dir string) error {
	d, err := root.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s to sync it: %w", dir, err)
	}
	_ = d.Sync()
	return d.Close()
}

// siteWriter writes the files of one export and keeps its manifest: each
// path is appended to the in-progress manifest, and synced, before its file
// is written, and close replaces the manifest with the complete one. Every
// write goes through an os.Root on the output directory, so a symlink (one
// planted, or a directory swapped for one mid-export) cannot take it outside.
// It refuses to overwrite a file it did not write (one not in the manifest it
// started from). A nil siteWriter writes plainly, for tests of a single
// writer.
type siteWriter struct {
	dir     string
	root    *os.Root
	journal *os.File
	owned   map[string]bool
	files   []string
}

// openSite starts the manifest of dir, opened as root, which the writer
// then owns and closes. Files an earlier, uncleaned export listed stay
// listed, and may be overwritten.
func openSite(dir string, root *os.Root) (*siteWriter, error) {
	w := &siteWriter{dir: dir, root: root, owned: map[string]bool{}}
	if m, err := readManifest(root); err == nil {
		for _, rel := range m.files {
			if checkManifestPath(dir, rel) == nil && !w.owned[rel] {
				w.owned[rel] = true
				w.files = append(w.files, rel)
			}
		}
	}
	if err := writeAtomic(root, ManifestFileName, w.content(manifestInProgress)); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("write %s: %w", ManifestFileName, err)
	}
	f, err := root.OpenFile(ManifestFileName, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open %s: %w", ManifestFileName, err)
	}
	w.journal = f
	return w, nil
}

func (w *siteWriter) content(state string) []byte {
	var b strings.Builder
	b.WriteString(manifestHeader + "\n" + state + "\n")
	for _, rel := range w.files {
		b.WriteString(rel + "\n")
	}
	return []byte(b.String())
}

// claim records full as written by this export, refusing a path outside the
// output directory and a file already there that the manifest does not list,
// and returns its manifest path.
func (w *siteWriter) claim(full string) (string, error) {
	rel, err := filepath.Rel(w.dir, full)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if err := checkManifestPath(w.dir, rel); err != nil {
		return "", err
	}
	if w.owned[rel] {
		return rel, nil
	}
	if _, err := w.root.Lstat(filepath.FromSlash(rel)); err == nil {
		return "", hint.Withf(fmt.Errorf("refusing to overwrite %q, which pvmt export did not write", full),
			"move it aside, or name another --output")
	}
	if _, err := w.journal.WriteString(rel + "\n"); err != nil {
		return "", fmt.Errorf("record %s in %s: %w", rel, ManifestFileName, err)
	}
	if err := w.journal.Sync(); err != nil {
		return "", fmt.Errorf("sync %s: %w", ManifestFileName, err)
	}
	w.owned[rel] = true
	w.files = append(w.files, rel)
	return rel, nil
}

// write writes data to full, a path under the output directory, creating
// its directories first. Directories are made only for a file being written,
// so every directory the export creates is implied by a manifest path.
func (w *siteWriter) write(full string, data []byte) error {
	if w == nil {
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return fsutil.WriteFile(full, data, 0o644)
	}
	rel, err := w.claim(full)
	if err != nil {
		return err
	}
	return writeAtomic(w.root, rel, data)
}

// close ends the export: with complete set, the manifest is replaced by the
// complete one; otherwise it stays in progress, listing every file written.
func (w *siteWriter) close(complete bool) error {
	defer func() { _ = w.root.Close() }()
	cerr := w.journal.Close()
	if !complete {
		return nil
	}
	if cerr != nil {
		return fmt.Errorf("close %s: %w", ManifestFileName, cerr)
	}
	return writeAtomic(w.root, ManifestFileName, w.content(manifestComplete))
}

// Files returns the paths the export's manifest lists, relative to the
// output directory with forward slashes, in the order they were written.
// Empty before Run.
func (e *Exporter) Files() []string {
	if e.site == nil {
		return nil
	}
	return slices.Clone(e.site.files)
}
