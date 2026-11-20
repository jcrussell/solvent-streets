package snapshots

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
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

// TestNewCmdList_RunFInjection pins the test-injection seam so future
// refactors of the constructor wiring can't quietly break callers'
// ability to substitute a fake run path.
func TestNewCmdList_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	called := false
	cmd := NewCmdList(f, func(context.Context, *ListOptions) error {
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

// askStub is a prompt.Stub that records each confirmation question.
type askStub struct {
	prompt.Stub
	Asked []string
}

func (s *askStub) Confirm(ctx context.Context, msg string, def bool) (bool, error) {
	s.Asked = append(s.Asked, msg)
	return s.Stub.Confirm(ctx, msg, def)
}

// tagRoot is a mock root holding two snapshots, newest first: v2 over alpha
// and beta, and v1 over alpha only. deleted records DeleteSnapshot calls.
func tagRoot(deleted *[]string) *dbtest.MockRootStore {
	created := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	city := func(id int64, slug, name string, snap int64) db.SnapshotCity {
		return db.SnapshotCity{ID: snap, Name: name, CityID: id, CitySlug: slug, ConfigHash: "h" + slug, HexEdgeM: 100,
			Parts: []db.SnapshotPart{{Part: "roads", RunID: 10*snap + id, ComputedAt: created}}}
	}
	tags := map[string]*db.Snapshot{
		"v2": {ID: 2, Name: "v2", CreatedAt: created, ComputeEpoch: 3, CityCount: 2,
			Cities: []db.SnapshotCity{city(1, "alpha", "v2", 2), city(2, "beta", "v2", 2)}},
		"v1": {ID: 1, Name: "v1", CreatedAt: created.Add(-time.Hour), ComputeEpoch: 2, CityCount: 1,
			Cities: []db.SnapshotCity{city(1, "alpha", "v1", 1)}},
	}
	return &dbtest.MockRootStore{
		EnsureCityFunc: func(_ context.Context, slug, _, _ string) (int64, error) {
			if slug == "alpha" {
				return 1, nil
			}
			return 2, nil
		},
		ForCityFunc: func(id int64) db.Store {
			return &dbtest.MockStore{ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) {
				var out []db.SnapshotCity
				for _, name := range []string{"v2", "v1"} {
					for _, c := range tags[name].Cities {
						if c.CityID == id {
							out = append(out, c)
						}
					}
				}
				return out, nil
			}}
		},
		ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) {
			var out []db.Snapshot
			for _, name := range []string{"v2", "v1"} {
				if t, ok := tags[name]; ok {
					cp := *t
					cp.Cities = nil
					out = append(out, cp)
				}
			}
			return out, nil
		},
		SnapshotFunc: func(_ context.Context, name string) (*db.Snapshot, error) {
			if t, ok := tags[name]; ok {
				return t, nil
			}
			return nil, db.ErrSnapshotNotFound
		},
		DeleteSnapshotFunc: func(_ context.Context, name string) (bool, error) {
			if _, ok := tags[name]; !ok {
				return false, nil
			}
			if deleted != nil {
				*deleted = append(*deleted, name)
			}
			delete(tags, name)
			return true, nil
		},
	}
}

// TestRunList: one row per snapshot, newest first, with its city count and
// epoch; --city keeps the snapshots covering that city.
func TestRunList(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	opts := &ListOptions{IO: ios, RootDB: rootDBFunc(tagRoot(nil)), ResolveCities: resolveCitiesFunc(twoCityConfig())}
	if err := runList(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "v2\t") || !strings.HasSuffix(lines[0], "\t2\t3") ||
		!strings.HasPrefix(lines[1], "v1\t") || !strings.HasSuffix(lines[1], "\t1\t2") {
		t.Errorf("list output = %q, want v2 (2 cities, epoch 3) then v1 (1, epoch 2)", stdout.String())
	}

	stdout.Reset()
	opts.CityFlagSet = func() bool { return true }
	opts.ResolveCities = resolveCitiesFunc(twoCityConfig()[1:]) // beta
	opts.Config = func() (*config.Config, error) { return &config.Config{ConfigID: "test"}, nil }
	if err := runList(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); !strings.HasPrefix(got, "v2\t") || strings.Contains(got, "v1") {
		t.Errorf("--city beta output = %q, want only v2", got)
	}
}

