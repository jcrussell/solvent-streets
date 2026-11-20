package all

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/cache"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/tui"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmd/cmdtest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestNewCmdAll_RegistersSubcommands pins the thin-wrapper contract:
// the factory returns a cobra.Command exposing the cross-resource
// ingest+compute fan-out. status is intentionally absent — `pvmt status`
// already covers every resource without a per-resource flag, so wiring
// `pvmt all status` would just duplicate it.
// TestForEachResource_ErrAllSourcesFailedContinues pins finding 0yfp: a
// total-source-failure for ONE resource must warn and continue the fan-out
// (so parking/sidewalks still run), not abort the whole pass with an early
// return. It must still be REPORTED at the end, though — swallowing it made
// `pvmt all ingest` exit 0 with nothing ingested. ErrNoResults is skipped
// entirely; only context.Canceled/DeadlineExceeded aborts mid-fan-out.
func TestForEachResource_ErrAllSourcesFailedContinues(t *testing.T) {
	t.Run("ErrAllSourcesFailed warns, continues, and is returned", func(t *testing.T) {
		ios, _, _, errBuf := iostreams.Test()
		var visited []resource.Type
		err := forEachResource(ios, func(rt resource.Source) error {
			visited = append(visited, rt.Type())
			if rt.Type() == resource.All[0].Type() {
				return cmdutil.ErrAllSourcesFailed
			}
			return nil
		})
		if !errors.Is(err, cmdutil.ErrAllSourcesFailed) {
			t.Fatalf("forEachResource = %v; want the failure joined into the result", err)
		}
		if len(visited) != len(resource.All) {
			t.Errorf("visited %d resources; want all %d (first failure must not abort)", len(visited), len(resource.All))
		}
		if !strings.Contains(errBuf.String(), "failed") {
			t.Errorf("expected a warning on ErrOut, got %q", errBuf.String())
		}
	})

	t.Run("all resources clean returns nil", func(t *testing.T) {
		ios, _, _, _ := iostreams.Test()
		if err := forEachResource(ios, func(resource.Source) error { return nil }); err != nil {
			t.Fatalf("forEachResource = %v; want nil when every resource succeeds", err)
		}
	})

	t.Run("every resource empty returns ErrNoResults", func(t *testing.T) {
		ios, _, _, _ := iostreams.Test()
		err := forEachResource(ios, func(resource.Source) error { return cmdutil.ErrNoResults })
		if !errors.Is(err, cmdutil.ErrNoResults) {
			t.Fatalf("forEachResource = %v; want ErrNoResults (nothing was produced)", err)
		}
	})

	t.Run("context.Canceled aborts", func(t *testing.T) {
		ios, _, _, _ := iostreams.Test()
		var visited int
		err := forEachResource(ios, func(_ resource.Source) error {
			visited++
			return context.Canceled
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
		if visited != 1 {
			t.Errorf("cancellation must abort after the first resource; visited %d", visited)
		}
	})
}

// ingestTestBoundary is a valid polygon so the ingest path can derive a bbox
// from the cached boundary without any network access.
const ingestTestBoundary = `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`

// reqCtxKey marks the context handed to ExecuteContext so the test can assert
// it reaches the ingest run — the property the retired execSub helper existed
// to preserve (ExecuteContext, not Execute).
type reqCtxKey struct{}

// ingestFactory builds a factory whose city has no configured sources
// (overpass off, no arcgis_url), so each per-resource ingest resolves an empty
// source list, fetches nothing, and returns ErrNoResults — the fan-out
// behavior under test, with no network involved.
// roundTripFunc adapts a function to http.RoundTripper for offline tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func ingestFactory(ios *iostreams.IOStreams, store db.Store) *cmdutil.Factory {
	city := cmdtest.NewTestCity()
	cfg := cmdtest.NewTestConfig(city)
	return &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		Config:      func() (*config.Config, error) { return cfg, nil },
		// Offline: every request answers an empty Overpass result, so the
		// facility step (which always uses Overpass) reaches ErrNoResults
		// without touching the network.
		HttpClient: func() (*http.Client, error) {
			return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"elements":[]}`))}, nil
			})}, nil
		},
		UnitSystem: func() units.System { return units.Imperial },
	}
}

// TestAllIngest_InProcessFanOut pins the behavior `all ingest` used to get from
// building a throwaway ingest cobra command per resource and running it via
// ExecuteContext: every resource is visited and the request context reaches
// each run.
//
// The load-bearing assertion is the empty stderr warning check. Every ingest
// failure mode here lands on the same ErrNoResults from the command —
// forEachResource propagates it when NO resource produced anything, and
// warn-continues everything else — so only the absence of a
// "<resource> failed:" warning (cmdutil.Warnf) distinguishes "each resource
// reached the no-sources ErrNoResults exit" from "each resource blew up early
// and was swallowed". That is what catches, e.g., an Options literal that
// leaves Source at its "" zero value instead of cmdutil.SourceAll: GetBoundary
// still runs (it precedes resolveSources), so the call counts alone would not
// notice.
func TestAllIngest_InProcessFanOut(t *testing.T) {
	ios, _, _, errBuf := iostreams.Test()
	boundaryCalls, ctxSeen := 0, 0
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(ctx context.Context) (string, error) {
			boundaryCalls++
			if ctx.Value(reqCtxKey{}) != nil {
				ctxSeen++
			}
			return ingestTestBoundary, nil
		},
		// Tripwire, not coverage: the no-sources fixture returns ErrNoResults
		// upstream of any write, so this is unreachable unless that ordering
		// changes.
		UpsertFeaturesFunc: func(_ context.Context, _ resource.Type, _ []db.Feature, _ []string) error {
			t.Error("UpsertFeatures called; an empty fetch must return before touching the DB")
			return nil
		},
	}

	cmd := NewCmdAll(ingestFactory(ios, store))
	cmd.SetArgs([]string{"ingest"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	ctx := context.WithValue(context.Background(), reqCtxKey{}, "req")
	// Exit 3, not 0: every resource came back empty, so the city ingested
	// nothing and a chained `ingest && compute && export` must not proceed.
	// Same rule ForEachCity applies across cities, and same as what a bare
	// `pvmt roads ingest` returns for this fixture.
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("all ingest = %v, want ErrNoResults (every resource was empty)", err)
	}
	if strings.Contains(errBuf.String(), "failed:") {
		t.Errorf("a resource failed and was warn-swallowed; want every resource to reach ErrNoResults cleanly.\nstderr:\n%s", errBuf.String())
	}
	// The up-front boundary resolve, every pavement resource, and the
	// best-effort fire-facility (with its routing roads, a companion) and
	// Census block ingests that ride along after them (runOptionalStep).
	wantRuns := 1 + len(resource.All) + 3
	if boundaryCalls != wantRuns {
		t.Errorf("ingested %d resources, want %d — the fan-out must visit every resource and the optional steps", boundaryCalls, wantRuns)
	}
	if ctxSeen != wantRuns {
		t.Errorf("request context reached %d of %d ingest runs; a cancellable ctx must propagate to each", ctxSeen, wantRuns)
	}
}

// TestAllIngest_ForceBypassesTheCache pins `all ingest --force`:
// behind a real CachingTransport a repeat run is served
// entirely from the cache, while --force sends even cached requests upstream
// and re-fetches the boundary the store already holds instead of reusing it.
func TestAllIngest_ForceBypassesTheCache(t *testing.T) {
	upstream := 0
	inner := roundTripFunc(func(*http.Request) (*http.Response, error) {
		upstream++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"elements":[]}`))}, nil
	})
	client := &http.Client{Transport: cache.NewTransport(inner, t.TempDir(), cache.DefaultTTL)}
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { return ingestTestBoundary, nil },
	}
	run := func(args ...string) string {
		t.Helper()
		ios, _, _, errBuf := iostreams.Test()
		f := ingestFactory(ios, store)
		f.HttpClient = func() (*http.Client, error) { return client, nil }
		cmd := NewCmdAll(f)
		cmd.SetArgs(append([]string{"ingest"}, args...))
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		_ = cmd.ExecuteContext(context.Background())
		return errBuf.String()
	}

	run()
	if upstream == 0 {
		t.Fatal("first run made no upstream request; the fixture no longer exercises the cache")
	}
	before := upstream
	run()
	if upstream != before {
		t.Errorf("repeat run made %d upstream requests; want 0 (every response is cached)", upstream-before)
	}
	// The first --force run re-fetches the boundary, a request no earlier run
	// made, so it reaches upstream either way. The second repeats exactly the
	// first's requests, which are now cached: only a real bypass sends them
	// upstream again.
	run("--force")
	before = upstream
	stderr := run("--force")
	if upstream == before {
		t.Error("a repeated --force run was served from the cache; --force must bypass it")
	}
	if strings.Contains(stderr, "Using cached boundary") {
		t.Errorf("--force reused the stored boundary; want it re-fetched.\nstderr:\n%s", stderr)
	}
}

