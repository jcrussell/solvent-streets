package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/hint"
)

// TestRequireMatchingSnapshot covers the 8xqn fail-loud behavior: the
// exporter must reject a city whose snapshots predate the current config
// hash, but pass a city with a matching snapshot even when hex_stats is
// empty (legitimate tiny-city case).
func TestRequireMatchingSnapshot(t *testing.T) {
	cfg := &config.Config{}
	wantHash := cfg.CityHash(nil)

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
			return []db.Snapshot{{ID: 1, ConfigHash: cfg.CityHash(nil), Parts: paved}}, nil
		},
	}
	if err := entry.RequireMatchingSnapshot(context.Background()); !errors.Is(err, ErrNoMatchingSnapshot) {
		t.Errorf("a snapshot under the UNION's hash must not satisfy an included city, got %v", err)
	}
}

// TestRequireMatchingSnapshot_IncludedDataDivergence: an [[include]]d city
// whose data inputs this config resolves differently from its own config gets
// its own hash (Config.CityHash), so the source's snapshots stop satisfying it
// — and the error says which setting diverged rather than just "recompute".
// The divergence used is solvent-streets-qtue: the merge drops an included
// file's [[services]].
func TestRequireMatchingSnapshot_IncludedDataDivergence(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, data string) string {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("leaf/pvmt.toml", "config_id = \"leaf\"\n[[services]]\nname = \"fire\"\nturnout_s = 60\n"+
		"[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	cfg, err := config.Load(write("all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	city := cfg.Cities[0]
	entry := CityEntry{
		Config: cfg, City: city, Slug: city.Slug(),
		Store: &dbtest.MockStore{
			ListSnapshotsFunc: func(_ context.Context) ([]db.Snapshot, error) {
				// Only the leaf's own snapshot exists.
				return []db.Snapshot{{ID: 1, ConfigHash: city.SourceConfigHash, Parts: paved}}, nil
			},
		},
	}
	err = entry.RequireMatchingSnapshot(context.Background())
	if !errors.Is(err, ErrNoMatchingSnapshot) {
		t.Fatalf("want ErrNoMatchingSnapshot, got %v", err)
	}
	var h *hint.ErrHint
	if !errors.As(err, &h) || !strings.Contains(h.Hint, "[[services]]") || !strings.Contains(h.Hint, "[[include]]") {
		t.Errorf("hint should name the [[include]] and the diverging [[services]], got %v", err)
	}
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
			{ID: 3, ConfigHash: cfg.CityHash(nil), Parts: []db.SnapshotPart{{Part: "service:fire"}}},
			{ID: 2, ConfigHash: "older-config", Parts: paved},
		}, nil
	}}
	entry := CityEntry{Config: cfg, City: config.CityConfig{Name: "Test City"}, Store: store, Slug: "test-city"}
	err := entry.RequireMatchingSnapshot(context.Background())
	if !errors.Is(err, ErrNoMatchingSnapshot) || !strings.Contains(err.Error(), "no pavement") {
		t.Errorf("err = %v, want ErrNoMatchingSnapshot naming the missing pavement compute", err)
	}
}

// TestRequireMatchingSnapshot_StaleSaysSettingsChanged: a city computed only
// under other settings gets the shared "settings changed" hint (the one
// forecast and status print too), and a never-computed city a different one.
func TestRequireMatchingSnapshot_StaleSaysSettingsChanged(t *testing.T) {
	cfg := &config.Config{}
	entryWith := func(snaps []db.Snapshot) CityEntry {
		return CityEntry{
			Config: cfg, City: config.CityConfig{Name: "Test City"}, Slug: "test-city",
			Store: &dbtest.MockStore{ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) { return snaps, nil }},
		}
	}
	hintOf := func(err error) string {
		t.Helper()
		var h *hint.ErrHint
		if !errors.As(err, &h) {
			t.Fatalf("want a hinted error, got %v", err)
		}
		return h.Hint
	}

	stale := hintOf(entryWith([]db.Snapshot{{ID: 1, ConfigHash: "0123456789abcdef", Parts: paved}}).
		RequireMatchingSnapshot(context.Background()))
	if want := config.StaleSnapshotsHint("test-city", cfg.CityHash(nil)); stale != want {
		t.Errorf("stale hint = %q; want the shared StaleSnapshotsHint %q", stale, want)
	}
	never := hintOf(entryWith(nil).RequireMatchingSnapshot(context.Background()))
	if !strings.Contains(never, "never been computed") {
		t.Errorf("never-computed hint = %q; want it to say so", never)
	}
}

