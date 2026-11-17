package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// ErrNothingComputed is returned by CreateSnapshot when the city has no
// latest compute under the config hash: a snapshot is never empty.
var ErrNothingComputed = errors.New("nothing computed under the current settings")

// LatestPart is one part of a city's latest compute (working_parts): the run
// that fills it, when that run began, and the input digests it recorded
// (run_inputs).
type LatestPart struct {
	Part       string            `json:"part"`
	RunID      int64             `json:"run_id"`
	ComputedAt time.Time         `json:"computed_at"`
	Inputs     map[string]string `json:"inputs"`
}

// CheckFunc judges whether a city's latest compute may be frozen as a
// snapshot, given its parts and the city's current input states. A non-nil
// error refuses the snapshot and is returned by CreateSnapshot unchanged.
type CheckFunc func(parts []LatestPart, inputs []InputState) error

// LatestCompute returns this city's latest compute under configHash, ordered
// by part. No parts returns (nil, nil).
func (s *sqliteStore) LatestCompute(ctx context.Context, configHash string) ([]LatestPart, error) {
	return latestCompute(ctx, s.db, s.cityID, configHash)
}

func latestCompute(ctx context.Context, q queryer, cityID int64, configHash string) ([]LatestPart, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT w.part, w.run_id, r.computed_at, i.input, i.digest
		 FROM working_parts w
		 JOIN runs r ON r.id = w.run_id
		 LEFT JOIN run_inputs i ON i.run_id = w.run_id
		 WHERE w.city_id = ? AND w.config_hash = ?
		 ORDER BY w.part, i.input`, cityID, configHash)
	if err != nil {
		return nil, fmt.Errorf("query latest compute: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []LatestPart
	for rows.Next() {
		var (
			p             LatestPart
			input, digest sql.NullString
		)
		if err := rows.Scan(&p.Part, &p.RunID, unixTime{&p.ComputedAt}, &input, &digest); err != nil {
			return nil, fmt.Errorf("scan latest compute: %w", err)
		}
		if len(out) == 0 || out[len(out)-1].Part != p.Part {
			p.Inputs = map[string]string{}
			out = append(out, p)
		}
		if input.Valid {
			out[len(out)-1].Inputs[input.String] = digest.String
		}
	}
	return out, rows.Err()
}

// inputStates reads every city_inputs row of the city, ordered by input.
func inputStates(ctx context.Context, q queryer, cityID int64) ([]InputState, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT input, digest, ingested_at FROM city_inputs WHERE city_id = ? ORDER BY input`, cityID)
	if err != nil {
		return nil, fmt.Errorf("query inputs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []InputState
	for rows.Next() {
		var st InputState
		if err := rows.Scan(&st.Input, &st.Digest, unixTime{&st.IngestedAt}); err != nil {
			return nil, fmt.Errorf("scan input: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// CreateSnapshot freezes the latest compute as a snapshot. See the Store
// interface for the contract. It retries while another process holds the
// write lock, like CommitLatest.
func (s *sqliteStore) CreateSnapshot(ctx context.Context, configHash string, hexEdgeM float64, check CheckFunc) (*Snapshot, bool, error) {
	if configHash == "" {
		return nil, false, errors.New("create snapshot: config hash is required")
	}
	if hexEdgeM <= 0 {
		return nil, false, fmt.Errorf("create snapshot: hex edge must be positive, got %v", hexEdgeM)
	}
	var (
		snap    *Snapshot
		created bool
	)
	err := retryBusy(ctx, func() error {
		var err error
		snap, created, err = s.createSnapshot(ctx, configHash, hexEdgeM, check)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return snap, created, nil
}

// createSnapshot is one attempt at CreateSnapshot, in one IMMEDIATE
// transaction: the check sees exactly the inputs and manifest that are
// frozen, and no ingest or compute can land in between.
func (s *sqliteStore) createSnapshot(ctx context.Context, configHash string, hexEdgeM float64, check CheckFunc) (*Snapshot, bool, error) {
	var (
		snap    *Snapshot
		created bool
	)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		snap, created, err = s.freezeLatest(ctx, tx, configHash, hexEdgeM, check)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return snap, created, nil
}

// freezeLatest is createSnapshot's body inside its transaction.
func (s *sqliteStore) freezeLatest(ctx context.Context, tx *sql.Tx, configHash string, hexEdgeM float64, check CheckFunc) (*Snapshot, bool, error) {
	parts, err := latestCompute(ctx, tx, s.cityID, configHash)
	if err != nil {
		return nil, false, err
	}
	states, err := inputStates(ctx, tx, s.cityID)
	if err != nil {
		return nil, false, err
	}
	// Nothing computed is reported as such before check runs: the check
	// refuses an empty compute too, but would describe each ingested
	// resource as never computed, hiding that the city was computed under
	// other settings (solvent-streets-vx0s).
	if len(parts) == 0 {
		return nil, false, ErrNothingComputed
	}
	if check != nil {
		if err := check(parts, states); err != nil {
			return nil, false, err
		}
	}
	manifest := make(map[string]int64, len(parts))
	for _, p := range parts {
		manifest[p.Part] = p.RunID
	}
	id, err := s.unchangedSnapshot(ctx, tx, configHash, hexEdgeM, manifest, states)
	if err != nil {
		return nil, false, err
	}
	created := id == 0
	if created {
		if id, err = s.insertSnapshot(ctx, tx, configHash, hexEdgeM, manifest); err != nil {
			return nil, false, err
		}
	}
	snaps, err := listSnapshots(ctx, tx, s.cityID, id)
	if err != nil {
		return nil, false, err
	}
	return &snaps[0], created, nil
}

// unchangedSnapshot returns the id of the city's newest snapshot when it
// already freezes exactly this: the same config hash, hex edge, boundary and
// manifest. 0 when there is no such snapshot.
func (s *sqliteStore) unchangedSnapshot(ctx context.Context, tx *sql.Tx, configHash string, hexEdgeM float64, manifest map[string]int64, states []InputState) (int64, error) {
	newest, err := newestSnapshotID(ctx, tx, s.cityID, "")
	if err != nil || newest == 0 {
		return 0, err
	}
	snaps, err := listSnapshots(ctx, tx, s.cityID, newest)
	if err != nil || len(snaps) == 0 {
		return 0, err
	}
	n := snaps[0]
	boundary := ""
	for _, st := range states {
		if st.Input == InputBoundary {
			boundary = st.Digest
		}
	}
	if n.ConfigHash != configHash || n.HexEdgeM != hexEdgeM || n.BoundaryDigest != boundary {
		return 0, nil
	}
	have := make(map[string]int64, len(n.Parts))
	for _, p := range n.Parts {
		have[p.Part] = p.RunID
	}
	if !maps.Equal(have, manifest) {
		return 0, nil
	}
	return n.ID, nil
}

// retryBusy runs attempt until it does not fail with isBusy, for up to
// commitBusyBudget, backing off between tries.
func retryBusy(ctx context.Context, attempt func() error) error {
	deadline := time.Now().Add(commitBusyBudget)
	for wait := 100 * time.Millisecond; ; wait = min(2*wait, 5*time.Second) {
		err := attempt()
		if !isBusy(err) || time.Now().Add(wait).After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(wait):
		}
	}
}

// UnsnapshottedParts lists, sorted, the parts on which a city's latest
// compute differs from snap (its newest snapshot; nil when it has none): a
// part computed or recomputed since snap was created, or one snap holds that
// the latest compute dropped. Empty means `pvmt snapshots create` would find
// nothing new to freeze.
func UnsnapshottedParts(latest []LatestPart, snap *Snapshot) []string {
	held := map[string]int64{}
	if snap != nil {
		for _, p := range snap.Parts {
			held[p.Part] = p.RunID
		}
	}
	var out []string
	for _, p := range latest {
		if held[p.Part] != p.RunID {
			out = append(out, p.Part)
		}
		delete(held, p.Part)
	}
	if len(latest) > 0 { // an empty latest compute (other settings) has dropped nothing
		out = append(out, slices.Collect(maps.Keys(held))...)
	}
	slices.Sort(out)
	return out
}