// TestAllIngest_BoundaryFailureSkipsCity: every
// ingest step needs the city boundary, so when it cannot be resolved the city
// is skipped after one attempt and one warning. Each step used to resolve it
// on its own, so a failing Overpass re-issued the boundary and water-strip
// queries for every step of every city, with a warning each time.
func TestAllIngest_BoundaryFailureSkipsCity(t *testing.T) {
	ios, _, _, errBuf := iostreams.Test()
	wantErr := errors.New("database is locked")
	boundaryCalls := 0
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) {
			boundaryCalls++
			return "", wantErr
		},
		UpsertFeaturesFunc: func(context.Context, resource.Type, []db.Feature, []string) error {
			t.Error("UpsertFeatures called; no step may run without a boundary")
			return nil
		},
	}

	cmd := NewCmdAll(ingestFactory(ios, store))
	cmd.SetArgs([]string{"ingest"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.ExecuteContext(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("all ingest = %v, want the boundary failure", err)
	}
	if boundaryCalls != 1 {
		t.Errorf("boundary resolved %d times, want 1", boundaryCalls)
	}
	stderr := errBuf.String()
	if n := strings.Count(stderr, "warning:"); n != 1 {
		t.Errorf("printed %d warnings, want 1:\n%s", n, stderr)
	}
	if strings.Contains(stderr, "--- ") {
		t.Errorf("an ingest step ran after the boundary failed:\n%s", stderr)
	}
}

// TestAllIngest_CanceledContextAborts: a canceled request context must stop
// the fan-out before any per-resource ingest work happens.
func TestAllIngest_CanceledContextAborts(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	boundaryCalls := 0
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			boundaryCalls++
			return ingestTestBoundary, nil
		},
	}

	cmd := NewCmdAll(ingestFactory(ios, store))
	cmd.SetArgs([]string{"ingest"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("all ingest = %v, want context.Canceled", err)
	}
	if boundaryCalls != 0 {
		t.Errorf("ingest ran %d times after cancellation; want 0", boundaryCalls)
	}
}