// TestRunList_EmptyDatabase: a database with no snapshots says how to make
// one on stderr and writes nothing to stdout.
func TestRunList_EmptyDatabase(t *testing.T) {
	ios, _, stdout, stderr := iostreams.Test()
	opts := &ListOptions{IO: ios, RootDB: rootDBFunc(&dbtest.MockRootStore{})}
	if err := runList(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "pvmt snapshots create <name>") {
		t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

// TestList_JSON: --json rows carry the snake_case tag fields.
func TestList_JSON(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	cmd := NewCmdList(&cmdutil.Factory{IOStreams: ios}, func(ctx context.Context, opts *ListOptions) error {
		opts.RootDB = rootDBFunc(tagRoot(nil))
		return runList(ctx, opts)
	})
	cmd.SetArgs([]string{"--json", "name,created_at,city_count,compute_epoch"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if len(rows) != 2 || rows[0]["name"] != "v2" || rows[0]["city_count"] != 2.0 || rows[0]["compute_epoch"] != 3.0 ||
		rows[0]["created_at"] != "2026-05-18T12:00:00Z" {
		t.Errorf("rows = %v", rows)
	}
}

// TestRunShow: one row per city with its hash, edge and manifest; --city
// narrows to one; an unknown name is an error naming `list`.
func TestRunShow(t *testing.T) {
	ios, _, stdout, stderr := iostreams.Test()
	opts := &ShowOptions{IO: ios, RootDB: rootDBFunc(tagRoot(nil)), Name: "v2"}
	if err := runShow(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	want := "alpha\thalpha\t100 m\troads#21\nbeta\thbeta\t100 m\troads#22\n"
	if stdout.String() != want {
		t.Errorf("show output = %q, want %q", stdout.String(), want)
	}
	if !strings.Contains(stderr.String(), "Snapshot v2: created") || !strings.Contains(stderr.String(), "compute epoch 3, 2 cities") {
		t.Errorf("show header = %q", stderr.String())
	}

	stdout.Reset()
	opts.CityFlagSet = func() bool { return true }
	opts.ResolveCities = resolveCitiesFunc(twoCityConfig()[1:])
	if err := runShow(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "beta\thbeta\t100 m\troads#22\n" {
		t.Errorf("--city beta output = %q", got)
	}

	opts.Name = "nope"
	err := runShow(context.Background(), opts)
	var h *hint.ErrHint
	if err == nil || !errors.As(err, &h) || !strings.Contains(h.Hint, "pvmt snapshots list") {
		t.Errorf("unknown name: err = %v, want a hint naming snapshots list", err)
	}
}

// TestShow_JSON: --json rows carry the snapshot name, city and manifest.
func TestShow_JSON(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	cmd := NewCmdShow(&cmdutil.Factory{IOStreams: ios}, func(ctx context.Context, opts *ShowOptions) error {
		opts.RootDB = rootDBFunc(tagRoot(nil))
		return runShow(ctx, opts)
	})
	cmd.SetArgs([]string{"v1", "--json", "snapshot,city,config_hash,hex_edge_m,parts"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Snapshot string    `json:"snapshot"`
		City     string    `json:"city"`
		HexEdgeM float64   `json:"hex_edge_m"`
		Parts    []partRow `json:"parts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if len(rows) != 1 || rows[0].Snapshot != "v1" || rows[0].City != "alpha" || rows[0].HexEdgeM != 100 ||
		len(rows[0].Parts) != 1 || rows[0].Parts[0].RunID != 11 || rows[0].Parts[0].ComputedAt != "2026-05-18T12:00:00Z" {
		t.Errorf("rows = %+v", rows)
	}
}

// TestRunDelete_DeletesByName: --yes deletes the named snapshot and says so.
func TestRunDelete_DeletesByName(t *testing.T) {
	var deleted []string
	ios, _, stdout, _ := iostreams.Test()
	opts := &DeleteOptions{IO: ios, RootDB: rootDBFunc(tagRoot(&deleted)), Name: "v1", Yes: true}
	if err := runDelete(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "v1" || !strings.Contains(stdout.String(), `Deleted snapshot "v1".`) {
		t.Errorf("deleted = %v, stdout = %q", deleted, stdout)
	}
}

// TestRunDelete_DryRunDeletesNothing: --dry-run names the snapshot on stdout
// and neither prompts nor deletes, even on a TTY.
func TestRunDelete_DryRunDeletesNothing(t *testing.T) {
	var deleted []string
	ios, _, stdout, _ := iostreams.Test()
	ios.SetStdinTTY(true)
	opts := &DeleteOptions{IO: ios, Prompter: &prompt.Stub{}, RootDB: rootDBFunc(tagRoot(&deleted)), Name: "v2", DryRun: true}
	if err := runDelete(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 || !strings.Contains(stdout.String(), `Would delete snapshot "v2" (2 cities)`) {
		t.Errorf("deleted = %v, stdout = %q", deleted, stdout)
	}
}

// TestRunDelete_PromptsOnTTY: on a TTY without --yes the prompt names the
// snapshot and its city count, and a yes deletes it.
func TestRunDelete_PromptsOnTTY(t *testing.T) {
	var deleted []string
	ios, _, _, _ := iostreams.Test()
	ios.SetStdinTTY(true)
	stub := &askStub{Confirms: []bool{true}}
	opts := &DeleteOptions{IO: ios, Prompter: stub, RootDB: rootDBFunc(tagRoot(&deleted)), Name: "v2"}
	if err := runDelete(context.Background(), opts); err != nil {
		t.Fatalf("runDelete: %v", err)
	}
	if len(stub.Confirms) != 0 || len(deleted) != 1 {
		t.Errorf("confirms left %v, deleted %v", stub.Confirms, deleted)
	}
	if len(stub.Asked) != 1 || stub.Asked[0] != `Delete snapshot "v2" (2 cities)?` {
		t.Errorf("asked %q", stub.Asked)
	}
}

// TestRunDelete_CancelOnPromptNo: a declined prompt returns ErrCancel and
// deletes nothing.
func TestRunDelete_CancelOnPromptNo(t *testing.T) {
	var deleted []string
	ios, _, _, stderr := iostreams.Test()
	ios.SetStdinTTY(true)
	opts := &DeleteOptions{IO: ios, Prompter: &prompt.Stub{Confirms: []bool{false}}, RootDB: rootDBFunc(tagRoot(&deleted)), Name: "v2"}
	if err := runDelete(context.Background(), opts); !errors.Is(err, cmdutil.ErrCancel) {
		t.Errorf("want ErrCancel, got: %v", err)
	}
	if len(deleted) != 0 || !strings.Contains(stderr.String(), "Canceled.") {
		t.Errorf("deleted %v, stderr %q", deleted, stderr)
	}
}

// TestRunDelete_NoTTYWithoutYesIsFlagError: no TTY and
// no --yes refuses with a FlagError rather than deleting.
func TestRunDelete_NoTTYWithoutYesIsFlagError(t *testing.T) {
	var deleted []string
	ios, _, _, _ := iostreams.Test()
	opts := &DeleteOptions{IO: ios, Prompter: &prompt.Stub{}, RootDB: rootDBFunc(tagRoot(&deleted)), Name: "v2"}
	err := runDelete(context.Background(), opts)
	var fe *cmdutil.FlagError
	if !errors.As(err, &fe) || len(deleted) != 0 {
		t.Errorf("err = %v (deleted %v), want a FlagError and nothing deleted", err, deleted)
	}
}

// TestRunDelete_NotFoundReturnsHint: an unknown name is an error naming
// `snapshots list`.
func TestRunDelete_NotFoundReturnsHint(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &DeleteOptions{IO: ios, RootDB: rootDBFunc(tagRoot(nil)), Name: "v9", Yes: true}
	err := runDelete(context.Background(), opts)
	var h *hint.ErrHint
	if err == nil || !strings.Contains(err.Error(), `snapshot "v9" not found`) || !errors.As(err, &h) {
		t.Errorf("err = %v, want not found with a hint", err)
	}
}

// TestDelete_RejectsCityAndBadNames: --city and an invalid name are flag
// errors before anything runs.
func TestDelete_RejectsCityAndBadNames(t *testing.T) {
	for name, c := range map[string]struct {
		citySet bool
		args    []string
	}{
		"--city":  {true, []string{"v1"}},
		"no name": {false, nil},
		"slash":   {false, []string{"a/b"}},
	} {
		t.Run(name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios, CityFlagSet: func() bool { return c.citySet }}
			ran := false
			cmd := NewCmdDelete(f, func(context.Context, *DeleteOptions) error { ran = true; return nil })
			cmd.SetArgs(c.args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			if err := cmd.Execute(); err == nil || ran {
				t.Errorf("err = %v, ran = %v; want a refusal before running", err, ran)
			}
		})
	}
}

// TestRunPrune_KeepsNMostRecent: prune deletes every snapshot past the N
// newest, oldest included, and keeps the newest.
func TestRunPrune_KeepsNMostRecent(t *testing.T) {
	var deleted []string
	ios, _, stdout, _ := iostreams.Test()
	opts := &PruneOptions{IO: ios, RootDB: rootDBFunc(tagRoot(&deleted)), Keep: 1, Yes: true}
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "v1" || !strings.Contains(stdout.String(), "Pruned 1 snapshot (kept 1).") {
		t.Errorf("deleted %v, stdout %q; want v1 pruned", deleted, stdout)
	}
}

// TestRunPrune_DryRunDeletesNothing: --dry-run lists what would go on stdout
// and neither prompts nor deletes.
func TestRunPrune_DryRunDeletesNothing(t *testing.T) {
	var deleted []string
	ios, _, stdout, _ := iostreams.Test()
	ios.SetStdinTTY(true)
	opts := &PruneOptions{IO: ios, Prompter: &prompt.Stub{}, RootDB: rootDBFunc(tagRoot(&deleted)), Keep: 1, DryRun: true}
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 || !strings.Contains(stdout.String(), "Would delete 1 snapshot, keeping the 1 newest:\n  v1\n") {
		t.Errorf("deleted %v, stdout %q", deleted, stdout)
	}
}

// TestRunPrune_NothingToDo: keeping at least as many as exist deletes nothing.
func TestRunPrune_NothingToDo(t *testing.T) {
	var deleted []string
	ios, _, stdout, _ := iostreams.Test()
	opts := &PruneOptions{IO: ios, RootDB: rootDBFunc(tagRoot(&deleted)), Keep: 2, Yes: true}
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 || !strings.Contains(stdout.String(), "Nothing to prune.") {
		t.Errorf("deleted %v, stdout %q", deleted, stdout)
	}
}

// TestRunPrune_CancelOnPromptNo: a declined prompt deletes nothing.
func TestRunPrune_CancelOnPromptNo(t *testing.T) {
	var deleted []string
	ios, _, _, _ := iostreams.Test()
	ios.SetStdinTTY(true)
	stub := &askStub{Confirms: []bool{false}}
	opts := &PruneOptions{IO: ios, Prompter: stub, RootDB: rootDBFunc(tagRoot(&deleted)), Keep: 1}
	if err := runPrune(context.Background(), opts); !errors.Is(err, cmdutil.ErrCancel) {
		t.Errorf("want ErrCancel, got %v", err)
	}
	if len(deleted) != 0 || len(stub.Asked) != 1 || stub.Asked[0] != "Delete 1 snapshot, keeping the 1 newest?" {
		t.Errorf("deleted %v, asked %q", deleted, stub.Asked)
	}
}

// TestRunPrune_NoTTYWithoutYesIsFlagError: no TTY and no --yes refuses.
func TestRunPrune_NoTTYWithoutYesIsFlagError(t *testing.T) {
	var deleted []string
	ios, _, _, _ := iostreams.Test()
	opts := &PruneOptions{IO: ios, Prompter: &prompt.Stub{}, RootDB: rootDBFunc(tagRoot(&deleted)), Keep: 1}
	var fe *cmdutil.FlagError
	if err := runPrune(context.Background(), opts); !errors.As(err, &fe) || len(deleted) != 0 {
		t.Errorf("err = %v (deleted %v), want a FlagError", err, deleted)
	}
}

// TestNewCmdPrune_RequiresKeepFlag: --keep is required, and --city refused.
func TestNewCmdPrune_RequiresKeepFlag(t *testing.T) {
	for name, c := range map[string]struct {
		citySet bool
		args    []string
	}{
		"no --keep": {false, nil},
		"--keep=0":  {false, []string{"--keep=0"}},
		"--city":    {true, []string{"--keep=1"}},
	} {
		t.Run(name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios, CityFlagSet: func() bool { return c.citySet }}
			ran := false
			cmd := NewCmdPrune(f, func(context.Context, *PruneOptions) error { ran = true; return nil })
			cmd.SetArgs(c.args)
			cmd.SetOut(ios.Out)
			cmd.SetErr(ios.ErrOut)
			if err := cmd.Execute(); err == nil || ran {
				t.Errorf("err = %v, ran = %v; want a refusal", err, ran)
			}
		})
	}
}

// TestDeleteAndPrune_JSON: --json reports each snapshot that went (or, on a
// dry run, would go) as a snapshots list row with deleted.
func TestDeleteAndPrune_JSON(t *testing.T) {
	run := func(cmd func(*cmdutil.Factory, *[]string) *cobra.Command, args ...string) ([]map[string]any, []string) {
		t.Helper()
		var deleted []string
		ios, _, stdout, _ := iostreams.Test()
		c := cmd(&cmdutil.Factory{IOStreams: ios}, &deleted)
		c.SetArgs(args)
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
			t.Fatalf("%v: %s", err, stdout)
		}
		return rows, deleted
	}
	del := func(f *cmdutil.Factory, deleted *[]string) *cobra.Command {
		return NewCmdDelete(f, func(ctx context.Context, opts *DeleteOptions) error {
			opts.RootDB = rootDBFunc(tagRoot(deleted))
			return runDelete(ctx, opts)
		})
	}
	prune := func(f *cmdutil.Factory, deleted *[]string) *cobra.Command {
		return NewCmdPrune(f, func(ctx context.Context, opts *PruneOptions) error {
			opts.RootDB = rootDBFunc(tagRoot(deleted))
			return runPrune(ctx, opts)
		})
	}
	for _, tc := range []struct {
		name    string
		cmd     func(*cmdutil.Factory, *[]string) *cobra.Command
		args    []string
		deleted bool
	}{
		{"delete", del, []string{"v1", "--yes"}, true},
		{"delete --dry-run", del, []string{"v1", "--dry-run"}, false},
		{"prune", prune, []string{"--keep=1", "--yes"}, true},
		{"prune --dry-run", prune, []string{"--keep=1", "--dry-run"}, false},
	} {
		rows, deleted := run(tc.cmd, append(tc.args, "--json", "name,city_count,deleted")...)
		if len(rows) != 1 || rows[0]["name"] != "v1" || rows[0]["city_count"] != 1.0 || rows[0]["deleted"] != tc.deleted {
			t.Errorf("%s: rows = %v", tc.name, rows)
		}
		if (len(deleted) == 1) != tc.deleted {
			t.Errorf("%s: deleted %v", tc.name, deleted)
		}
	}
	if rows, _ := run(prune, "--keep=5", "--yes", "--json", "name"); len(rows) != 0 {
		t.Errorf("prune with nothing to do: rows = %v, want []", rows)
	}
}

// TestPrune_JSONFailsPartway: a prune whose delete fails partway still
// writes the --json rows of the snapshots it already deleted, then fails.
func TestPrune_JSONFailsPartway(t *testing.T) {
	boom := errors.New("disk full")
	root := &dbtest.MockRootStore{
		ListSnapshotsFunc: func(context.Context) ([]db.Snapshot, error) {
			return []db.Snapshot{{Name: "v3"}, {Name: "v2"}, {Name: "v1"}}, nil
		},
		DeleteSnapshotFunc: func(_ context.Context, name string) (bool, error) {
			if name == "v1" {
				return false, boom
			}
			return true, nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	cmd := NewCmdPrune(&cmdutil.Factory{IOStreams: ios}, func(ctx context.Context, opts *PruneOptions) error {
		opts.RootDB = rootDBFunc(root)
		return runPrune(ctx, opts)
	})
	cmd.SetArgs([]string{"--keep=1", "--yes", "--json", "name,deleted"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the delete failure", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, stdout)
	}
	if len(rows) != 1 || rows[0]["name"] != "v2" || rows[0]["deleted"] != true {
		t.Errorf("rows = %v, want v2 deleted", rows)
	}
}
