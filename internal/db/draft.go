package db

import (
	"context"
	"errors"
	"fmt"
	"maps"
)

// Draft is the update one command is assembling for the city's latest
// compute. Every producer the command runs (a resource compute, the combined
// pass, service coverage, census) creates its run through the draft and Puts
// it once the run is complete. No producer reads another's stored results, so
// a draft has no read view: its runs stay invisible until Commit swaps only
// the put parts into the latest compute, in one transaction. Nothing is
// published: `pvmt snapshots create` freezes the latest compute. Discard
// deletes every run the draft created and nothing else.
//
// A Draft is not safe for concurrent use.
type Draft struct {
	store      Store
	configHash string
	parts      map[string]int64  // committed by Commit; 0 removes a part
	created    []int64           // every run created through this draft
	pinned     map[string]string // input -> digest every run records (PinInput)
}

// NewDraft starts a draft over store for configHash, the hash that owns the
// city's data (config.Config.CityHash).
func NewDraft(store Store, configHash string) *Draft {
	return &Draft{store: store, configHash: configHash, parts: map[string]int64{}}
}

// ConfigHash is the hash the draft commits under.
func (d *Draft) ConfigHash() string { return d.configHash }

// NewRun creates a run for producer under the draft's hash, recording inputs,
// the digests the producer read before reading its data (ReadProducerInputs).
// The run stays invisible to every reader until Commit puts it in the latest
// compute.
func (d *Draft) NewRun(ctx context.Context, producer string, inputs map[string]string) (int64, error) {
	if len(d.pinned) > 0 {
		inputs = maps.Clone(inputs)
		for in, digest := range d.pinned {
			if _, reads := inputs[in]; reads {
				inputs[in] = digest
			}
		}
	}
	run, err := d.store.CreateRun(ctx, producer, d.configHash, inputs)
	if err != nil {
		return 0, err
	}
	d.created = append(d.created, run.ID)
	return run.ID, nil
}

// PinInput makes every later run of the draft whose producer reads input
// record digest for it, whatever digest the producer read itself. A command
// that reads an input once and shares what it derived across producers pins
// the digest it read BEFORE reading the input: `all compute` builds one hex
// grid from the boundary for every step, so an ingest that lands after the
// grid was built makes every step look stale rather than falsely current.
func (d *Draft) PinInput(input, digest string) {
	if d.pinned == nil {
		d.pinned = map[string]string{}
	}
	d.pinned[input] = digest
}

// Put makes runID the draft's run for producer (the part it fills).
func (d *Draft) Put(producer string, runID int64) { d.parts[producer] = runID }

// Drop removes producer's part from the latest compute on Commit, e.g.
// when the producer found nothing to work from (no features, no stations), so
// results computed from inputs that are gone stop being published.
func (d *Draft) Drop(producer string) { d.parts[producer] = 0 }

// Parts returns a copy of the parts put (or dropped, as 0) so far.
func (d *Draft) Parts() map[string]int64 { return maps.Clone(d.parts) }

// Commit applies the draft's parts to the city's latest compute
// (Store.CommitLatest). A draft that would not change it (no parts, or only
// drops of parts that are not there) writes nothing. The runs it replaces are
// left for gc.
//
// An error means nothing was committed, so the caller can Discard. Runs that
// were created but never Put are discarded only AFTER a successful commit,
// best-effort: a failure there must not turn a committed update into a
// reported failure, and gc sweeps whatever is left (OrphanRunGrace).
func (d *Draft) Commit(ctx context.Context) error {
	unused := d.unusedRuns()
	if err := d.store.CommitLatest(ctx, d.configHash, d.parts); err != nil {
		return err
	}
	d.created, d.parts = nil, map[string]int64{}
	// Best-effort: an unreferenced run that survives is gc's to sweep.
	_ = d.discard(ctx, unused)
	return nil
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
