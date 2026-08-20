package all

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
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
func TestNewCmdAll_RegistersSubcommands(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
	}

	cmd := NewCmdAll(f)
	if cmd == nil {
		t.Fatal("NewCmdAll returned nil")
	}
	if cmd.Use != "all" {
		t.Errorf("Use = %q, want %q", cmd.Use, "all")
	}

	got := map[string]bool{}
	for _, sub := range cmd.Commands() {
		got[sub.Name()] = true
	}
	for _, name := range []string{"ingest", "compute"} {
		if !got[name] {
			t.Errorf("missing %q subcommand", name)
		}
	}
	if got["status"] {
		t.Errorf("unexpected %q subcommand on `all` — `pvmt status` already covers all resources", "status")
	}
}

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
func ingestFactory(ios *iostreams.IOStreams, store db.Store) *cmdutil.Factory {
	city := cmdtest.NewTestCity()
	cfg := cmdtest.NewTestConfig(city)
	return &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		Config:      func() (*config.Config, error) { return cfg, nil },
		HttpClient:  func() (*http.Client, error) { return &http.Client{}, nil },
		UnitSystem:  func() units.System { return units.Imperial },
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
	if boundaryCalls != len(resource.All) {
		t.Errorf("ingested %d resources, want %d — the fan-out must visit every resource", boundaryCalls, len(resource.All))
	}
	if ctxSeen != len(resource.All) {
		t.Errorf("request context reached %d of %d ingest runs; a cancellable ctx must propagate to each", ctxSeen, len(resource.All))
	}
}

// TestAllIngest_CancelledContextAborts: a cancelled request context must stop
// the fan-out before any per-resource ingest work happens.
func TestAllIngest_CancelledContextAborts(t *testing.T) {
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
// TestAllIngest_CancelledContextAborts does not: there the context is already
// dead at Execute, so cmdutil.ForEachCity's pre-check stops everything before
// the first resource. Here the fan-out has already started and is cancelled
// between resources.
//
// dbtest.MockStore ignores ctx entirely, which is the whole point — real sqlite
// surfaces context.Canceled on its own, so without an explicit ctx.Err() check
// in ingest.RunResourceForCity this fixture would happily run every remaining
// resource after cancellation (solvent-streets-b3k2).
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
		t.Errorf("ingest ran for %d resources, want 1 — the fan-out must stop at the first cancelled resource", boundaryCalls)
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
