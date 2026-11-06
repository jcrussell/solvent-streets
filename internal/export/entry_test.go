package export

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
)

// TestRequireMatchingSnapshot covers the 8xqn fail-loud behavior: the
// exporter must reject a city whose snapshots predate the current config
// hash, but pass a city with a matching snapshot even when hex_stats is
// empty (legitimate tiny-city case).
func TestRequireMatchingSnapshot(t *testing.T) {
	cfg := &config.Config{}
	wantHash := cfg.Hash()

	cases := []struct {
		name    string
		snaps   []db.Snapshot
		wantErr error
	}{
		{
			name:    "no snapshots at all",
			snaps:   nil,
			wantErr: ErrNoMatchingSnapshot,
		},
		{
			name:    "snapshots exist but hash mismatch",
			snaps:   []db.Snapshot{{ID: 1, ConfigHash: "other-hash"}},
			wantErr: ErrNoMatchingSnapshot,
		},
		{
			name:    "snapshot with matching hash",
			snaps:   []db.Snapshot{{ID: 1, ConfigHash: wantHash, Parts: paved}},
			wantErr: nil,
		},
		{
			name: "multiple snapshots, one matches",
			snaps: []db.Snapshot{
				{ID: 1, ConfigHash: "stale"},
				{ID: 2, ConfigHash: wantHash, Parts: paved},
			},
			wantErr: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snaps := tc.snaps
			store := &dbtest.MockStore{
				ListSnapshotsFunc: func(_ context.Context) ([]db.Snapshot, error) {
					return snaps, nil
				},
			}
			entry := CityEntry{
				Config: cfg,
				City:   config.CityConfig{Name: "Test City"},
				Store:  store,
				Slug:   "test-city",
			}
			err := entry.RequireMatchingSnapshot(context.Background())
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("RequireMatchingSnapshot: got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestRequireMatchingSnapshot_IncludedCityUsesSourceHash: a city pulled in via
// [[include]] reads the snapshots its SOURCE config wrote, not the including
// config's. Without this the union config looks for a hash nothing ever wrote
// and every city fails — which is exactly why `make site` could not run.
func TestRequireMatchingSnapshot_IncludedCityUsesSourceHash(t *testing.T) {
	cfg := &config.Config{ConfigID: "union"}
	const sourceHash = "50urc3hash000000"

	city := config.CityConfig{Name: "Oakland, CA", SourceConfigID: "bay-area-ca", SourceConfigHash: sourceHash}
	entry := CityEntry{
		Config: cfg,
		City:   city,
		Slug:   city.Slug(),
		Store: &dbtest.MockStore{
			ListSnapshotsFunc: func(_ context.Context) ([]db.Snapshot, error) {
				// Only the source's snapshot exists — the union config never
				// computed anything of its own.
				return []db.Snapshot{{ID: 1, ConfigHash: sourceHash, Parts: paved}}, nil
			},
		},
	}
	if err := entry.RequireMatchingSnapshot(context.Background()); err != nil {
		t.Fatalf("included city should match its source snapshot, got %v", err)
	}

	// And the union's own hash must NOT satisfy it — that would mean the stamp
	// is being ignored.
	entry.Store = &dbtest.MockStore{
		ListSnapshotsFunc: func(_ context.Context) ([]db.Snapshot, error) {
			return []db.Snapshot{{ID: 1, ConfigHash: cfg.Hash(), Parts: paved}}, nil
		},
	}
	if err := entry.RequireMatchingSnapshot(context.Background()); !errors.Is(err, ErrNoMatchingSnapshot) {
		t.Errorf("a snapshot under the UNION's hash must not satisfy an included city, got %v", err)
	}
}

// TestRequireMatchingSnapshot_HexEdgeMismatch is the guard for the one merge
// outcome that produces a blank map instead of an error. The snapshot hash is
// the source's and matches fine; what diverges is the resolved hex edge, and
// since hex ids are derived from the grid, every stored hex would fail to join
// and the city would export zero features silently.
func TestRequireMatchingSnapshot_HexEdgeMismatch(t *testing.T) {
	const sourceHash = "50urc3hash000000"
	matching := &dbtest.MockStore{
		ListSnapshotsFunc: func(_ context.Context) ([]db.Snapshot, error) {
			return []db.Snapshot{{ID: 1, ConfigHash: sourceHash, Parts: paved}}, nil
		},
	}

	t.Run("union resolves a different edge", func(t *testing.T) {
		// The union sets a top-level grid the source never had; the city has no
		// override of its own, so it silently inherits the wrong edge.
		cfg := &config.Config{Grid: config.GridConfig{HexEdgeM: 150}}
		city := config.CityConfig{Name: "Oakland, CA", SourceConfigHash: sourceHash, SourceHexEdgeM: 100}
		entry := CityEntry{Config: cfg, City: city, Slug: city.Slug(), Store: matching}

		err := entry.RequireMatchingSnapshot(context.Background())
		if !errors.Is(err, ErrNoMatchingSnapshot) {
			t.Fatalf("want ErrNoMatchingSnapshot for a hex-edge divergence, got %v", err)
		}
		if !strings.Contains(err.Error(), "hex_edge_m") {
			t.Errorf("error should name hex_edge_m, got %q", err)
		}
	})

	t.Run("edges agree", func(t *testing.T) {
		cfg := &config.Config{Grid: config.GridConfig{HexEdgeM: 100}}
		city := config.CityConfig{Name: "Oakland, CA", SourceConfigHash: sourceHash, SourceHexEdgeM: 100}
		entry := CityEntry{Config: cfg, City: city, Slug: city.Slug(), Store: matching}
		if err := entry.RequireMatchingSnapshot(context.Background()); err != nil {
			t.Errorf("matching edges must pass, got %v", err)
		}
	})

	t.Run("unstamped city is not checked", func(t *testing.T) {
		// A directly-declared city has no source edge; the guard must not fire
		// on it regardless of what the config resolves.
		cfg := &config.Config{Grid: config.GridConfig{HexEdgeM: 150}}
		city := config.CityConfig{Name: "Bend, OR"}
		entry := CityEntry{
			Config: cfg, City: city, Slug: city.Slug(),
			Store: &dbtest.MockStore{
				ListSnapshotsFunc: func(_ context.Context) ([]db.Snapshot, error) {
					return []db.Snapshot{{ID: 1, ConfigHash: cfg.Hash(), Parts: paved}}, nil
				},
			},
		}
		if err := entry.RequireMatchingSnapshot(context.Background()); err != nil {
			t.Errorf("a directly-declared city must skip the hex-edge guard, got %v", err)
		}
	})
}

// TestPinCurrentSnapshot: an export reads ONE snapshot per city. The pin picks
// the newest snapshot for the city's current config hash, ignores other hashes,
// and keeps reading it after a later commit, so a compute that lands
// mid-export cannot mix two snapshots into one city's files.
func TestPinCurrentSnapshot(t *testing.T) {
	ctx := context.Background()
	root, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := &config.Config{}
	city := config.CityConfig{Name: "Test City"}
	id, err := root.EnsureCity(ctx, "test-city", city.Name, "test")
	if err != nil {
		t.Fatal(err)
	}
	store := root.ForCity(id)
	hash := cfg.CityHash(&city)
	commitArea := func(h string, area float64) {
		t.Helper()
		run, err := store.CreateRun(ctx, "roads", h)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveComputeResult(ctx, db.ComputeResult{ResourceType: "roads", TotalArea: area, RunID: run.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CommitSnapshot(ctx, h, map[string]int64{"roads": run.ID}); err != nil {
			t.Fatal(err)
		}
	}
	commitArea(hash, 100)
	commitArea("another-config", 999)

	entry := CityEntry{Config: cfg, City: city, Store: store.WithConfigHash(hash), Slug: "test-city"}
	pinned, err := entry.PinCurrentSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	commitArea(hash, 200) // lands "mid-export"

	r, err := pinned.Store.LatestComputeResult(ctx, "roads")
	if err != nil || r.TotalArea != 100 {
		t.Errorf("pinned read = (%+v, %v), want the 100 snapshot that was current when the export started", r, err)
	}
	if r, _ := entry.Store.LatestComputeResult(ctx, "roads"); r == nil || r.TotalArea != 200 {
		t.Errorf("unpinned entry = %+v, want the newer 200 (so the pin is what holds the export steady)", r)
	}
}

// paved is the manifest of a snapshot that holds a pavement compute, which
// RequireMatchingSnapshot requires before a city may be exported.
var paved = []db.SnapshotPart{{Part: "roads", RunID: 1}}

// TestRequireMatchingSnapshot_MetricsOnlyIsRefused: a snapshot under the
// current config that holds only metric parts (fire coverage, census) must
// not pass. After any pvmt.toml edit `pvmt services compute` alone commits
// exactly that, and exporting it publishes a blank city at exit 0.
func TestRequireMatchingSnapshot_MetricsOnlyIsRefused(t *testing.T) {
	cfg := &config.Config{}
	store := &dbtest.MockStore{ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) {
		return []db.Snapshot{
			{ID: 3, ConfigHash: cfg.Hash(), Parts: []db.SnapshotPart{{Part: "service:fire"}}},
			{ID: 2, ConfigHash: "older-config", Parts: paved},
		}, nil
	}}
	entry := CityEntry{Config: cfg, City: config.CityConfig{Name: "Test City"}, Store: store, Slug: "test-city"}
	err := entry.RequireMatchingSnapshot(context.Background())
	if !errors.Is(err, ErrNoMatchingSnapshot) || !strings.Contains(err.Error(), "no pavement") {
		t.Errorf("err = %v, want ErrNoMatchingSnapshot naming the missing pavement compute", err)
	}
}