// TestAllIngest_MidFanOutCancelAborts covers the gap
// TestAllIngest_CanceledContextAborts does not: there the context is already
// dead at Execute, so cmdutil.ForEachCity's pre-check stops everything before
// the first resource. Here the fan-out has already started and is canceled
// between resources.
//
// dbtest.MockStore ignores ctx entirely, which is the whole point — real sqlite
// surfaces context.Canceled on its own, so without an explicit ctx.Err() check
// in ingest.RunResourceForCity this fixture would happily run every remaining
// resource after cancellation.
func TestAllIngest_MidFanOutCancelAborts(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	boundaryCalls := 0
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			boundaryCalls++
			cancel() // cancel partway through the first resource
			return ingestTestBoundary, nil
		},
	}

	cmd := NewCmdAll(ingestFactory(ios, store))
	cmd.SetArgs([]string{"ingest"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("all ingest = %v, want context.Canceled", err)
	}
	if boundaryCalls != 1 {
		t.Errorf("ingest ran for %d resources, want 1 — the fan-out must stop at the first canceled resource", boundaryCalls)
	}
}

// TestAllIngest_MidFanOutDeadlineAborts is the DeadlineExceeded twin of the
// above. forEachResource used to abort on context.Canceled ONLY, so an expired
// deadline fell through to warn-and-continue: every remaining resource failed
// identically and the run still exited 0.
//
// The deadline has to expire mid-fan-out to exercise that check. An
// already-expired one is caught by cmdutil.ForEachCity's pre-check before the
// first resource ever runs, so it never reaches forEachResource at all — which
// is why this sleeps past a short deadline inside the first resource instead.
func TestAllIngest_MidFanOutDeadlineAborts(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	t.Cleanup(cancel)

	boundaryCalls := 0
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			boundaryCalls++
			if boundaryCalls == 1 {
				time.Sleep(50 * time.Millisecond) // outlive the deadline
			}
			return ingestTestBoundary, nil
		},
	}

	cmd := NewCmdAll(ingestFactory(ios, store))
	cmd.SetArgs([]string{"ingest"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("all ingest = %v, want context.DeadlineExceeded", err)
	}
	if boundaryCalls != 1 {
		t.Errorf("ingest ran for %d resources, want 1 — an expired deadline must stop the fan-out, not warn-and-continue", boundaryCalls)
	}
}

