package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
)

// ErrSnapshotExists is returned by CreateSnapshot when the name is taken.
// Snapshots are immutable: replacing one is delete, then create.
var ErrSnapshotExists = errors.New("snapshot already exists")

// ErrSnapshotNotFound is returned by RootStorer.Snapshot for an unknown name.
var ErrSnapshotNotFound = errors.New("snapshot not found")

// snapshotNameRE is the one statement of what a snapshot name may be: like a
// git ref component, it starts with a letter or digit (so never a flag) and
// holds letters, digits, '.', '_' and '-', at most 64 characters, no '/'.
var snapshotNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SnapshotNameRule describes snapshotNameRE for an error message.
const SnapshotNameRule = "1-64 letters, digits, '.', '_' or '-', starting with a letter or digit"

// LatestComputeName is reserved: pvmt serve's ?snapshot=latest names a city's
// latest compute, so no snapshot may take it.
const LatestComputeName = "latest"

// CheckSnapshotName returns an error naming the rule when name is not a valid
// snapshot name: snapshotNameRE, and not LatestComputeName.
func CheckSnapshotName(name string) error {
	if !snapshotNameRE.MatchString(name) {
		return fmt.Errorf("invalid snapshot name %q: want %s", name, SnapshotNameRule)
	}
	if name == LatestComputeName {
		return fmt.Errorf("invalid snapshot name %q: it is reserved for the latest compute (pvmt serve's ?snapshot=latest)", name)
	}
	return nil
}

// CityRefusal is one city CreateSnapshot refused: Err is the city's check
// error unchanged, or ErrNothingComputed.
type CityRefusal struct {
	CityID int64
	Err    error
}

// SnapshotRefusedError is CreateSnapshot's error when any city was refused:
// nothing was written, and Refusals lists every refused city in request
// order.
type SnapshotRefusedError struct {
	Name     string
	Refusals []CityRefusal
}

func (e *SnapshotRefusedError) Error() string {
	return fmt.Sprintf("snapshot %s not created: %d city(ies) refused", e.Name, len(e.Refusals))
}

// The root-level snapshot statements (TestQueryPlan_SnapshotStatements).
const (
	sqlSnapshotByName = `SELECT id FROM snapshots WHERE name = ?`
	sqlListSnapshots  = `SELECT s.id, s.name, s.created_at, s.compute_epoch,
			(SELECT COUNT(*) FROM snapshot_cities c WHERE c.snapshot_id = s.id)
		FROM snapshots s ORDER BY s.id DESC`
	// sqlSnapshotRuns: every (city, run) a snapshot's manifests hold.
	sqlSnapshotRuns = `SELECT city_id, run_id FROM snapshot_parts WHERE snapshot_id = ? ORDER BY city_id`
)

