package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

func manifestTestEntries(cfg *config.Config, slugs ...string) []CityEntry {
	totals := map[resource.Type]results.Totals{resource.TypeRoads: {Area: 1000, Features: 10}}
	boundaries := []string{exportBoundaryA, exportBoundaryB}
	var entries []CityEntry
	for i, slug := range slugs {
		entries = append(entries, exportTestEntry(cfg, strings.ToUpper(slug), slug, boundaries[i%2], totals))
	}
	return entries
}

// runExport is what 'pvmt export [--clean]' does once the command checked its
// flags: PrepareOutput, then Run.
func runExport(t *testing.T, dir string, clean bool, slugs ...string) (*Exporter, error) {
	t.Helper()
	cfg := &config.Config{}
	e := New(manifestTestEntries(cfg, slugs...), cfg, dir, "metric", testAssets(t))
	if err := e.Pin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.PrepareOutput(clean); err != nil {
		return e, err
	}
	return e, e.Run(context.Background())
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (%v), want %q untouched", rel, got, err, want)
		}
	}
}

// TestExport_ManifestListsEveryFileWritten: a finished export's manifest is
// complete and lists exactly the files under the site, the manifest aside.
func TestExport_ManifestListsEveryFileWritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	e, err := runExport(t, dir, false, "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	m, err := readManifestIn(t, dir)
	if err != nil || !m.complete {
		t.Fatalf("manifest = %+v, %v; want a complete one", m, err)
	}
	var onDisk []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			if rel != ManifestFileName {
				onDisk = append(onDisk, filepath.ToSlash(rel))
			}
		}
		return err
	})
	slices.Sort(onDisk)
	listed := slices.Sorted(slices.Values(m.files))
	if !slices.Equal(onDisk, listed) {
		t.Errorf("manifest lists %v, on disk %v", listed, onDisk)
	}
	if !slices.Equal(e.Files(), m.files) {
		t.Errorf("Files() = %v, want the manifest's %v", e.Files(), m.files)
	}
}

// TestPrepareOutput_SecondExportRemovesStaleFiles: --clean over an earlier
// export removes that export's files, so a city the new export does not write
// is gone, while files pvmt did not write (the user's pvmt.toml, a Hugo tree)
// stay where they are.
func TestPrepareOutput_SecondExportRemovesStaleFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if _, err := runExport(t, dir, false, "a", "b"); err != nil {
		t.Fatal(err)
	}
	foreign := map[string]string{
		"pvmt.toml":              "[[cities]]\n",
		"config.toml":            "baseURL = 'x'\n",
		"content/posts/hello.md": "# hello\n",
		"cities/notes.txt":       "mine\n",
		".git/config":            "[core]\n", // a GitHub Pages checkout of the site
	}
	writeFiles(t, dir, foreign)

	if _, err := runExport(t, dir, true, "a", "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cities", "b")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("city b's files from the first export survived --clean (stat err %v)", err)
	}
	for _, rel := range []string{"cities/a/data/meta.json", "cities/c/data/meta.json", IndexFileName} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("second export did not write %s: %v", rel, err)
		}
	}
	assertFiles(t, dir, foreign)
}

// TestPrepareOutput_RefusesWithoutManifest: --clean deletes nothing in a
// non-empty directory with no manifest (a Hugo site, say) or with the marker
// an older pvmt export wrote, which lists no files. An empty directory is
// used as is.
func TestPrepareOutput_RefusesWithoutManifest(t *testing.T) {
	hugo := map[string]string{"index.html": "<html>hugo", "config.toml": "x", "public/app.js": "y"}
	for name, files := range map[string]map[string]string{
		"hugo":   hugo,
		"legacy": {ManifestFileName: "pvmt export in progress\n", "index.html": "<html>old", "pvmt.wasm": "w"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, files)
			_, err := runExport(t, dir, true, "a")
			if _, ok := errors.AsType[*hint.ErrHint](err); !ok {
				t.Fatalf("PrepareOutput = %v, want a hinted refusal", err)
			}
			assertFiles(t, dir, files)
		})
	}
	if _, err := runExport(t, t.TempDir(), false, "a"); err != nil {
		t.Errorf("export into an empty directory: %v", err)
	}
}

// TestPrepareOutput_RefusesPlantedEntries: a manifest entry that leaves the
// directory, by "..", an absolute path or a symlink, refuses the whole clean,
// and nothing is removed, inside or out.
func TestPrepareOutput_RefusesPlantedEntries(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	for name, entry := range map[string]string{
		"dotdot":       "../" + filepath.Base(outside) + "/victim.txt",
		"absolute":     victim,
		"symlink dir":  "out/victim.txt",
		"symlink file": "victim-link",
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
				t.Fatal(err)
			}
			parent := t.TempDir()
			dir := filepath.Join(parent, "site")
			writeFiles(t, dir, map[string]string{"index.html": "pvmt"})
			if err := os.Symlink(outside, filepath.Join(dir, "out")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, filepath.Join(dir, "victim-link")); err != nil {
				t.Fatal(err)
			}
			manifest := manifestHeader + "\n" + manifestComplete + "\nindex.html\n" + entry + "\n"
			writeFiles(t, dir, map[string]string{ManifestFileName: manifest})

			if _, err := runExport(t, dir, true, "a"); err == nil {
				t.Fatal("PrepareOutput accepted a manifest entry outside the site")
			}
			assertFiles(t, outside, map[string]string{"victim.txt": "keep"})
			assertFiles(t, dir, map[string]string{"index.html": "pvmt"})
		})
	}
}