// TestForEachResource_PartialResultsAreNotNoResults: ErrNoResults must only
// propagate when NOTHING was produced. One empty resource alongside a
// successful one is an ordinary outcome (a city with roads but no sidewalks),
// and returning exit 3 there would break every chained pipeline.
func TestForEachResource_PartialResultsAreNotNoResults(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	err := forEachResource(ios, func(rt resource.Source) error {
		if rt.Type() == resource.All[0].Type() {
			return nil
		}
		return cmdutil.ErrNoResults
	})
	if err != nil {
		t.Fatalf("forEachResource = %v; want nil when at least one resource produced results", err)
	}
}

// TestForEachResource_EmptyPlusFailureReportsTheFailure: a real failure must
// win over ErrNoResults, so the caller sees the actionable error rather than a
// generic "no results".
func TestForEachResource_EmptyPlusFailureReportsTheFailure(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	err := forEachResource(ios, func(rt resource.Source) error {
		if rt.Type() == resource.All[0].Type() {
			return cmdutil.ErrAllSourcesFailed
		}
		return cmdutil.ErrNoResults
	})
	if !errors.Is(err, cmdutil.ErrAllSourcesFailed) {
		t.Fatalf("forEachResource = %v; want the real failure, not ErrNoResults", err)
	}
	if errors.Is(err, cmdutil.ErrNoResults) {
		t.Error("ErrNoResults must not mask the actionable failure")
	}
}

// combinedTestBoundary is a ~600m box at 38°N, -120°. Sized so the road and
// parking fixtures below sit inside the bbox-derived hex grid with margin
// (mirrors pkg/cmd/compute's combined tests).
const combinedTestBoundary = `{"type":"Polygon","coordinates":[[[-120.003,37.998],[-119.997,37.998],[-119.997,38.002],[-120.003,38.002],[-120.003,37.998]]]}`

// computeFactory builds a factory for `all compute` over a store that has real
// geometry, so every per-resource pass produces a result instead of short-
// circuiting on ErrNoResults. That is what the q48z.6 regression needs: resErr
// must be nil for the combined pass's error to be the only thing left to
// report.
func computeFactory(ios *iostreams.IOStreams, store db.Store) *cmdutil.Factory {
	city := cmdtest.NewTestCity()
	cfg := cmdtest.NewTestConfig(city)
	return &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		Config:      func() (*config.Config, error) { return cfg, nil },
		HttpClient:  func() (*http.Client, error) { return &http.Client{}, nil },
		UnitSystem:  func() units.System { return units.Metric },
	}
}

func combinedTestStore(saveCombined func([]db.CityMetric) error) *dbtest.MockStore {
	roadFeature := db.Feature{
		ID:           "road1",
		ResourceType: resource.TypeRoads,
		Tags:         map[string]string{"highway": "residential", "width": "20"},
		GeometryJSON: `{"type":"LineString","coordinates":[[-120.0025,38.0],[-119.9975,38.0]]}`,
	}
	return &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return combinedTestBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
			if rt == resource.TypeRoads {
				return []db.Feature{roadFeature}, nil
			}
			return nil, nil
		},
		SaveCityMetricsFunc: func(_ context.Context, _ int64, rows []db.CityMetric) error {
			if len(rows) > 0 && resource.Type(rows[0].Subject).Bare() == resource.TypeCombined {
				return saveCombined(rows)
			}
			return nil
		},
	}
}

