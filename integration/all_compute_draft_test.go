package integration

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/pkg/cmd/all"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// draftProbeStore is a real store that counts the latest-compute commits and
// run discards an `all compute` makes, and can fail or cancel when a given
// producer creates its run.
type draftProbeStore struct {
	db.Store
	failProducer string             // CreateRun for it returns errInjected
	cancelOn     string             // CreateRun for it cancels the run's context
	cancel       context.CancelFunc // what cancelOn calls
	commits      int
	created      []int64
	discarded    []int64
}

var errInjected = errors.New("injected step failure")

func (s *draftProbeStore) CreateRun(ctx context.Context, producer, configHash string, inputs map[string]string) (*db.Run, error) {
	switch producer {
	case s.failProducer:
		return nil, errInjected
	case s.cancelOn:
		s.cancel()
		return nil, ctx.Err()
	}
	run, err := s.Store.CreateRun(ctx, producer, configHash, inputs)
	if err == nil {
		s.created = append(s.created, run.ID)
	}
	return run, err
}

func (s *draftProbeStore) CommitLatest(ctx context.Context, configHash string, computeEpoch int, parts map[string]int64) error {
	s.commits++
	return s.Store.CommitLatest(ctx, configHash, computeEpoch, parts)
}

func (s *draftProbeStore) DiscardRun(ctx context.Context, runID int64) error {
	s.discarded = append(s.discarded, runID)
	return s.Store.DiscardRun(ctx, runID)
}

// computeAllOver runs `pvmt all compute` over store on ctx.
func computeAllOver(ctx context.Context, store db.Store) (string, error) {
	ios, _, _, errOut := iostreams.Test()
	cmd := all.NewCmdAll(allComputeFactory(ios, store))
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"compute"})
	err := cmd.ExecuteContext(ctx)
	return errOut.String(), err
}

// latestRuns is the city's latest compute under the fixture config: part ->
// run id.
func latestRuns(t *testing.T, ctx context.Context, store db.Store) map[string]int64 {
	t.Helper()
	cfg := allFixtureConfig()
	parts, err := store.LatestCompute(ctx, cfg.CityHash(&cfg.Cities[0]))
	if err != nil {
		t.Fatalf("LatestCompute: %v", err)
	}
	out := make(map[string]int64, len(parts))
	for _, p := range parts {
		out[p.Part] = p.RunID
	}
	return out
}

// computedFixture runs one clean `all compute` and returns the store and the
// latest compute it committed.
func computedFixture(t *testing.T, ctx context.Context) (db.Store, map[string]int64) {
	t.Helper()
	store := newAllFixtureStore(t, ctx)
	if stderr, err := computeAllOver(ctx, store); err != nil {
		t.Fatalf("first all compute: %v\n%s", err, stderr)
	}
	before := latestRuns(t, ctx, store)
	for _, part := range []string{"roads", "parking", "sidewalks"} {
		if before[part] == 0 {
			t.Fatalf("first all compute committed no %s part: %v", part, before)
		}
	}
	return store, before
}

// TestAllCompute_FailedStepKeepsItsPart: a step that fails leaves its part
// of the latest compute as the previous compute left it, while every step
// that succeeded swaps in its new run, all in one commit.
func TestAllCompute_FailedStepKeepsItsPart(t *testing.T) {
	ctx := context.Background()
	base, before := computedFixture(t, ctx)

	probe := &draftProbeStore{Store: base, failProducer: "parking"}
	stderr, err := computeAllOver(ctx, probe)
	if !errors.Is(err, errInjected) {
		t.Fatalf("all compute with a failing parking step = %v, want the injected failure\n%s", err, stderr)
	}
	if probe.commits != 1 {
		t.Errorf("all compute committed %d times, want once for the city", probe.commits)
	}
	after := latestRuns(t, ctx, base)
	if after["parking"] != before["parking"] {
		t.Errorf("failed parking step changed its part: run %d, want the previous %d", after["parking"], before["parking"])
	}
	for _, part := range []string{"roads", "sidewalks"} {
		if after[part] == before[part] {
			t.Errorf("%s part still holds run %d; a succeeding step must swap in its new run", part, after[part])
		}
	}
}

// TestAllCompute_CancelCommitsNothing: a compute canceled part-way commits
// nothing, so the latest compute is the previous one, and the runs the
// canceled steps created are discarded.
func TestAllCompute_CancelCommitsNothing(t *testing.T) {
	ctx := context.Background()
	base, before := computedFixture(t, ctx)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	probe := &draftProbeStore{Store: base, cancelOn: "sidewalks", cancel: cancel}
	stderr, err := computeAllOver(runCtx, probe)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled all compute = %v, want context.Canceled\n%s", err, stderr)
	}
	if probe.commits != 0 {
		t.Errorf("canceled all compute committed %d times, want none", probe.commits)
	}
	if len(probe.created) == 0 {
		t.Fatal("no step created a run before the cancel; the test cancels too early to mean anything")
	}
	for _, id := range probe.created {
		if !slices.Contains(probe.discarded, id) {
			t.Errorf("run %d created before the cancel was not discarded (discarded %v)", id, probe.discarded)
		}
	}
	if after := latestRuns(t, ctx, base); !maps.Equal(after, before) {
		t.Errorf("latest compute after a canceled run = %v, want unchanged %v", after, before)
	}
}
