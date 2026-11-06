package compute

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// WithDraft runs fn against draft, or, when draft is nil, against a fresh
// draft over store for configHash that it owns: committed as ONE snapshot when
// fn succeeds (or reports cmdutil.ErrNoResults, see below), discarded when fn
// fails otherwise or commit fails. A caller-supplied
// draft (`pvmt all compute`, which commits once per city after every step) is
// neither committed nor discarded here.
//
// Discarding runs on a context that ignores cancellation, so a Ctrl-C'd
// command still cleans up the runs it created.
func WithDraft(ctx context.Context, draft *db.Draft, store db.Store, configHash string, fn func(*db.Draft) error) error {
	if draft != nil {
		return fn(draft)
	}
	draft = db.NewDraft(store, configHash)
	err := fn(draft)
	// An empty result (ErrNoResults) still commits whatever the step dropped:
	// a producer that finds nothing to work from (no facilities, census
	// switched off) removes its part instead of leaving the old result on
	// the map. Any other error commits nothing.
	if err != nil && !errors.Is(err, cmdutil.ErrNoResults) {
		return errors.Join(err, draft.Discard(context.WithoutCancel(ctx)))
	}
	if _, cerr := draft.Commit(ctx); cerr != nil {
		return CommitFailed(err, cerr, draft.Discard(context.WithoutCancel(ctx)))
	}
	return err
}

// CommitFailed builds the error for a draft whose commit failed after its
// steps returned stepsErr. ErrNoResults is deliberately NOT carried over: it
// is matched ahead of every other error (ForEachCity files the city as empty,
// the runner exits 3 without printing), so joining it would hide that the
// commit failed and the old result is still published.
func CommitFailed(stepsErr, commitErr, discardErr error) error {
	return errors.Join(withoutNoResults(stepsErr), fmt.Errorf("commit snapshot: %w", commitErr), discardErr)
}

// withoutNoResults strips ErrNoResults out of err, keeping every other error
// of a join (one step's real failure must survive another step's empty
// result).
func withoutNoResults(err error) error {
	if err == nil || !errors.Is(err, cmdutil.ErrNoResults) {
		return err
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var keep []error
		for _, e := range j.Unwrap() {
			if e = withoutNoResults(e); e != nil {
				keep = append(keep, e)
			}
		}
		return errors.Join(keep...)
	}
	return nil
}
