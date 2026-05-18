package snapshots

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// twoCityConfig is the fixture used by every test that exercises the
// multi-city iteration: two cities so we can verify "for each city"
// without ambiguity, and slugs distinct from their display names so
// slug-vs-name confusions show up.
func twoCityConfig() []config.CityConfig {
	return []config.CityConfig{
		{Name: "Alpha"},
		{Name: "Beta"},
	}
}

func resolveCitiesFunc(cities []config.CityConfig) func() ([]config.CityConfig, error) {
	return func() ([]config.CityConfig, error) { return cities, nil }
}

func rootDBFunc(root db.RootStorer) func() (db.RootStorer, error) {
	return func() (db.RootStorer, error) { return root, nil }
}

// TestNewCmdLs_RunFInjection pins the test-injection seam so future
// refactors of the constructor wiring can't quietly break callers'
// ability to substitute a fake run path.
func TestNewCmdLs_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	called := false
	cmd := NewCmdLs(f, func(context.Context, *LsOptions) error {
		called = true
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("runF was not called")
	}
}

// TestRunLs_ListsAcrossCities exercises the happy-path multi-city render.
// Verifies the table shape (one row per snapshot, tagged with the owning
// city slug) and that newest-first ordering from ListSnapshots is
// preserved on the way to the printer.
func TestRunLs_ListsAcrossCities(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	cities := twoCityConfig()

	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(_ context.Context, slug, _ string) (int64, error) {
			if slug == "alpha" {
				return 1, nil
			}
			return 2, nil
		},
		ForCityFunc: func(id int64) db.Store {
			switch id {
			case 1:
				return &dbtest.MockStore{ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) {
					return []db.Snapshot{
						{ID: 7, ComputedAt: now, ConfigHash: "hash-a-2"},
						{ID: 6, ComputedAt: now.Add(-time.Hour), ConfigHash: "hash-a-1"},
					}, nil
				}}
			case 2:
				return &dbtest.MockStore{ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) {
					return []db.Snapshot{{ID: 9, ComputedAt: now, ConfigHash: "hash-b-1"}}, nil
				}}
			}
			return &dbtest.MockStore{}
		},
	}

	ios, _, stdout, _ := iostreams.Test()
	opts := &LsOptions{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
	}
	if err := runLs(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	for _, want := range []string{"alpha", "beta", "7", "6", "9", "hash-a-2", "hash-b-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got: %s", want, out)
		}
	}
}

// TestRunLs_EmptyDatabase pins byob-iostreams.3 routing: when there are
// no snapshots, the hint must land on stderr so scripted consumers see
// an empty stdout.
func TestRunLs_EmptyDatabase(t *testing.T) {
	cities := twoCityConfig()
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string) (int64, error) { return 1, nil },
		ForCityFunc:    func(int64) db.Store { return &dbtest.MockStore{} },
	}
	ios, _, stdout, stderr := iostreams.Test()
	opts := &LsOptions{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
	}
	if err := runLs(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "" {
		t.Errorf("stdout should be empty for empty-state; got: %q", got)
	}
	if !strings.Contains(stderr.String(), "No snapshots") {
		t.Errorf("expected empty-db hint on stderr, got: %s", stderr.String())
	}
}

// TestSnapshotRow_ExportData_AllFieldsPopulated guards the handwritten
// switch in snapshotRow.ExportData against typo regressions, the same
// way cities does it for cityRow.
func TestSnapshotRow_ExportData_AllFieldsPopulated(t *testing.T) {
	r := snapshotRow{City: "alpha", ID: 1, ComputedAt: "2026-05-18T12:00:00Z", ConfigHash: "h"}
	out := r.ExportData(snapshotFields)
	if len(out) != len(snapshotFields) {
		t.Fatalf("want %d keys, got %d: %v", len(snapshotFields), len(out), out)
	}
	for _, f := range snapshotFields {
		if _, ok := out[f]; !ok {
			t.Errorf("missing field %q", f)
		}
	}
}

// TestRunRm_DeletesFromOwningCity verifies the cross-city lookup loop:
// the rm subcommand tries every configured city's DeleteSnapshot, and
// when one returns (true, nil) it stops without trying the rest. This
// is the contract that lets the user pass an id without --city.
func TestRunRm_DeletesFromOwningCity(t *testing.T) {
	cities := twoCityConfig()
	var calls []string
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(_ context.Context, slug, _ string) (int64, error) {
			if slug == "alpha" {
				return 1, nil
			}
			return 2, nil
		},
		ForCityFunc: func(id int64) db.Store {
			return &dbtest.MockStore{DeleteSnapshotFunc: func(_ context.Context, sid int64) (bool, error) {
				calls = append(calls, slugFor(id))
				// Only beta (id=2) owns snapshot 42.
				return id == 2 && sid == 42, nil
			}}
		},
	}
	ios, _, _, stderr := iostreams.Test()
	opts := &RmOptions{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
		SnapshotID:    42,
	}
	if err := runRm(context.Background(), opts); err != nil {
		t.Fatalf("runRm: %v", err)
	}
	// Both cities should have been tried until the owner was found.
	if len(calls) != 2 || calls[0] != "alpha" || calls[1] != "beta" {
		t.Errorf("call order: got %v, want [alpha beta]", calls)
	}
	if !strings.Contains(stderr.String(), "Deleted snapshot 42 from beta") {
		t.Errorf("expected confirmation on stderr, got: %s", stderr.String())
	}
}