// CreateSnapshot freezes every city of cities into one new snapshot. See the
// RootStorer interface for the contract.
func (r *RootStore) CreateSnapshot(ctx context.Context, name string, computeEpoch int, cities []SnapshotRequest) (*Snapshot, error) {
	if err := CheckSnapshotName(name); err != nil {
		return nil, err
	}
	if len(cities) == 0 {
		return nil, errors.New("create snapshot: no cities")
	}
	seen := make(map[int64]bool, len(cities))
	for _, c := range cities {
		switch {
		case c.ConfigHash == "":
			return nil, fmt.Errorf("create snapshot: city %d: config hash is required", c.CityID)
		case c.HexEdgeM <= 0:
			return nil, fmt.Errorf("create snapshot: city %d: hex edge must be positive, got %v", c.CityID, c.HexEdgeM)
		case seen[c.CityID]:
			return nil, fmt.Errorf("create snapshot: city %d listed twice", c.CityID)
		}
		seen[c.CityID] = true
	}
	var snap *Snapshot
	err := retryBusy(ctx, func() error {
		return withTx(ctx, r.db, func(tx *sql.Tx) error {
			var err error
			snap, err = createSnapshot(ctx, tx, name, computeEpoch, cities)
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// createSnapshot is CreateSnapshot's body inside its IMMEDIATE transaction:
// every check sees exactly the inputs and manifest that are frozen. Any
// refusal returns an error, which rolls the whole snapshot back.
func createSnapshot(ctx context.Context, tx *sql.Tx, name string, computeEpoch int, cities []SnapshotRequest) (*Snapshot, error) {
	var taken int64
	switch err := tx.QueryRowContext(ctx, sqlSnapshotByName, name).Scan(&taken); {
	case err == nil:
		return nil, fmt.Errorf("snapshot %s: %w", name, ErrSnapshotExists)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("look up snapshot %s: %w", name, err)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO snapshots (name, created_at, compute_epoch) VALUES (?, ?, ?)`,
		name, nowUnix(), computeEpoch)
	if err != nil {
		return nil, fmt.Errorf("create snapshot: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create snapshot id: %w", err)
	}
	refused := &SnapshotRefusedError{Name: name}
	for _, c := range cities {
		refusal, err := freezeCity(ctx, tx, id, c)
		if err != nil {
			return nil, err
		}
		if refusal != nil {
			refused.Refusals = append(refused.Refusals, CityRefusal{CityID: c.CityID, Err: refusal})
		}
	}
	if len(refused.Refusals) > 0 {
		return nil, refused
	}
	return snapshotByID(ctx, tx, id)
}

// ListSnapshots returns every snapshot newest first. See RootStorer.
func (r *RootStore) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	rows, err := r.db.QueryContext(ctx, sqlListSnapshots)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Snapshot
	for rows.Next() {
		var s Snapshot
		if err := rows.Scan(&s.ID, &s.Name, unixTime{&s.CreatedAt}, &s.ComputeEpoch, &s.CityCount); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Snapshot returns the snapshot named name with its cities. See RootStorer.
func (r *RootStore) Snapshot(ctx context.Context, name string) (*Snapshot, error) {
	var id int64
	switch err := r.db.QueryRowContext(ctx, sqlSnapshotByName, name).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("snapshot %s: %w", name, ErrSnapshotNotFound)
	case err != nil:
		return nil, fmt.Errorf("look up snapshot %s: %w", name, err)
	}
	snap, err := snapshotByID(ctx, r.db, id)
	if errors.Is(err, sql.ErrNoRows) { // deleted between the two reads
		return nil, fmt.Errorf("snapshot %s: %w", name, ErrSnapshotNotFound)
	}
	return snap, err
}

// snapshotByID reads one snapshot with every city it covers, ordered by
// slug. sql.ErrNoRows when there is no such snapshot.
func snapshotByID(ctx context.Context, q queryer, id int64) (*Snapshot, error) {
	snap := &Snapshot{ID: id}
	if err := q.QueryRowContext(ctx, `SELECT name, created_at, compute_epoch FROM snapshots WHERE id = ?`, id).
		Scan(&snap.Name, unixTime{&snap.CreatedAt}, &snap.ComputeEpoch); err != nil {
		return nil, err
	}
	cities, err := listSnapshotCities(ctx, q, `c.snapshot_id = ?`, `ci.slug, c.city_id`, id)
	if err != nil {
		return nil, err
	}
	snap.Cities, snap.CityCount = cities, len(cities)
	return snap, nil
}

// DeleteSnapshot removes the snapshot named name. See RootStorer.
func (r *RootStore) DeleteSnapshot(ctx context.Context, name string) (bool, error) {
	var deleted bool
	err := withTx(ctx, r.db, func(tx *sql.Tx) error {
		var id int64
		switch err := tx.QueryRowContext(ctx, sqlSnapshotByName, name).Scan(&id); {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("look up snapshot %s: %w", name, err)
		}
		runs, err := snapshotRuns(ctx, tx, id)
		if err != nil {
			return err
		}
		// ON DELETE CASCADE removes its snapshot_cities and snapshot_parts.
		if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete snapshot %s: %w", name, err)
		}
		for _, city := range slices.Sorted(maps.Keys(runs)) {
			unreferenced, err := unreferencedRuns(ctx, tx, runs[city])
			if err != nil {
				return err
			}
			if _, err := deleteRuns(ctx, tx, city, unreferenced); err != nil {
				return err
			}
			if err := sweepSnapshotBoundaries(ctx, tx, city); err != nil {
				return err
			}
		}
		deleted = true
		return nil
	})
	return deleted, err
}

// snapshotRuns returns the runs a snapshot's manifests hold, per city.
func snapshotRuns(ctx context.Context, q queryer, snapshotID int64) (map[int64][]int64, error) {
	rows, err := q.QueryContext(ctx, sqlSnapshotRuns, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("query snapshot runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]int64{}
	for rows.Next() {
		var city, run int64
		if err := rows.Scan(&city, &run); err != nil {
			return nil, fmt.Errorf("scan snapshot run: %w", err)
		}
		out[city] = append(out[city], run)
	}
	return out, rows.Err()
}
