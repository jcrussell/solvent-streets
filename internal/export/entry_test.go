package export

import (
	"context"
	"errors"
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
			snaps:   []db.Snapshot{{ID: 1, ConfigHash: wantHash}},
			wantErr: nil,
		},
		{
			name: "multiple snapshots, one matches",
			snaps: []db.Snapshot{
				{ID: 1, ConfigHash: "stale"},
				{ID: 2, ConfigHash: wantHash},
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