// TestRunRm_NotFoundReturnsHint pins the not-found error path so users
// see a remediation hint pointing at `snapshots ls` rather than a bare
// "not found" message.
func TestRunRm_NotFoundReturnsHint(t *testing.T) {
	cities := twoCityConfig()
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string) (int64, error) { return 1, nil },
		ForCityFunc:    func(int64) db.Store { return &dbtest.MockStore{} }, // returns (false, nil)
	}
	ios, _, _, _ := iostreams.Test()
	opts := &RmOptions{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
		SnapshotID:    999,
	}
	err := runRm(context.Background(), opts)
	if err == nil {
		t.Fatal("expected not-found error")
	}
	var hint *cmdutil.ErrHint
	if !errors.As(err, &hint) {
		t.Errorf("expected ErrHint wrapping, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "snapshot 999 not found") {
		t.Errorf("error message should name the id; got: %v", err)
	}
}

// TestRunPrune_KeepsNMostRecentPerCity is the load-bearing test for the
// acceptance criterion: --keep=N retains the N most recent snapshots
// per city. ListSnapshots returns newest-first; everything past index
// N goes to DeleteSnapshot.
func TestRunPrune_KeepsNMostRecentPerCity(t *testing.T) {
	cities := twoCityConfig()

	// Alpha: 4 snapshots. Beta: 2 snapshots.
	alphaSnaps := []db.Snapshot{
		{ID: 4}, {ID: 3}, {ID: 2}, {ID: 1},
	}
	betaSnaps := []db.Snapshot{{ID: 6}, {ID: 5}}

	var deleted []int64
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(_ context.Context, slug, _ string) (int64, error) {
			if slug == "alpha" {
				return 1, nil
			}
			return 2, nil
		},
		ForCityFunc: func(id int64) db.Store {
			snaps := alphaSnaps
			if id == 2 {
				snaps = betaSnaps
			}
			return &dbtest.MockStore{
				ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) { return snaps, nil },
				DeleteSnapshotFunc: func(_ context.Context, sid int64) (bool, error) {
					deleted = append(deleted, sid)
					return true, nil
				},
			}
		},
	}
	ios, _, _, _ := iostreams.Test()
	opts := &PruneOptions{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
		Keep:          2,
	}
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	// Alpha had 4, keep 2: delete IDs 2, 1. Beta had 2, keep 2: nothing.
	want := []int64{2, 1}
	if len(deleted) != len(want) {
		t.Fatalf("deleted = %v; want %v", deleted, want)
	}
	for i := range want {
		if deleted[i] != want[i] {
			t.Errorf("deleted[%d] = %d; want %d", i, deleted[i], want[i])
		}
	}
}

// TestRunPrune_NothingToDo verifies the no-op path when every city is
// already at or below the keep window. The "Nothing to prune." hint on
// stderr makes the no-op visible to humans without writing to stdout.
func TestRunPrune_NothingToDo(t *testing.T) {
	cities := twoCityConfig()
	root := &dbtest.MockRootStore{
		EnsureCityFunc: func(context.Context, string, string) (int64, error) { return 1, nil },
		ForCityFunc: func(int64) db.Store {
			return &dbtest.MockStore{ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) {
				return []db.Snapshot{{ID: 1}}, nil
			}}
		},
	}
	ios, _, stdout, stderr := iostreams.Test()
	opts := &PruneOptions{
		IO:            ios,
		RootDB:        rootDBFunc(root),
		ResolveCities: resolveCitiesFunc(cities),
		Keep:          5,
	}
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "" {
		t.Errorf("stdout should be empty for no-op prune; got: %q", got)
	}
	if !strings.Contains(stderr.String(), "Nothing to prune.") {
		t.Errorf("expected no-op hint on stderr, got: %s", stderr.String())
	}
}

// TestNewCmdPrune_RequiresKeepFlag pins the flag-required contract so
// `pvmt snapshots prune` (no flag) is a usage error, not a silent no-op.
func TestNewCmdPrune_RequiresKeepFlag(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	cmd := NewCmdPrune(f, nil)
	cmd.SetArgs([]string{})
	cmd.SetOut(ios.Out)
	cmd.SetErr(ios.ErrOut)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when --keep is missing")
	}
}

// TestNewCmdRm_RejectsNonPositiveID guards the input validation: a 0 or
// negative id reaches the runtime as a typed FlagError, not a generic
// failure that exits 1. Pairs with byob-errors.4 exit-code routing.
func TestNewCmdRm_RejectsNonPositiveID(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	cmd := NewCmdRm(f, nil)
	cmd.SetArgs([]string{"0"})
	cmd.SetOut(ios.Out)
	cmd.SetErr(ios.ErrOut)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-positive id")
	}
	var fe *cmdutil.FlagError
	if !errors.As(err, &fe) {
		t.Errorf("want *cmdutil.FlagError, got %T: %v", err, err)
	}
}

// slugFor maps the test fixture's city id back to a slug for assertion
// ordering. Kept inline so the test data stays self-describing.
func slugFor(id int64) string {
	if id == 1 {
		return "alpha"
	}
	return "beta"
}