// TestPrepareOutput_RefusesForeignCollision: a file this export would write
// that the manifest does not list refuses the export before anything is
// removed.
func TestPrepareOutput_RefusesForeignCollision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if _, err := runExport(t, dir, false, "a"); err != nil {
		t.Fatal(err)
	}
	m, err := readManifestIn(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Drop play.html from the manifest and make it the user's.
	var kept []string
	for _, f := range m.files {
		if f != PlayFileName {
			kept = append(kept, f)
		}
	}
	writeFiles(t, dir, map[string]string{
		ManifestFileName: manifestHeader + "\n" + manifestComplete + "\n" + strings.Join(kept, "\n") + "\n",
		PlayFileName:     "mine",
	})
	_, err = runExport(t, dir, true, "a")
	if err == nil || !strings.Contains(err.Error(), PlayFileName) {
		t.Fatalf("PrepareOutput = %v, want a refusal naming %s", err, PlayFileName)
	}
	assertFiles(t, dir, map[string]string{PlayFileName: "mine"})
	if _, err := os.Stat(filepath.Join(dir, IndexFileName)); err != nil {
		t.Errorf("a refused clean removed index.html: %v", err)
	}
}

// TestRun_RefusesToOverwriteForeignFile: Run itself never overwrites a file
// the manifest does not list, even without PrepareOutput.
func TestRun_RefusesToOverwriteForeignFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{IndexFileName: "mine"})
	cfg := &config.Config{}
	err := New(manifestTestEntries(cfg, "a"), cfg, dir, "metric", testAssets(t)).Run(context.Background())
	if err == nil {
		t.Fatal("Run overwrote a file it did not write")
	}
	assertFiles(t, dir, map[string]string{IndexFileName: "mine"})
	if m, err := readManifestIn(t, dir); err != nil || m.complete {
		t.Errorf("manifest after a refused run = %+v, %v; want it in progress", m, err)
	}
}

// TestPrepareOutput_KeepsTheDirectory: the output directory itself is never
// removed and recreated, so its mode (setgid, group bits) survives both an
// export into it and a --clean of it.
func TestPrepareOutput_KeepsTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o2770|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(dir)
	for _, clean := range []bool{false, true} {
		if _, err := runExport(t, dir, clean, "a"); err != nil {
			t.Fatal(err)
		}
		after, _ := os.Stat(dir)
		if after.Mode() != before.Mode() || !os.SameFile(before, after) {
			t.Errorf("clean=%v: directory mode %v -> %v, same=%v", clean, before.Mode(), after.Mode(), os.SameFile(before, after))
		}
	}
}

// TestPrepareOutput_SymlinkSwappedAfterCheck: a directory the manifest's
// paths run through, swapped for a symlink to elsewhere between the checks
// and the removals, steers no removal outside the site.
func TestPrepareOutput_SymlinkSwappedAfterCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if _, err := runExport(t, dir, false, "a"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victims := map[string]string{"meta.json": "keep", "hexgrid.geojson": "keep"}
	writeFiles(t, outside, victims)
	afterCleanCheck = func() {
		if err := os.RemoveAll(filepath.Join(dir, DataDirName)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, DataDirName)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterCleanCheck = nil })
	if _, err := runExport(t, dir, true, "a"); err == nil {
		t.Error("--clean went through a directory swapped for a symlink")
	}
	assertFiles(t, outside, victims)
}

// TestSiteWriter_SymlinkSwappedMidExport: a directory swapped for a symlink
// to elsewhere while an export runs takes no write outside the site.
func TestSiteWriter_SymlinkSwappedMidExport(t *testing.T) {
	dir := t.TempDir()
	w, err := openSiteIn(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.close(false) })
	if err := w.write(filepath.Join(dir, "data", "a.json"), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(dir, "data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "data")); err != nil {
		t.Fatal(err)
	}
	if err := w.write(filepath.Join(dir, "data", "b.json"), []byte("{}")); err == nil {
		t.Error("write went through a directory swapped for a symlink")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("the write left %v outside the site", ents)
	}
}

// TestPrepareOutput_RemovesStaleTempFiles: a temp file an interrupted write of
// a listed file left is removed by --clean; one standing in for a file the
// manifest does not list stays.
func TestPrepareOutput_RemovesStaleTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if _, err := runExport(t, dir, false, "a"); err != nil {
		t.Fatal(err)
	}
	stale := "data/meta.json" + tempSuffix + "123"
	foreign := map[string]string{"notes.txt" + tempSuffix + "9": "mine"}
	writeFiles(t, dir, map[string]string{stale: "half", ManifestFileName + tempSuffix + "7": "half"})
	writeFiles(t, dir, foreign)
	if err := New(manifestTestEntries(&config.Config{}, "a"), &config.Config{}, dir, "metric", testAssets(t)).PrepareOutput(true); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{stale, ManifestFileName + tempSuffix + "7"} {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale temp file %s survived --clean (%v)", rel, err)
		}
	}
	assertFiles(t, dir, foreign)
}

// TestPrepareOutput_NewerManifest: a manifest in a format this pvmt does not
// read is refused as such, not as an older export's marker.
func TestPrepareOutput_NewerManifest(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{ManifestFileName: "pvmt export manifest v2\nstate: complete\nindex.html\n", "index.html": "x"})
	_, err := runExport(t, dir, true, "a")
	if err == nil || !strings.Contains(err.Error(), "newer pvmt") || strings.Contains(err.Error(), "older") {
		t.Errorf("PrepareOutput = %v, want a refusal naming a newer pvmt", err)
	}
	assertFiles(t, dir, map[string]string{"index.html": "x"})
}

// readManifestIn reads dir's manifest through an os.Root of its own.
func readManifestIn(t *testing.T, dir string) (*siteManifest, error) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	return readManifest(root)
}

// openSiteIn opens dir's site writer, as Run does without PrepareOutput.
func openSiteIn(t *testing.T, dir string) (*siteWriter, error) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return openSite(dir, root)
}