// TestMissingParts is solvent-streets-ern4.12 (a): a settings change followed
// by a partial recompute leaves the current snapshot without parts the city had
// under its earlier settings, and export would publish it without them.
func TestMissingParts(t *testing.T) {
	cfg := &config.Config{}
	cur := cfg.CityHash(nil)
	parts := func(names ...string) []db.SnapshotPart {
		out := make([]db.SnapshotPart, len(names))
		for i, n := range names {
			out[i] = db.SnapshotPart{Part: n, RunID: int64(i + 1)}
		}
		return out
	}
	full := parts("roads", "parking", "sidewalks", "combined", "forecast:roads", "service:fire", "census")
	off := false

	for _, tc := range []struct {
		name     string
		city     config.CityConfig
		snaps    []db.Snapshot // newest first
		want     []string
		wantPrev int64
	}{
		{
			name: "partial recompute after a settings change",
			snaps: []db.Snapshot{
				{ID: 3, ConfigHash: cur, Parts: parts("roads", "forecast:roads")},
				{ID: 2, ConfigHash: "0ld0ld0ld0ld0ld0", Parts: full},
			},
			want:     []string{"parking", "sidewalks", "combined", "service:fire", "census"},
			wantPrev: 2,
		},
		{
			name: "everything recomputed",
			snaps: []db.Snapshot{
				{ID: 3, ConfigHash: cur, Parts: full},
				{ID: 2, ConfigHash: "0ld0ld0ld0ld0ld0", Parts: full},
			},
		},
		{
			name: "dropped under the current settings is deliberate",
			snaps: []db.Snapshot{
				{ID: 4, ConfigHash: cur, Parts: parts("roads", "parking", "sidewalks", "combined", "forecast:roads", "census")},
				{ID: 3, ConfigHash: cur, Parts: full},
				{ID: 2, ConfigHash: "0ld0ld0ld0ld0ld0", Parts: full},
			},
		},
		{
			name: "census = false is not reported",
			city: config.CityConfig{Census: &off},
			snaps: []db.Snapshot{
				{ID: 3, ConfigHash: cur, Parts: parts("roads", "parking", "sidewalks", "combined", "forecast:roads", "service:fire")},
				{ID: 2, ConfigHash: "0ld0ld0ld0ld0ld0", Parts: full},
			},
		},
		{
			name:  "no other settings",
			snaps: []db.Snapshot{{ID: 1, ConfigHash: cur, Parts: parts("roads")}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			city := tc.city
			city.Name = "Test City"
			snaps := tc.snaps
			entry := CityEntry{
				Config: cfg, City: city, Slug: "test-city",
				Store: &dbtest.MockStore{ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) { return snaps, nil }},
			}
			got, prev, err := entry.MissingParts(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			// prev only means something when a part is missing.
			if strings.Join(got, ",") != strings.Join(tc.want, ",") || (len(tc.want) > 0 && prev != tc.wantPrev) {
				t.Errorf("MissingParts = %v (prev %d); want %v (prev %d)", got, prev, tc.want, tc.wantPrev)
			}
		})
	}
}

// TestExport_WarnsAboutMissingParts: the export still succeeds (the rest of
// the city is current) but says what it is leaving out.
func TestExport_WarnsAboutMissingParts(t *testing.T) {
	cfg := &config.Config{}
	entry := exportTestEntry(cfg, "Solo City", "solo-city", exportBoundaryA, nil)
	cur := cfg.CityHash(&entry.City)
	entry.Store.(*dbtest.MockStore).ListSnapshotsFunc = func(context.Context) ([]db.Snapshot, error) {
		return []db.Snapshot{
			{ID: 2, ConfigHash: cur, Parts: paved},
			{ID: 1, ConfigHash: "0ld0ld0ld0ld0ld0", Parts: append([]db.SnapshotPart{{Part: "census", RunID: 9}}, paved...)},
		}, nil
	}
	var warn strings.Builder
	e := New([]CityEntry{entry}, cfg, t.TempDir(), "metric", testAssets(t))
	e.SetErrOut(&warn)
	if err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(warn.String(), "census") || !strings.Contains(warn.String(), "snapshot 1") {
		t.Errorf("warnings = %q; want one naming census and snapshot 1", warn.String())
	}
}