// TestAllCompute_CombinedFailureIsFatal pins q48z.6. Before the fix, a failing
// combined pass was a stderr warning and `all compute` returned resErr — nil
// whenever the three per-resource passes succeeded. So `pvmt all compute &&
// pvmt export` exited 0 and published a site whose cross-resource paved-area
// total was the previous run's, or absent.
//
// The store here fails ONLY the combined write, so every per-resource pass
// succeeds and resErr is nil. That isolates the regression: if the command
// returns nil, the combined error was swallowed.
func TestAllCompute_CombinedFailureIsFatal(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	wantErr := errors.New("database is locked")
	combinedWrites := 0
	store := combinedTestStore(func([]db.CityMetric) error {
		combinedWrites++
		return wantErr
	})

	cmd := NewCmdAll(computeFactory(ios, store))
	cmd.SetArgs([]string{"compute"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("all compute = nil; want the combined-pass failure to be fatal (exit non-zero)")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("all compute = %v; want it to wrap %v", err, wantErr)
	}
	if combinedWrites == 0 {
		t.Errorf("the combined pass never attempted its write; the fixture is not exercising it")
	}
}

// TestAllCompute_TUIInterruptAbortsCity: ctrl-C
// inside a step's TUI never cancels the request context (raw mode swallows
// SIGINT); it surfaces only as the step returning tui.ErrInterrupted. That
// must abort the city like any cancellation: no further resource passes, no
// combined pass, no best-effort steps, and nothing committed. It used to be
// demoted to "roads failed: interrupted" and the run carried on through every
// step, committed, and moved to the next city.
func TestAllCompute_TUIInterruptAbortsCity(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	var listed []resource.Type
	commits := 0
	store := combinedTestStore(func([]db.CityMetric) error {
		t.Error("the combined pass ran after an interrupt")
		return nil
	})
	store.ListFeaturesFunc = func(_ context.Context, rt resource.Type) ([]db.Feature, error) {
		listed = append(listed, rt)
		return nil, tui.ErrInterrupted // the user hit ctrl+c in the first step's TUI
	}
	store.CommitLatestFunc = func(context.Context, string, map[string]int64) error {
		commits++
		return nil
	}

	cmd := NewCmdAll(computeFactory(ios, store))
	cmd.SetArgs([]string{"compute"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	if err := cmd.ExecuteContext(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("all compute = %v, want a cancellation", err)
	}
	if len(listed) != 1 {
		t.Errorf("resource passes that read features = %v, want only the interrupted one", listed)
	}
	if commits != 0 {
		t.Errorf("committed %d snapshot(s) after an interrupt, want 0", commits)
	}
	if strings.Contains(errOut.String(), "failed:") {
		t.Errorf("the interrupt was reported as a step failure:\n%s", errOut.String())
	}
}

// TestAllCompute_SucceedsWhenCombinedSucceeds is the companion guard: making
// the combined failure fatal must not make a healthy run non-zero.
func TestAllCompute_SucceedsWhenCombinedSucceeds(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	combinedWrites := 0
	store := combinedTestStore(func([]db.CityMetric) error {
		combinedWrites++
		return nil
	})

	cmd := NewCmdAll(computeFactory(ios, store))
	cmd.SetArgs([]string{"compute"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("all compute = %v; want nil on a healthy run", err)
	}
	if combinedWrites == 0 {
		t.Errorf("the combined pass never ran")
	}
}

// TestAllCompute_CombinedFailureNotMaskedByEmptyResources covers the case
// TestAllCompute_CombinedFailureIsFatal cannot: there every resource produces a
// result, so resErr is nil and there is no sentinel to collide with.
//
// When every resource comes back empty, forEachResource returns
// cmdutil.ErrNoResults. errors.Join(ErrNoResults, combinedErr) still satisfies
// errors.Is(_, ErrNoResults), and BOTH ForEachCity and exitCode match that
// sentinel ahead of the generic arm — so a joined error would file the
// combined-pass failure under "nothing to do" and exit 0 on a multi-city run.
func TestAllCompute_CombinedFailureNotMaskedByEmptyResources(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	wantErr := errors.New("database is locked")

	// No features anywhere, so every per-resource pass reports ErrNoResults.
	// The combined pass is then failed at its boundary read — RunCombined
	// returns nil once it finds nothing to union, so a later failure point
	// would never be reached. The fan-out lists features once per resource, so
	// once listCalls has reached len(resource.All) the next boundary read is
	// RunCombined's; failedCombined pins that this actually happened, so the
	// test cannot silently degrade into asserting nothing.
	listCalls, failedCombined := 0, false
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			if listCalls >= len(resource.All) {
				failedCombined = true
				return "", wantErr
			}
			return combinedTestBoundary, nil
		},
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			listCalls++
			return nil, nil
		},
		SaveCityMetricsFunc: func(context.Context, int64, []db.CityMetric) error {
			t.Error("SaveCityMetrics called; the fixture has no features")
			return nil
		},
	}

	cmd := NewCmdAll(computeFactory(ios, store))
	cmd.SetArgs([]string{"compute"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatalf("all compute = nil; the combined-pass failure must not be swallowed")
	}
	if errors.Is(err, cmdutil.ErrNoResults) {
		t.Errorf("all compute = %v; it still matches ErrNoResults, which ForEachCity "+
			"files under skippedEmpty and exitCode maps to a silent 3 — the combined "+
			"failure is masked", err)
	}
	if !failedCombined {
		t.Errorf("the combined pass never reached its boundary read; the fixture is not " +
			"exercising the masking case")
	}
}

// TestAllIngest_FailuresFailTheRunWithRetryCommands:
// with every upstream down, `all ingest` still visits every step, then fails
// (not exit 0) with an error that lists each failed step, the fire and
// Census steps included, with the command that retries it.
func TestAllIngest_FailuresFailTheRunWithRetryCommands(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { return ingestTestBoundary, nil },
	}
	f := ingestFactory(ios, store)
	city, _ := f.CurrentCity()
	city.Overpass = true
	f.HttpClient = func() (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})}, nil
	}
	cmd := NewCmdAll(f)
	cmd.SetArgs([]string{"ingest"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.ExecuteContext(context.Background())
	if err == nil || errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("all ingest = %v; want a failure", err)
	}
	for _, want := range []string{
		"retry: pvmt --city " + city.Slug() + " roads ingest",
		"retry: pvmt --city " + city.Slug() + " sidewalks ingest",
		"fire ingest:", "retry: pvmt --city " + city.Slug() + " services ingest",
		"census ingest:", "retry: pvmt --city " + city.Slug() + " census ingest",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q\ngot: %v", want, err)
		}
	}
}

