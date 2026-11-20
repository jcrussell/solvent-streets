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

// ErrNothingComputed refuses a city in CreateSnapshot when it has no latest
// compute under the config hash: no city of a snapshot is ever empty.
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
// error refuses the city, and so the snapshot; CreateSnapshot returns it
// unchanged in its *SnapshotRefusedError.
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

// freezeCity is one city of CreateSnapshot inside its transaction: it reads
// the city's latest compute and input states, runs the check, and on success
// records the city and its manifest in snapshot snapshotID. A refusal (the
// check's error, or ErrNothingComputed) is returned as refusal with a nil
// err; err is a failure to read or write.
func freezeCity(ctx context.Context, tx *sql.Tx, snapshotID int64, req SnapshotRequest) (refusal, err error) {
	parts, err := latestCompute(ctx, tx, req.CityID, req.ConfigHash)
	if err != nil {
		return nil, err
	}
	states, err := inputStates(ctx, tx, req.CityID)
	if err != nil {
		return nil, err
	}
	// Nothing computed is reported as such before check runs: the check
	// refuses an empty compute too, but would describe each ingested
	// resource as never computed, hiding that the city was computed under
	// other settings.
	if len(parts) == 0 {
		return ErrNothingComputed, nil
	}
	if req.Check != nil {
		if err := req.Check(parts, states); err != nil {
			return err, nil
		}
	}
	manifest := make(map[string]int64, len(parts))
	for _, p := range parts {
		manifest[p.Part] = p.RunID
	}
	return nil, insertSnapshotCity(ctx, tx, snapshotID, req, manifest)
}

// retryBusy runs attempt until it does not fail with isBusy, for up to
// commitBusyBudget, backing off between tries.
func retryBusy(ctx context.Context, attempt func() error) error {
	return retryBusyWithin(ctx, commitBusyBudget, attempt)
}

// retryBusyWithin is retryBusy for up to budget.
func retryBusyWithin(ctx context.Context, budget time.Duration, attempt func() error) error {
	deadline := time.Now().Add(budget)
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
// compute differs from snap (the city in its newest snapshot; nil when it has
// none): a part computed or recomputed since snap was created, or one snap
// holds that the latest compute dropped. Empty means a new snapshot would
// freeze the same runs for the city.
func UnsnapshottedParts(latest []LatestPart, snap *SnapshotCity) []string {
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
