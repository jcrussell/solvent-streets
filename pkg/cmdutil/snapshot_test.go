package cmdutil

import (
	"context"
	"errors"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
)

type snapshotReaderFunc func(context.Context, string) (*db.Snapshot, error)

func (f snapshotReaderFunc) Snapshot(ctx context.Context, name string) (*db.Snapshot, error) {
	return f(ctx, name)
}

// TestLookupSnapshot: an unknown name is a usage error worded once, with the
// list hint; another read error passes through; a known name reads.
func TestLookupSnapshot(t *testing.T) {
	root := snapshotReaderFunc(func(_ context.Context, name string) (*db.Snapshot, error) {
		switch name {
		case "v1":
			return &db.Snapshot{Name: "v1"}, nil
		case "broken":
			return nil, errors.New("disk on fire")
		}
		return nil, db.ErrSnapshotNotFound
	})
	if snap, err := LookupSnapshot(context.Background(), root, "v1"); err != nil || snap.Name != "v1" {
		t.Errorf("v1: (%v, %v)", snap, err)
	}
	_, err := LookupSnapshot(context.Background(), root, "x")
	var fe *FlagError
	var h *hint.ErrHint
	if !errors.As(err, &fe) || err.Error() != `no snapshot named "x"` || !errors.As(err, &h) ||
		h.Hint != "check 'pvmt snapshots list' for available names" || !errors.Is(err, db.ErrSnapshotNotFound) {
		t.Errorf("unknown name: err = %v, want the unknown-snapshot usage error", err)
	}
	if _, err := LookupSnapshot(context.Background(), root, "broken"); err == nil || errors.As(err, &fe) {
		t.Errorf("read error: err = %v, want it passed through, not a usage error", err)
	}
}
