package compute

import (
	"context"
	"errors"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// TestWithDraft_FailedCommitIsNotNoResults: a step that found nothing to do
// (ErrNoResults) but whose commit then failed must report the failure.
// ErrNoResults is matched first everywhere (silent exit 3, "empty city" in a
// multi-city run), so carrying it would hide that the commit failed and the
// old result is still published.
func TestWithDraft_FailedCommitIsNotNoResults(t *testing.T) {
	boom := errors.New("database is locked")
	store := &dbtest.MockStore{
		CommitSnapshotFunc: func(context.Context, string, map[string]int64) (*db.Snapshot, error) { return nil, boom },
	}
	err := WithDraft(context.Background(), nil, store, "h", func(d *db.Draft) error {
		d.Drop("service:fire")
		return cmdutil.ErrNoResults
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the commit failure", err)
	}
	if errors.Is(err, cmdutil.ErrNoResults) {
		t.Errorf("err = %v still matches ErrNoResults, which would hide the failed commit", err)
	}
}

// TestCommitFailed_KeepsRealStepErrors: stripping ErrNoResults keeps the other
// errors of a join.
func TestCommitFailed_KeepsRealStepErrors(t *testing.T) {
	step := errors.New("parking: locked")
	err := CommitFailed(errors.Join(cmdutil.ErrNoResults, step), errors.New("commit"), nil)
	if !errors.Is(err, step) || errors.Is(err, cmdutil.ErrNoResults) {
		t.Errorf("err = %v, want the parking failure kept and ErrNoResults gone", err)
	}
}
