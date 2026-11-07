package serve

import (
	"context"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	exportpkg "github.com/jcrussell/solvent-streets/internal/export"
)

// TestPinEntries_WarnsAboutUnservableCities: serve answers HTTP 200 with an
// empty map for a city that has no snapshot under its current settings, so
// startup must name each such city once, with export's hint — and stay quiet
// about a city that has one.
func TestPinEntries_WarnsAboutUnservableCities(t *testing.T) {
	cfg := &config.Config{}
	entry := func(name string, snaps []db.Snapshot) exportpkg.CityEntry {
		city := config.CityConfig{Name: name}
		return exportpkg.CityEntry{
			Config: cfg, City: city, Slug: city.Slug(),
			Store: &dbtest.MockStore{ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) { return snaps, nil }},
		}
	}
	cur := cfg.CityHash(&config.CityConfig{Name: "x"})
	entries := []exportpkg.CityEntry{
		entry("Fresh City", []db.Snapshot{{ID: 1, ConfigHash: cur, Parts: []db.SnapshotPart{{Part: "roads", RunID: 1}}}}),
		entry("Stale City", []db.Snapshot{{ID: 2, ConfigHash: "0ld0ld0ld0ld0ld0", Parts: []db.SnapshotPart{{Part: "roads", RunID: 2}}}}),
	}

	var out strings.Builder
	if err := pinEntries(context.Background(), entries, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "warning:") != 1 || !strings.Contains(got, "Stale City") || strings.Contains(got, "Fresh City") {
		t.Errorf("want one warning, for Stale City only; got:\n%s", got)
	}
	if want := cfg.StaleHint(&entries[1].City); !strings.Contains(got, "hint: "+want) {
		t.Errorf("warning lacks the shared hint %q; got:\n%s", want, got)
	}
}
