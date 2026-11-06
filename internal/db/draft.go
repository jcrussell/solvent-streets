package db

import (
	"context"
	"errors"
	"fmt"
	"maps"
)

// Draft is the snapshot one command is assembling. Every producer the command
// runs (a resource compute, the forecast that follows it, service coverage,
// census) creates its run through the draft and Puts it once the run is
// complete. Reads through Store see those runs on top of the newest snapshot
// for the config hash, so a later producer in the same command reads an
// earlier one's output. Commit writes ONE snapshot with only the put parts
// swapped in; Discard deletes every run the draft created and nothing else.
//
// A Draft is not safe for concurrent use.
type Draft struct {
	store      Store
	configHash string
	parts      map[string]int64 // committed by Commit; 0 removes a part
	created    []int64          // every run created through this draft
}

// NewDraft starts a draft over store for configHash, the hash that owns the
// city's data (config.Config.CityHash).
func NewDraft(store Store, configHash string) *Draft {
	return &Draft{store: store, configHash: configHash, parts: map[string]int64{}}
}

// ConfigHash is the hash the draft commits under.
func (d *Draft) ConfigHash() string { return d.configHash }

// Store returns a view reading the newest snapshot for the draft's hash with
// the parts put so far overlaid. Call it again after Put to see new parts.
func (d *Draft) Store() Store { return d.store.WithParts(d.configHash, d.parts) }

// NewRun creates a run for producer under the draft's hash. The run stays
// invisible, even through Store, until it is Put.
func (d *Draft) NewRun(ctx context.Context, producer string) (int64, error) {
	run, err := d.store.CreateRun(ctx, producer, d.configHash)
	if err != nil {
		return 0, err
	}
	d.created = append(d.created, run.ID)
	return run.ID, nil
}

// Put makes runID the draft's run for producer (the part it fills).
func (d *Draft) Put(producer string, runID int64) { d.parts[producer] = runID }

// Drop removes producer's part from the snapshot the draft will commit, e.g.
// a dependent part whose rerun failed and must not be carried forward stale.
func (d *Draft) Drop(producer string) { d.parts[producer] = 0 }

// Parts returns a copy of the parts put (or dropped, as 0) so far.
func (d *Draft) Parts() map[string]int64 { return maps.Clone(d.parts) }

// Commit writes the draft as the city's next snapshot and returns it. When
// the draft would not change the newest snapshot for its hash (no parts, or
// only drops of parts that are not there) nothing is written, and Commit
// returns that snapshot, or nil when the hash has none.
//
// An error means nothing was committed, so the caller can Discard. Runs that
// were created but never Put are discarded only AFTER a successful commit,
// best-effort: a failure there must not turn a committed snapshot into a
// reported failure, and gc sweeps whatever is left (OrphanRunGrace).
func (d *Draft) Commit(ctx context.Context) (*Snapshot, error) {
	unused := d.unusedRuns()
	snap, err := d.store.CommitSnapshot(ctx, d.configHash, d.parts)
	if err != nil {
		return nil, err
	}
	d.created, d.parts = nil, map[string]int64{}
	// Best-effort: an unreferenced run that survives is gc's to sweep.
	_ = d.discard(ctx, unused)
	return snap, nil
}

// Discard deletes every run the draft created; nothing is committed. Callers
// on a cancelled context should pass context.WithoutCancel(ctx).
func (d *Draft) Discard(ctx context.Context) error {
	err := d.discard(ctx, d.created)
	d.created, d.parts = nil, map[string]int64{}
	return err
}

func (d *Draft) unusedRuns() []int64 {
	used := map[int64]bool{}
	for _, id := range d.parts {
		used[id] = true
	}
	var out []int64
	for _, id := range d.created {
		if !used[id] {
			out = append(out, id)
		}
	}
	return out
}

func (d *Draft) discard(ctx context.Context, runs []int64) error {
	var errs []error
	for _, id := range runs {
		if err := d.store.DiscardRun(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("discard run %d: %w", id, err))
		}
	}
	return errors.Join(errs...)
}
