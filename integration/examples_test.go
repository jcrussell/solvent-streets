package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
)

// externalExampleConsumers names the example directories something outside
// examples/ depends on by path, and who depends on them. A rename is otherwise
// silent here: TestShippedExamplesLoad enumerates whatever directories exist,
// so it would happily pass on the renamed dir while the release smoke job
// broke at tag time — which is the failure shape this file exists to kill.
var externalExampleConsumers = map[string]string{
	"livermore-ca": "the release smoke job (.github/workflows/release.yaml) and TestE2ENetwork_Livermore",
	"all":          "`make site` (SITE_CONFIG_DIR in the Makefile)",
}

// TestShippedExamplesLoad locks in solvent-streets-mv2p: every example under
// examples/ must survive config.Load, so a validation rule that outdates a
// shipped config fails on the PR that adds the rule.
//
// The rule from f7l7 (a city needs overpass = true or an arcgis_url) landed
// without touching the release workflow's own hand-rolled pvmt.toml, and
// because the smoke job only runs on tag push, nothing noticed for three
// months. The smoke job now runs against examples/livermore-ca and this test
// covers it — along with the other eleven examples, which had no test at all.
//
// Load, not LoadFS: examples/all is built entirely from [[include]], which
// LoadFS rejects outright, so going through Load also catches a broken or
// renamed include target. Only the error is asserted — examples/all emits
// non-fatal load warnings by design (one per city whose calibration is
// superseded), so a "no warnings" assertion would fail on a healthy config.
func TestShippedExamplesLoad(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	loaded := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "pvmt.toml")
		if _, err := os.Stat(path); err != nil {
			continue // a subdirectory that isn't an example (build output, docs)
		}
		loaded[e.Name()] = true
		t.Run(e.Name(), func(t *testing.T) {
			if _, err := config.Load(path); err != nil {
				t.Errorf("config.Load(%s): %v", path, err)
			}
		})
	}

	for name, consumer := range externalExampleConsumers {
		if !loaded[name] {
			t.Errorf("examples/%s/pvmt.toml is missing — %s depends on that exact path", name, consumer)
		}
	}
}
