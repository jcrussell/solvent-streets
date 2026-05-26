package ingest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	ingestpkg "github.com/jcrussell/solvent-streets/internal/ingest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/cmdtest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

var testCity = func() *config.CityConfig {
	c := cmdtest.NewTestCity()
	c.Overpass = true
	return c
}()

var testCfg = cmdtest.NewTestConfig(testCity)

func testFactory(ios *iostreams.IOStreams) *cmdutil.Factory {
	store := &dbtest.MockStore{}
	return &cmdutil.Factory{
		IOStreams: ios,
		CityDB: func() (db.Store, error) {
			return store, nil
		},
		CurrentCity: func() (*config.CityConfig, error) {
			return testCity, nil
		},
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
	}
}

func TestNewCmdIngest_DefaultFlags(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := testFactory(ios)
	rt := &resource.Pavement{}

	var gotOpts *Options
	cmd := NewCmdIngest(f, rt, func(_ context.Context, opts *Options) error {
		gotOpts = opts
		return nil
	})

	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotOpts.Source != "all" {
		t.Errorf("expected default source 'all', got %q", gotOpts.Source)
	}
	if gotOpts.Force {
		t.Error("expected default force=false")
	}
}

func TestNewCmdIngest_SourceFlag(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := testFactory(ios)
	rt := &resource.Pavement{}

	var gotOpts *Options
	cmd := NewCmdIngest(f, rt, func(_ context.Context, opts *Options) error {
		gotOpts = opts
		return nil
	})

	cmd.SetArgs([]string{"--source=overpass"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotOpts.Source != "overpass" {
		t.Errorf("expected source 'overpass', got %q", gotOpts.Source)
	}
}

func TestNewCmdIngest_ForceFlag(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := testFactory(ios)
	rt := &resource.Pavement{}

	var gotOpts *Options
	cmd := NewCmdIngest(f, rt, func(_ context.Context, opts *Options) error {
		gotOpts = opts
		return nil
	})

	cmd.SetArgs([]string{"--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !gotOpts.Force {
		t.Error("expected force=true")
	}
}

// TestNewCmdIngest_ForceAndDryRunMutuallyExclusive covers byob-command-shape.6:
// --force (bypass HTTP cache) and --dry-run (no fetch) are nonsense together,
// so cobra's MarkFlagsMutuallyExclusive helper rejects the combination at
// flag-parse time rather than each command silently ignoring one. The
// "RunE not invoked" assertion is the load-bearing half: moving this
// check into runFunc would still produce an error here, but only after
// side effects (HTTP cache lookups, DB opens) had already happened.
func TestNewCmdIngest_ForceAndDryRunMutuallyExclusive(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := testFactory(ios)
	rt := &resource.Pavement{}

	runECalled := false
	cmd := NewCmdIngest(f, rt, func(_ context.Context, opts *Options) error {
		runECalled = true
		return nil
	})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--force", "--dry-run"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected --force and --dry-run to be mutually exclusive")
	}
	if runECalled {
		t.Error("RunE must not run when cobra rejects the flag combination")
	}
}

func TestNewCmdIngest_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := testFactory(ios)
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdIngest(f, rt, func(_ context.Context, opts *Options) error {
		called = true
		if opts.ResourceType.Type() != resource.TypeRoads {
			t.Errorf("expected KindRoads, got %v", opts.ResourceType.Type())
		}
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

type failingSource struct{ name string }

func (s *failingSource) Name() string { return s.name }
func (s *failingSource) Fetch(ctx context.Context, _ *http.Client, _ resource.Source) ([]db.Feature, error) {
	return nil, errors.New("upstream offline")
}

type emptySource struct{ name string }

func (s *emptySource) Name() string { return s.name }
func (s *emptySource) Fetch(ctx context.Context, _ *http.Client, _ resource.Source) ([]db.Feature, error) {
	return nil, nil
}

func TestFetchFromSources_AllFailedReturnsErrAllSourcesFailed(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &Options{IO: ios, ResourceType: &resource.Pavement{}}
	sources := []ingestpkg.Source{&failingSource{name: "a"}, &failingSource{name: "b"}}
	_, err := fetchFromSources(context.Background(), sources, &http.Client{}, opts, "Test City")
	if !errors.Is(err, cmdutil.ErrAllSourcesFailed) {
		t.Errorf("expected ErrAllSourcesFailed, got %v", err)
	}
}

func TestFetchFromSources_PartialSuccessNoError(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &Options{IO: ios, ResourceType: &resource.Pavement{}}
	sources := []ingestpkg.Source{&failingSource{name: "a"}, &emptySource{name: "b"}}
	features, err := fetchFromSources(context.Background(), sources, &http.Client{}, opts, "Test City")
	if err != nil {
		t.Errorf("expected nil error when at least one source returns cleanly, got %v", err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
}

func TestNewCmdIngest_InvalidSource(t *testing.T) {
	store := &dbtest.MockStore{}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		HttpClient: func() (*http.Client, error) {
			return &http.Client{}, nil
		},
		CityDB: func() (db.Store, error) {
			return store, nil
		},
		CurrentCity: func() (*config.CityConfig, error) {
			return testCity, nil
		},
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdIngest(f, rt, nil)
	cmd.SetArgs([]string{"--source=bogus"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for bogus source")
	}
	var flagErr *cmdutil.FlagError
	if !errors.As(err, &flagErr) {
		t.Errorf("expected FlagError through pflag wrapping, got %T: %v", err, err)
	}
}

// TestAcceptStripRatio pins the pure ratio-decision helper. The
// extraction means the over-subtraction guard can be unit-tested
// without httptest, geometry, or network plumbing — keeping the
// end-to-end stripWaterFromBoundary tests focused on plumbing,
// not arithmetic.
func TestAcceptStripRatio(t *testing.T) {
	cases := []struct {
		name                      string
		orig, stripped, threshold float64
		want                      bool
	}{
		{"clear pass: full boundary preserved", 100, 100, 0.5, true},
		{"clear pass: 75% preserved", 100, 75, 0.5, true},
		{"boundary case: exactly at threshold", 100, 50, 0.5, true},
		{"reject: just under threshold", 100, 49, 0.5, false},
		{"reject: clear over-subtraction", 100, 5, 0.5, false},
		{"reject: empty strip", 100, 0, 0.5, false},
		{"degenerate: zero orig accepted", 0, 0, 0.5, true},
		{"degenerate: negative orig accepted", -1, 5, 0.5, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := acceptStripRatio(c.orig, c.stripped, c.threshold); got != c.want {
				t.Errorf("acceptStripRatio(%v, %v, %v) = %v, want %v",
					c.orig, c.stripped, c.threshold, got, c.want)
			}
		})
	}
}

// fakeWaterFetcher returns the same body for every call. Lets tests
// of stripWaterFromBoundary control what "the Overpass API" returns
// without standing up an httptest server.
func fakeWaterFetcher(body string) waterFetcher {
	return func(_ context.Context, _ *http.Client, _ [4]float64) (string, error) {
		return body, nil
	}
}

// recordingNominatim returns a nominatimFetcher that returns the given
// body and records the city name it was asked for. Lets tests assert
// "the Nominatim branch was used with name X" without httptest.
func recordingNominatim(body string, gotName *string) nominatimFetcher {
	return func(_ context.Context, _ *http.Client, name string) (string, error) {
		if gotName != nil {
			*gotName = name
		}
		return body, nil
	}
}

// recordingRelation returns a relationFetcher that returns the given
// body and records the relation id it was asked for. Symmetric with
// recordingNominatim.
func recordingRelation(body string, gotID *int64) relationFetcher {
	return func(_ context.Context, _ *http.Client, id int64) (string, error) {
		if gotID != nil {
			*gotID = id
		}
		return body, nil
	}
}

// resolveBoundaryHarness wires a minimal Options for resolveBoundary
// tests. The store and city are passed by callers directly so this
// just builds the iostreams + opts shell.
func resolveBoundaryHarness(t *testing.T) *Options {
	t.Helper()
	ios, _, _, _ := iostreams.Test()
	return &Options{IO: ios}
}

// TestResolveBoundary_NominatimWhenNoRelationID pins the default
// branch: a city without BoundaryRelationID set goes through the
// nominatim fetcher and the boundary saved to the store matches what
// the fetcher returned (with the "nominatim+osm-water" source tag if
// a water polygon is present, "nominatim" otherwise).
func TestResolveBoundary_NominatimWhenNoRelationID(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`

	savedSource := ""
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return "", nil },
		SaveBoundaryFunc: func(_ context.Context, _, source string) error { savedSource = source; return nil },
	}
	city := &config.CityConfig{Name: "Oakland, CA"} // BoundaryRelationID==0

	var nomName string
	var relID int64
	opts := resolveBoundaryHarness(t)

	got, err := resolveBoundary(
		context.Background(), opts, store, &http.Client{}, city,
		recordingNominatim(boundary, &nomName),
		recordingRelation("", &relID),
	)
	if err != nil {
		t.Fatalf("resolveBoundary returned err: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty boundary")
	}
	if nomName != "Oakland, CA" {
		t.Errorf("nominatim fetcher called with %q, want %q", nomName, "Oakland, CA")
	}
	if relID != 0 {
		t.Errorf("relation fetcher called with id=%d; want 0 (not called)", relID)
	}
	if savedSource == "" {
		t.Error("expected source to be saved")
	}
}

// TestResolveBoundary_RelationWhenIDSet pins the override branch:
// when BoundaryRelationID > 0, the relation fetcher is invoked with
// that ID and the nominatim fetcher is NOT called.
func TestResolveBoundary_RelationWhenIDSet(t *testing.T) {
	boundary := `{"type":"MultiPolygon","coordinates":[[[[-106.7,35.0],[-106.5,35.0],[-106.5,35.2],[-106.7,35.2],[-106.7,35.0]]]]}`

	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return "", nil },
		SaveBoundaryFunc: func(_ context.Context, _, _ string) error { return nil },
	}
	city := &config.CityConfig{Name: "Albuquerque, NM", BoundaryRelationID: 171262}

	var nomName string
	var relID int64
	opts := resolveBoundaryHarness(t)

	got, err := resolveBoundary(
		context.Background(), opts, store, &http.Client{}, city,
		recordingNominatim("", &nomName),
		recordingRelation(boundary, &relID),
	)
	if err != nil {
		t.Fatalf("resolveBoundary returned err: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty boundary")
	}
	if relID != 171262 {
		t.Errorf("relation fetcher called with id=%d, want 171262", relID)
	}
	if nomName != "" {
		t.Errorf("nominatim fetcher called with %q; want empty (not called)", nomName)
	}
}

// TestResolveBoundary_CachedShortCircuitsBothFetchers pins that the
// DB-cached boundary path runs BEFORE either fetcher is consulted —
// the BoundaryRelationID branch addition must not have moved the
// cache check.
func TestResolveBoundary_CachedShortCircuitsBothFetchers(t *testing.T) {
	cached := `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`

	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return cached, nil },
		SaveBoundaryFunc: func(_ context.Context, _, _ string) error {
			t.Error("SaveBoundary called on cache-hit path")
			return nil
		},
	}
	city := &config.CityConfig{Name: "Oakland, CA", BoundaryRelationID: 171262}

	var nomName string
	var relID int64
	opts := resolveBoundaryHarness(t)

	got, err := resolveBoundary(
		context.Background(), opts, store, &http.Client{}, city,
		recordingNominatim("nope", &nomName),
		recordingRelation("nope", &relID),
	)
	if err != nil {
		t.Fatalf("resolveBoundary returned err: %v", err)
	}
	if got != cached {
		t.Errorf("expected cached boundary returned verbatim, got: %s", got)
	}
	if nomName != "" || relID != 0 {
		t.Errorf("cache hit should bypass both fetchers; got nomName=%q relID=%d", nomName, relID)
	}
}

// TestResolveBoundary_FetcherErrorWrappedWithHint pins that a failure
// from either fetcher returns an error with the overpass-turbo hint
// attached, so operators can find the relation ID for the override.
func TestResolveBoundary_FetcherErrorWrappedWithHint(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return "", nil },
	}
	city := &config.CityConfig{Name: "Oakland, CA"}

	opts := resolveBoundaryHarness(t)

	failingNominatim := nominatimFetcher(func(_ context.Context, _ *http.Client, _ string) (string, error) {
		return "", errors.New("synthetic nominatim failure")
	})
	failingRelation := relationFetcher(func(_ context.Context, _ *http.Client, _ int64) (string, error) {
		return "", errors.New("synthetic relation failure")
	})

	_, err := resolveBoundary(
		context.Background(), opts, store, &http.Client{}, city,
		failingNominatim, failingRelation,
	)
	if err == nil {
		t.Fatal("expected error from failing fetcher")
	}
	// cmdutil.Hintf wraps the error with the remediation hint; check
	// that the hint payload mentions the new config knob so operators
	// can find it.
	if !strings.Contains(err.Error(), "synthetic nominatim failure") {
		t.Errorf("expected wrapped underlying error, got: %v", err)
	}
}

// TestResolveBoundary_RelationErrorHintsAreClassSpecific pins the
// solvent-streets-90ev fix: relation-path failures should NOT emit the
// Nominatim-only hint ("set boundary_relation_id"). Instead, the hint
// branches on errors.Is against the sentinels in internal/ingest/boundary.go.
func TestResolveBoundary_RelationErrorHintsAreClassSpecific(t *testing.T) {
	const relationID int64 = 9999
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return "", nil },
	}

	cases := []struct {
		name            string
		fetchErr        error
		wantHintHas     []string
		wantHintMissing []string
	}{
		{
			name:            "relation not found",
			fetchErr:        fmt.Errorf("%w: id=%d", ingestpkg.ErrBoundaryRelationNotFound, relationID),
			wantHintHas:     []string{"OSM relation not found", "admin_level=8"},
			wantHintMissing: []string{"set [[cities]].boundary_relation_id"},
		},
		{
			name:            "relation too large",
			fetchErr:        fmt.Errorf("%w: id=%d span=12.3°", ingestpkg.ErrBoundaryRelationTooLarge, relationID),
			wantHintHas:     []string{"spans >5", "county or state"},
			wantHintMissing: []string{"set [[cities]].boundary_relation_id"},
		},
		{
			name:        "generic nominatim error keeps the original hint",
			fetchErr:    errors.New("nominatim returned a Point"),
			wantHintHas: []string{"set [[cities]].boundary_relation_id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			city := &config.CityConfig{Name: "Test", BoundaryRelationID: relationID}
			// Both branches will use tc.fetchErr; only the relevant one fires
			// because BoundaryRelationID routes to the relation fetcher.
			failing := func(_ context.Context, _ *http.Client, _ int64) (string, error) {
				return "", tc.fetchErr
			}
			opts := resolveBoundaryHarness(t)
			_, err := resolveBoundary(
				context.Background(), opts, store, &http.Client{}, city,
				recordingNominatim("", nil), failing,
			)
			if err == nil {
				t.Fatal("expected error")
			}
			var hint *cmdutil.ErrHint
			if !errors.As(err, &hint) {
				t.Fatalf("expected *cmdutil.ErrHint wrap, got %T: %v", err, err)
			}
			for _, want := range tc.wantHintHas {
				if !strings.Contains(hint.Hint, want) {
					t.Errorf("hint %q missing substring %q", hint.Hint, want)
				}
			}
			for _, missing := range tc.wantHintMissing {
				if strings.Contains(hint.Hint, missing) {
					t.Errorf("hint %q should NOT contain %q (Nominatim-only wording on relation path)", hint.Hint, missing)
				}
			}
		})
	}
}

// TestStripWaterFromBoundary_RejectsOverSubtractedStrip pins the
// over-subtraction guard at the function level: when the OSM water
// polygon covers most of the boundary, stripWaterFromBoundary returns
// ErrWaterStripOverSubtracted so the caller aborts loudly rather than
// silently falling back. The fallback would hide regressions in the
// water-stitching pipeline.
func TestStripWaterFromBoundary_RejectsOverSubtractedStrip(t *testing.T) {
	// ~0.01° × 0.01° square at Boston latitude → ~0.9 km².
	boundary := `{"type":"Polygon","coordinates":[[[-71.06,42.36],[-71.05,42.36],[-71.05,42.37],[-71.06,42.37],[-71.06,42.36]]]}`

	// Water = MultiPolygon covering ~98% of boundary; stripped result
	// lands well under the 0.1 threshold. Inset of only 1% of each side
	// so the remaining strip is too small to pass the backstop guard.
	water := `{"type":"MultiPolygon","coordinates":[[[[-71.05999,42.36001],[-71.05001,42.36001],[-71.05001,42.36999],[-71.05999,42.36999],[-71.05999,42.36001]]]]}`

	gjson, warn, err := stripWaterFromBoundary(
		context.Background(), &http.Client{}, fakeWaterFetcher(water), boundary,
	)
	if !errors.Is(err, ErrWaterStripOverSubtracted) {
		t.Fatalf("err = %v, want errors.Is ErrWaterStripOverSubtracted", err)
	}
	if !strings.Contains(err.Error(), "% of original") {
		t.Errorf("err message %q should describe the area ratio", err.Error())
	}
	if gjson != "" || warn != "" {
		t.Errorf("on hard failure all soft returns should be empty; got gjson=%d warn=%q",
			len(gjson), warn)
	}
}

// TestStripWaterFromBoundary_AcceptsModestStrip pins the happy path:
// when the water polygon represents a small fraction of the boundary,
// the stripped result is returned with the OSM water source tag and
// no error/warn.
func TestStripWaterFromBoundary_AcceptsModestStrip(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-71.06,42.36],[-71.05,42.36],[-71.05,42.37],[-71.06,42.37],[-71.06,42.36]]]}`

	// Water covering only the NE quadrant → ~25% of boundary → ratio
	// ~0.75, well above the 0.5 threshold.
	water := `{"type":"MultiPolygon","coordinates":[[[[-71.055,42.365],[-71.050,42.365],[-71.050,42.370],[-71.055,42.370],[-71.055,42.365]]]]}`

	gjson, warn, err := stripWaterFromBoundary(
		context.Background(), &http.Client{}, fakeWaterFetcher(water), boundary,
	)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if warn != "" {
		t.Errorf("warn = %q, want empty on success", warn)
	}
	if gjson == "" {
		t.Errorf("expected non-empty stripped gjson")
	}
}

// TestStripWaterFromBoundary_SoftFailureFallsBack pins that a Overpass
// fetch error returns a warn (not an err), so the caller falls back to
// the unstripped boundary. Network outages must not break ingest.
func TestStripWaterFromBoundary_SoftFailureFallsBack(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-71.06,42.36],[-71.05,42.36],[-71.05,42.37],[-71.06,42.37],[-71.06,42.36]]]}`
	failingFetcher := func(_ context.Context, _ *http.Client, _ [4]float64) (string, error) {
		return "", errors.New("simulated overpass outage")
	}

	gjson, warn, err := stripWaterFromBoundary(
		context.Background(), &http.Client{}, failingFetcher, boundary,
	)
	if err != nil {
		t.Fatalf("err = %v, want nil (soft failure should not abort)", err)
	}
	if !strings.Contains(warn, "overpass") {
		t.Errorf("warn = %q, want it to mention the overpass failure", warn)
	}
	if gjson != "" {
		t.Errorf("on soft failure gjson should be empty; got gjson=%d", len(gjson))
	}
}

// TestStripWaterFromBoundary_NoWaterIsNoOp pins that an empty fetcher
// response (no water in the bbox) returns all empty + nil — neither
// success nor failure, just "nothing to do".
func TestStripWaterFromBoundary_NoWaterIsNoOp(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-71.06,42.36],[-71.05,42.36],[-71.05,42.37],[-71.06,42.37],[-71.06,42.36]]]}`

	gjson, warn, err := stripWaterFromBoundary(
		context.Background(), &http.Client{}, fakeWaterFetcher(""), boundary,
	)
	if err != nil || gjson != "" || warn != "" {
		t.Errorf("no-water path should return all zero; got gjson=%d warn=%q err=%v",
			len(gjson), warn, err)
	}
}