// TestAllCompute_StepsRecordTheGridsBoundary: every step records the boundary
// digest read before the shared grid was built, so a boundary ingest that
// lands after the grid makes the steps stale instead of passing a grid from
// the old boundary off as current.
func TestAllCompute_StepsRecordTheGridsBoundary(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	store := combinedTestStore(func([]db.CityMetric) error { return nil })
	reads := 0
	store.InputDigestsFunc = func(_ context.Context, inputs ...string) (map[string]string, error) {
		out := map[string]string{}
		for _, in := range inputs {
			out[in] = ""
		}
		// The first read is before the grid; every later one sees a boundary
		// re-ingested meanwhile.
		out[db.InputBoundary] = "new"
		if reads == 0 {
			out[db.InputBoundary] = "old"
		}
		reads++
		return out, nil
	}
	recorded := map[string]string{}
	store.CreateRunFunc = func(_ context.Context, producer, hash string, inputs map[string]string) (*db.Run, error) {
		recorded[producer] = inputs[db.InputBoundary]
		return &db.Run{ID: int64(len(recorded)), Producer: producer, ConfigHash: hash}, nil
	}

	cmd := NewCmdAll(computeFactory(ios, store))
	cmd.SetArgs([]string{"compute"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	_ = cmd.ExecuteContext(context.Background())

	if len(recorded) < 2 {
		t.Fatalf("runs created = %v, want the roads and combined runs at least", recorded)
	}
	for producer, digest := range recorded {
		if digest != "old" {
			t.Errorf("%s recorded boundary %q, want the digest read before the grid (\"old\")", producer, digest)
		}
	}
}

// TestAllIngest_RebuildBoundaryReadsTheCache: `all ingest --rebuild-boundary`
// re-resolves the boundary the store holds, as --force does, but through the
// HTTP cache: a repeat run is served from the cache entirely.
func TestAllIngest_RebuildBoundaryReadsTheCache(t *testing.T) {
	upstream := 0
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstream++
		body := `{"elements":[]}`
		if r.URL.Hostname() == "nominatim.openstreetmap.org" {
			body = `[{"addresstype":"city","geojson":` + ingestTestBoundary + `}]`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	client := &http.Client{Transport: cache.NewTransport(inner, t.TempDir(), cache.DefaultTTL)}
	saves := 0
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(context.Context) (string, error) { return ingestTestBoundary, nil },
		SaveBoundaryFunc: func(context.Context, string, string) error { saves++; return nil },
	}
	run := func() string {
		t.Helper()
		ios, _, _, errBuf := iostreams.Test()
		f := ingestFactory(ios, store)
		f.HttpClient = func() (*http.Client, error) { return client, nil }
		cmd := NewCmdAll(f)
		cmd.SetArgs([]string{"ingest", "--rebuild-boundary"})
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		_ = cmd.ExecuteContext(context.Background())
		return errBuf.String()
	}

	run()
	if saves != 1 {
		t.Fatalf("first run saved the boundary %d times, want 1", saves)
	}
	before := upstream
	stderr := run()
	if saves != 2 {
		t.Errorf("repeat run saved the boundary %d times in total, want 2: --rebuild-boundary must re-resolve it", saves)
	}
	if upstream != before {
		t.Errorf("repeat run made %d upstream requests; want 0 (--rebuild-boundary must not bypass the cache)", upstream-before)
	}
	if !strings.Contains(stderr, "Fetching boundary for") {
		t.Errorf("stderr = %q; want the boundary re-resolved", stderr)
	}
}
