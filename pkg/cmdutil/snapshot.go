package cmdutil

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
)

// UnknownSnapshotError is the usage error (exit 2) for a snapshot name no
// snapshot has, the one wording every command that takes a name gives:
// `no snapshot named "<name>"`, with a hint naming 'pvmt snapshots list'.
// It wraps db.ErrSnapshotNotFound.
func UnknownSnapshotError(name string) error {
	return hint.With(&FlagError{Err: unknownSnapshot(name)}, "check 'pvmt snapshots list' for available names")
}

// unknownSnapshot reads as its own message, without the sentinel's text.
type unknownSnapshot string

func (n unknownSnapshot) Error() string { return fmt.Sprintf("no snapshot named %q", string(n)) }
func (unknownSnapshot) Unwrap() error   { return db.ErrSnapshotNotFound }

// SnapshotReader reads one snapshot by name (db.RootStorer.Snapshot).
type SnapshotReader interface {
	Snapshot(ctx context.Context, name string) (*db.Snapshot, error)
}

// LookupSnapshot reads the snapshot named name, refusing a name no snapshot
// has with UnknownSnapshotError.
func LookupSnapshot(ctx context.Context, root SnapshotReader, name string) (*db.Snapshot, error) {
	snap, err := root.Snapshot(ctx, name)
	if errors.Is(err, db.ErrSnapshotNotFound) {
		return nil, UnknownSnapshotError(name)
	}
	if err != nil {
		return nil, fmt.Errorf("snapshot %q: %w", name, err)
	}
	return snap, nil
}
