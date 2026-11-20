package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/hint"
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

// TestNewCmdIngest_ForceAndDryRunMutuallyExclusive:
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

// noSourceBoundary is a valid polygon so runIngest can derive a bbox without
// touching the network.
const noSourceBoundary = `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`

// TestRunResourceForCity_MatchesCommandDefaults pins the in-process entry
// point `all ingest` uses in place of executing a nested ingest cobra command
// with "--source all". Every default the retired flag-parse supplied must
// still hold: Source defaults to "all" (a zero-value Source would fail in
// resolveSources with "resolving source" long before ErrNoResults), --force is
// off (so the cached boundary is used instead of re-fetching Nominatim), and
// --dry-run is off (so no plan is printed and the real path runs).
func TestRunResourceForCity_MatchesCommandDefaults(t *testing.T) {
	ios, _, out, errBuf := iostreams.Test()
	// A city with no sources at all (overpass off, no arcgis_url) makes
	// AllSources empty, so the run reaches the "nothing fetched" branch
	// without any network access.
	city := cmdtest.NewTestCity()
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return noSourceBoundary, nil },
	}
	f := &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		Config:      func() (*config.Config, error) { return cmdtest.NewTestConfig(city), nil },
		HttpClient:  func() (*http.Client, error) { return &http.Client{}, nil },
	}

	err := RunResourceForCity(context.Background(), f, &resource.Pavement{})
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("RunResourceForCity = %v, want ErrNoResults", err)
	}
	if !strings.Contains(errBuf.String(), "Using cached boundary") {
		t.Errorf("expected the cached-boundary path (force=false), stderr = %q", errBuf.String())
	}
	if strings.Contains(out.String(), "[dry-run]") {
		t.Errorf("expected dry-run=false, stdout = %q", out.String())
	}
}

// roundTripFunc adapts a function to http.RoundTripper for offline tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeUpstream answers Nominatim with boundary and Overpass by query: the
// roads query with roadsBody, every other query (water, parking, sidewalks,
// facilities) with no elements. It counts the roads queries.
type fakeUpstream struct {
	boundary, roadsBody string
	roadsQueries        int
}

func (u *fakeUpstream) client() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"elements":[]}`
		switch {
		case r.URL.Hostname() == "nominatim.openstreetmap.org":
			body = `[{"addresstype":"city","geojson":` + u.boundary + `}]`
		case r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(raw))
			if strings.Contains(form.Get("data"), (&resource.Pavement{}).OverpassQuery([4]float64{})[:60]) {
				u.roadsQueries++
				body = u.roadsBody
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
}

// oneRoadInNoSourceBoundary is an Overpass roads answer with one residential
// way inside noSourceBoundary.
const oneRoadInNoSourceBoundary = `{"elements":[{"type":"way","id":10,"tags":{"highway":"residential"},
	"geometry":[{"lat":37.68,"lon":-121.80},{"lat":37.68,"lon":-121.79},{"lat":37.69,"lon":-121.78}]}]}`

// boundaryStore is a MockStore whose boundary reads back what was saved and
// that records each feature upsert's type and count.
func boundaryStore(saved *[]string, upserts *[]string) *dbtest.MockStore {
	var boundary string
	return &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { return boundary, nil },
		SaveBoundaryFunc: func(_ context.Context, gjson, source string) error {
			boundary = gjson
			*saved = append(*saved, source)
			return nil
		},
		UpsertFeaturesFunc: func(_ context.Context, rt resource.Type, f []db.Feature, _ []string) error {
			*upserts = append(*upserts, fmt.Sprintf("%s:%d", rt, len(f)))
			return nil
		},
	}
}

// TestRunResourceWithBoundary_RoadsStepReusesBoundaryRoads:
// `all ingest` resolves a fresh boundary up front,
// which fetches the roads over the unstripped boundary and saves them; the
// roads step then reports those instead of fetching again, so the roads,
// parking and sidewalks steps make exactly one roads query between them.
func TestRunResourceWithBoundary_RoadsStepReusesBoundaryRoads(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	city := cmdtest.NewTestCity()
	city.Overpass = true
	up := &fakeUpstream{boundary: noSourceBoundary, roadsBody: oneRoadInNoSourceBoundary}
	var saved, upserts []string
	store := boundaryStore(&saved, &upserts)
	f := &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		HttpClient:  func() (*http.Client, error) { return up.client(), nil },
	}
	b, err := ResolveBoundaryForCity(context.Background(), f, false)
	if err != nil {
		t.Fatalf("ResolveBoundaryForCity: %v\nstderr: %s", err, errOut)
	}
	for _, rt := range resource.All {
		err := RunResourceWithBoundary(context.Background(), f, rt, b)
		if rt.Type() == resource.TypeRoads && err != nil {
			t.Fatalf("roads step = %v, want the boundary's roads reported as a success", err)
		}
	}
	if up.roadsQueries != 1 {
		t.Errorf("roads queries = %d, want 1: the roads step must reuse the boundary's roads", up.roadsQueries)
	}
	if want := []string{"roads:1"}; !slices.Equal(upserts, want) {
		t.Errorf("feature upserts = %q, want %q", upserts, want)
	}
	if len(saved) != 1 {
		t.Errorf("boundary saves = %q, want one", saved)
	}
	if !strings.Contains(errOut.String(), "fetched with the boundary") {
		t.Errorf("stderr = %q; want the roads step to say it reused the boundary's roads", errOut)
	}
}

// TestRunIngest_ParkingOnFreshBoundarySavesRoads: `pvmt parking ingest` on a
// city with no stored boundary saves the roads its boundary fetched, and
// says so, before its own parking fetch.
func TestRunIngest_ParkingOnFreshBoundarySavesRoads(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	city := cmdtest.NewTestCity()
	city.Overpass = true
	up := &fakeUpstream{boundary: noSourceBoundary, roadsBody: oneRoadInNoSourceBoundary}
	var saved, upserts []string
	store := boundaryStore(&saved, &upserts)
	err := runIngest(context.Background(), &Options{
		IO:           ios,
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
		HttpClient:   func() (*http.Client, error) { return up.client(), nil },
		ResourceType: &resource.Parking{},
		Source:       cmdutil.SourceAll,
	})
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("parking ingest = %v, want ErrNoResults (no parking upstream)", err)
	}
	if want := []string{"roads:1"}; !slices.Equal(upserts, want) {
		t.Errorf("feature upserts = %q, want %q", upserts, want)
	}
	if !strings.Contains(errOut.String(), "Saved 1 roads") {
		t.Errorf("stderr = %q; want the roads saved with the boundary named", errOut)
	}
	got := map[string]db.IngestOutcome{}
	for _, a := range store.Attempts {
		got[a.Input+"/"+a.Source] = a.Outcome
	}
	if got["roads/overpass"] != db.IngestOK || got["boundary/nominatim"] != db.IngestOK {
		t.Errorf("attempts = %+v, want ok roads and boundary attempts", store.Attempts)
	}
}

// TestRunResourceForCity_UsesTheFactoryItIsGiven pins that the run reads its
// city and store from the factory ARGUMENT, not from any parent it was derived
// from. `all ingest` hands over the per-city factory ForEachCity built with
// withCity — a copy whose CurrentCity/CityDB are rebound — so a stale capture
// of the parent's closures would silently ingest the wrong city's data into
// the wrong store. Two factories with distinct cities and stores make that
// failure observable.
func TestRunResourceForCity_UsesTheFactoryItIsGiven(t *testing.T) {
	ios, _, _, errBuf := iostreams.Test()

	parentCity := cmdtest.NewTestCity()
	parentCity.Name = "Parent City"
	parentCalls := 0
	parentStore := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			parentCalls++
			return noSourceBoundary, nil
		},
	}
	parent := &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return parentStore, nil },
		CurrentCity: func() (*config.CityConfig, error) { return parentCity, nil },
		Config:      func() (*config.Config, error) { return cmdtest.NewTestConfig(parentCity), nil },
		HttpClient:  func() (*http.Client, error) { return &http.Client{}, nil },
	}

	// The city-scoped copy, shaped like cmdutil's withCity: same IO/Config/
	// HttpClient, rebound CurrentCity + CityDB.
	scopedCity := cmdtest.NewTestCity()
	scopedCity.Name = "Scoped City"
	scopedCalls := 0
	scopedStore := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) {
			scopedCalls++
			return noSourceBoundary, nil
		},
	}
	scoped := *parent
	scoped.CurrentCity = func() (*config.CityConfig, error) { return scopedCity, nil }
	scoped.CityDB = func() (db.Store, error) { return scopedStore, nil }

	if err := RunResourceForCity(context.Background(), &scoped, &resource.Pavement{}); !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("RunResourceForCity = %v, want ErrNoResults", err)
	}
	if scopedCalls != 1 {
		t.Errorf("scoped store read %d times, want 1", scopedCalls)
	}
	if parentCalls != 0 {
		t.Errorf("parent store read %d times; the scoped factory's store must be the only one used", parentCalls)
	}
	if !strings.Contains(errBuf.String(), scopedCity.Name) || strings.Contains(errBuf.String(), parentCity.Name) {
		t.Errorf("expected the run to name %q and never %q, stderr = %q", scopedCity.Name, parentCity.Name, errBuf.String())
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
	_, _, attempts, err := fetchFromSources(context.Background(), sources, &http.Client{}, opts, "Test City")
	if !errors.Is(err, cmdutil.ErrAllSourcesFailed) {
		t.Errorf("expected ErrAllSourcesFailed, got %v", err)
	}
	// Both failures are kept for the record.
	if len(attempts) != 2 || attempts[0].Outcome != db.IngestFailed || attempts[1].Error != "upstream offline" {
		t.Errorf("attempts = %+v, want two failed attempts with the error", attempts)
	}
}

func TestFetchFromSources_PartialSuccessNoError(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &Options{IO: ios, ResourceType: &resource.Pavement{}}
	sources := []ingestpkg.Source{&failingSource{name: "a"}, &emptySource{name: "b"}}
	features, succeeded, attempts, err := fetchFromSources(context.Background(), sources, &http.Client{}, opts, "Test City")
	if err != nil {
		t.Errorf("expected nil error when at least one source returns cleanly, got %v", err)
	}
	if len(attempts) != 2 || attempts[0].Source != "a" || attempts[0].Outcome != db.IngestFailed ||
		attempts[1].Source != "b" || attempts[1].Outcome != db.IngestEmpty || attempts[1].Input != "roads" {
		t.Errorf("attempts = %+v, want a failed and b empty, both for roads", attempts)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
	// Only the source that returned cleanly is reported as succeeded, so the
	// caller scopes the replace and the failed source's rows survive.
	if len(succeeded) != 1 || succeeded[0] != "b" {
		t.Errorf("succeeded = %v, want [b] (only the source that did not fail)", succeeded)
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
	if _, ok := errors.AsType[*cmdutil.FlagError](err); !ok {
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

// noRoads is a roadsFetcher for a city with no road sources.
func noRoads(context.Context, *http.Client, [4]float64) (roadsFetch, error) { return roadsFetch{}, nil }

// interiorProbes is the sparse land probes of boundary.
func interiorProbes(t *testing.T, boundary string) ingestpkg.LandProbes {
	t.Helper()
	probes, err := ingestpkg.InteriorLandProbes(boundary)
	if err != nil {
		t.Fatal(err)
	}
	return probes
}

// fakeWaterFetcher returns the same body for every call. Lets tests
// of stripWaterFromBoundary control what "the Overpass API" returns
// without standing up an httptest server.
func fakeWaterFetcher(body string) waterFetcher {
	return func(_ context.Context, _ *http.Client, _ [4]float64, _ ingestpkg.LandProbes) (string, error) {
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

	got, _, err := resolveBoundary(
		context.Background(), opts, store, &http.Client{}, city,
		boundaryFetchers{
			byName:     recordingNominatim(boundary, &nomName),
			byRelation: recordingRelation("", &relID),
			water:      fakeWaterFetcher(""),
			roads:      noRoads,
		},
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

	got, _, err := resolveBoundary(
		context.Background(), opts, store, &http.Client{}, city,
		boundaryFetchers{
			byName:     recordingNominatim("", &nomName),
			byRelation: recordingRelation(boundary, &relID),
			water:      fakeWaterFetcher(""),
			roads:      noRoads,
		},
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

	got, _, err := resolveBoundary(
		context.Background(), opts, store, &http.Client{}, city,
		boundaryFetchers{
			byName:     recordingNominatim("nope", &nomName),
			byRelation: recordingRelation("nope", &relID),
			water:      fakeWaterFetcher(""),
			roads:      noRoads,
		},
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

	_, _, err := resolveBoundary(
		context.Background(), opts, store, &http.Client{}, city,
		boundaryFetchers{byName: failingNominatim, byRelation: failingRelation, water: fakeWaterFetcher(""), roads: noRoads},
	)
	if err == nil {
		t.Fatal("expected error from failing fetcher")
	}
	// hint.Withf wraps the error with the remediation hint; check
	// that the hint payload mentions the new config knob so operators
	// can find it.
	if !strings.Contains(err.Error(), "synthetic nominatim failure") {
		t.Errorf("expected wrapped underlying error, got: %v", err)
	}
	// The failed fetch is recorded.
	if len(store.Attempts) != 1 || store.Attempts[0].Input != db.InputBoundary ||
		store.Attempts[0].Source != "nominatim" || store.Attempts[0].Outcome != db.IngestFailed ||
		!strings.Contains(store.Attempts[0].Error, "synthetic nominatim failure") {
		t.Errorf("attempts = %+v, want one failed nominatim boundary attempt", store.Attempts)
	}
}

// TestResolveBoundary_RecordsAttempts: a fetched
// boundary records an ok attempt under its source; a cached one records
// nothing, because no upstream was asked.
func TestResolveBoundary_RecordsAttempts(t *testing.T) {
	city := &config.CityConfig{Name: "Oakland, CA"}
	opts := resolveBoundaryHarness(t)
	fetchers := boundaryFetchers{byName: recordingNominatim(noSourceBoundary, nil), byRelation: nil, water: fakeWaterFetcher(""), roads: noRoads}

	fresh := &dbtest.MockStore{GetBoundaryFunc: func(_ context.Context) (string, error) { return "", nil }}
	if _, _, err := resolveBoundary(context.Background(), opts, fresh, &http.Client{}, city, fetchers); err != nil {
		t.Fatal(err)
	}
	if len(fresh.Attempts) != 1 || fresh.Attempts[0].Outcome != db.IngestOK || fresh.Attempts[0].Source != "nominatim" {
		t.Errorf("fetched boundary attempts = %+v, want one ok nominatim attempt", fresh.Attempts)
	}

	cached := &dbtest.MockStore{GetBoundaryFunc: func(_ context.Context) (string, error) { return noSourceBoundary, nil }}
	if _, _, err := resolveBoundary(context.Background(), opts, cached, &http.Client{}, city, fetchers); err != nil {
		t.Fatal(err)
	}
	if len(cached.Attempts) != 0 {
		t.Errorf("cached boundary attempts = %+v, want none", cached.Attempts)
	}
}

// TestResolveBoundary_RelationErrorHintsAreClassSpecific: relation-path failures should NOT emit the
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
			_, _, err := resolveBoundary(
				context.Background(), opts, store, &http.Client{}, city,
				boundaryFetchers{byName: recordingNominatim("", nil), byRelation: failing, water: fakeWaterFetcher(""), roads: noRoads},
			)
			if err == nil {
				t.Fatal("expected error")
			}
			var he *hint.ErrHint
			if !errors.As(err, &he) {
				t.Fatalf("expected *hint.ErrHint wrap, got %T: %v", err, err)
			}
			for _, want := range tc.wantHintHas {
				if !strings.Contains(he.Hint, want) {
					t.Errorf("hint %q missing substring %q", he.Hint, want)
				}
			}
			for _, missing := range tc.wantHintMissing {
				if strings.Contains(he.Hint, missing) {
					t.Errorf("hint %q should NOT contain %q (Nominatim-only wording on relation path)", he.Hint, missing)
				}
			}
		})
	}
}

// TestStripWaterFromBoundary_BackstopTripsOnDonutHole pins the
// area-ratio backstop: even after the upstream per-polygon land-probe
// filter (in parseWaterResponse) has dropped any individual polygon
// that overlaps city land, the assembled water can still
// collectively over-subtract if it has a hole containing the only
// land remaining. This test constructs that pathological case
// directly via fakeWaterFetcher (bypassing parseWaterResponse) to
// confirm the backstop still catches it as a HARD error.
//
// The 0.1-ratio guard is the last line of defense — every prior
// defense (acceptWaterPolygon's bbox-area cap, the per-polygon land-
// probe filter, closeOpenSubChain's coastline disambiguation) is
// per-polygon; only the backstop measures the collective effect.
func TestStripWaterFromBoundary_BackstopTripsOnDonutHole(t *testing.T) {
	// ~0.01° × 0.01° square at Boston latitude → ~0.9 km².
	boundary := `{"type":"Polygon","coordinates":[[[-71.06,42.36],[-71.05,42.36],[-71.05,42.37],[-71.06,42.37],[-71.06,42.36]]]}`

	// Donut MultiPolygon: outer = full boundary, hole = small region
	// containing the boundary's PointOnSurface (~ -71.055, 42.365).
	// The probe lies in the hole, so the per-polygon land-probe check
	// (if it ran here) would NOT drop the polygon; but subtraction
	// leaves only the ~1% hole area, well under the 10% backstop.
	water := `{"type":"MultiPolygon","coordinates":[[` +
		`[[-71.06,42.36],[-71.05,42.36],[-71.05,42.37],[-71.06,42.37],[-71.06,42.36]],` +
		`[[-71.0555,42.3645],[-71.0545,42.3645],[-71.0545,42.3655],[-71.0555,42.3655],[-71.0555,42.3645]]` +
		`]]}`

	gjson, warn, err := stripWaterFromBoundary(
		context.Background(), &http.Client{}, fakeWaterFetcher(water), boundary, interiorProbes(t, boundary),
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
		context.Background(), &http.Client{}, fakeWaterFetcher(water), boundary, interiorProbes(t, boundary),
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

// failingWaterFetcher simulates the water fetch failing upstream.
func failingWaterFetcher(_ context.Context, _ *http.Client, _ [4]float64, _ ingestpkg.LandProbes) (string, error) {
	return "", errors.New("simulated overpass outage")
}

// TestStripWaterFromBoundary_FetchErrorIsHard: a
// failed water fetch is an ErrWaterFetch error, not a soft warn. Falling
// back used to save the unstripped boundary, which every later ingest then
// reused, so one Overpass hiccup left open water in the city's area.
func TestStripWaterFromBoundary_FetchErrorIsHard(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-71.06,42.36],[-71.05,42.36],[-71.05,42.37],[-71.06,42.37],[-71.06,42.36]]]}`

	gjson, warn, err := stripWaterFromBoundary(
		context.Background(), &http.Client{}, failingWaterFetcher, boundary, interiorProbes(t, boundary),
	)
	if !errors.Is(err, ErrWaterFetch) {
		t.Fatalf("err = %v, want ErrWaterFetch", err)
	}
	if !strings.Contains(err.Error(), "simulated overpass outage") {
		t.Errorf("err = %v, want it to wrap the upstream failure", err)
	}
	if gjson != "" || warn != "" {
		t.Errorf("on a fetch error gjson and warn should be empty; got gjson=%d warn=%q", len(gjson), warn)
	}
}

// TestResolveBoundary_WaterFetchErrorSavesNothing pins the other half:
// When the water fetch fails, no boundary is saved,
// so the next ingest finds nothing cached and retries the fetch.
func TestResolveBoundary_WaterFetchErrorSavesNothing(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`
	saved := false
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(_ context.Context) (string, error) { return "", nil },
		SaveBoundaryFunc: func(_ context.Context, _, _ string) error { saved = true; return nil },
	}
	city := &config.CityConfig{Name: "Oakland, CA"}

	_, fresh, err := resolveBoundary(
		context.Background(), resolveBoundaryHarness(t), store, &http.Client{}, city,
		boundaryFetchers{
			byName:     recordingNominatim(boundary, nil),
			byRelation: recordingRelation("", nil),
			water:      failingWaterFetcher,
			roads:      noRoads,
		},
	)
	if !errors.Is(err, ErrWaterFetch) {
		t.Fatalf("err = %v, want ErrWaterFetch", err)
	}
	if saved {
		t.Error("SaveBoundary was called; a failed water fetch must save nothing so the next ingest retries")
	}
	if fresh != nil {
		t.Errorf("fresh = %+v, want nil on error", fresh)
	}
}

// TestResolveBoundary_CanceledWaterFetchHasNoOverloadHint: a Ctrl-C during
// the water fetch is the user stopping the run, not an overloaded Overpass, so
// it must stay a plain cancellation without the "wait a few minutes and
// re-run" hint.
func TestResolveBoundary_CanceledWaterFetchHasNoOverloadHint(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return "", nil },
		SaveBoundaryFunc: func(_ context.Context, _, _ string) error {
			t.Error("SaveBoundary called; a canceled water fetch must save nothing")
			return nil
		},
	}
	canceled := func(context.Context, *http.Client, [4]float64, ingestpkg.LandProbes) (string, error) {
		return "", context.Canceled
	}

	_, _, err := resolveBoundary(
		context.Background(), resolveBoundaryHarness(t), store, &http.Client{}, &config.CityConfig{Name: "Oakland, CA"},
		boundaryFetchers{byName: recordingNominatim(boundary, nil), byRelation: recordingRelation("", nil), water: canceled, roads: noRoads},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if he, ok := errors.AsType[*hint.ErrHint](err); ok {
		t.Errorf("a canceled water fetch carries hint %q, want none", he.Hint)
	}
}

// TestStripWaterFromBoundary_NoWaterIsNoOp pins that an empty fetcher
// response (no water in the bbox) returns all empty + nil — neither
// success nor failure, just "nothing to do".
func TestStripWaterFromBoundary_NoWaterIsNoOp(t *testing.T) {
	boundary := `{"type":"Polygon","coordinates":[[[-71.06,42.36],[-71.05,42.36],[-71.05,42.37],[-71.06,42.37],[-71.06,42.36]]]}`

	gjson, warn, err := stripWaterFromBoundary(
		context.Background(), &http.Client{}, fakeWaterFetcher(""), boundary, interiorProbes(t, boundary),
	)
	if err != nil || gjson != "" || warn != "" {
		t.Errorf("no-water path should return all zero; got gjson=%d warn=%q err=%v",
			len(gjson), warn, err)
	}
}

// TestResolveSources_DisabledSourceExplainsItself:
// `--source overpass` on a city with overpass unset used to fail with
// `resolving source "overpass": unknown source: overpass`, because
// SourceByName builds AllSources and reports "unknown" when nothing matches.
// But "overpass" IS a valid --source value (the --source flag rejects
// anything else before this point), so the user typed nothing wrong — their
// config disagrees with their flag, and "unknown source" sends them hunting for
// a typo.
func TestResolveSources_DisabledSourceExplainsItself(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	cases := []struct {
		name      string
		source    cmdutil.Source
		overpass  bool
		arcgisURL string
		wantHint  string
	}{
		{
			name: "overpass off", source: cmdutil.SourceOverpass,
			overpass: false, arcgisURL: "https://x",
			wantHint: "set overpass = true",
		},
		{
			name: "no arcgis url", source: cmdutil.SourceArcGIS,
			overpass: true, arcgisURL: "",
			wantHint: "set arcgis_url",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{Source: tc.source}
			_, err := resolveSources(opts, [4]float64{}, nil, tc.overpass, tc.arcgisURL, false, ios)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if strings.Contains(err.Error(), "unknown source") {
				t.Errorf("error = %q; the source is valid, it is disabled for this city", err)
			}
			var he *hint.ErrHint
			if !errors.As(err, &he) {
				t.Fatalf("error = %q; want an ErrHint carrying the remedy", err)
			}
			if !strings.Contains(he.Hint, tc.wantHint) {
				t.Errorf("hint = %q; want it to name %q", he.Hint, tc.wantHint)
			}
		})
	}
}

// TestResolveSources_EnabledSourcesStillResolve guards the obvious way to
// over-fix the above: rejecting a source that IS configured.
func TestResolveSources_EnabledSourcesStillResolve(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	for _, tc := range []struct {
		name      string
		source    cmdutil.Source
		overpass  bool
		arcgisURL string
	}{
		{"overpass on", cmdutil.SourceOverpass, true, ""},
		{"arcgis configured", cmdutil.SourceArcGIS, false, "https://x"},
		{"all", cmdutil.SourceAll, true, "https://x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{Source: tc.source}
			got, err := resolveSources(opts, [4]float64{}, nil, tc.overpass, tc.arcgisURL, false, ios)
			if err != nil {
				t.Fatalf("resolveSources: %v", err)
			}
			if len(got) == 0 {
				t.Error("resolveSources returned no sources")
			}
		})
	}
}

// untouchableFactory is a Factory whose every dependency fails the test when
// called: a command run against it must stop before reading the city, opening
// the database or building an HTTP client.
func untouchableFactory(t *testing.T, ios *iostreams.IOStreams) *cmdutil.Factory {
	t.Helper()
	return &cmdutil.Factory{
		IOStreams: ios,
		HttpClient: func() (*http.Client, error) {
			t.Error("HttpClient called; the source must be rejected before any network")
			return nil, errors.New("unreachable")
		},
		CityDB: func() (db.Store, error) {
			t.Error("CityDB called; the source must be rejected before the database is opened")
			return nil, errors.New("unreachable")
		},
		CurrentCity: func() (*config.CityConfig, error) {
			t.Error("CurrentCity called; the source must be rejected before the run starts")
			return nil, errors.New("unreachable")
		},
		Config: func() (*config.Config, error) {
			t.Error("Config called; the source must be rejected before the run starts")
			return nil, errors.New("unreachable")
		},
	}
}

// TestNewCmdIngest_SourceTheResourceCannotUse pins that a --source naming a
// real source the resource is not ingested from (census blocks come only from
// tigerweb, fire stations only from overpass) is a usage error: a FlagError
// (exit 2) at flag parse, before the city, database or HTTP client is touched,
// and the same with --dry-run, which must not print a plan the real run would
// reject.
func TestNewCmdIngest_SourceTheResourceCannotUse(t *testing.T) {
	cases := []struct {
		name   string
		rt     resource.Source
		source string
	}{
		{"census overpass", &resource.CensusBlocks{}, "overpass"},
		{"census arcgis", &resource.CensusBlocks{}, "arcgis"},
		{"fire arcgis", &resource.Facility{Service: "fire"}, "arcgis"},
		{"fire tigerweb", &resource.Facility{Service: "fire"}, "tigerweb"},
		{"roads tigerweb", &resource.Pavement{}, "tigerweb"},
	}
	for _, tc := range cases {
		for _, extra := range [][]string{nil, {"--dry-run"}} {
			args := append([]string{"--source", tc.source}, extra...)
			t.Run(tc.name+" "+strings.Join(extra, " "), func(t *testing.T) {
				ios, _, out, _ := iostreams.Test()
				cmd := NewCmdIngest(untouchableFactory(t, ios), tc.rt, nil)
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				cmd.SetArgs(args)
				err := cmd.Execute()
				if _, ok := errors.AsType[*cmdutil.FlagError](err); !ok {
					t.Fatalf("Execute(%v) = %T: %v, want a FlagError", args, err, err)
				}
				if !strings.Contains(err.Error(), tc.source) {
					t.Errorf("error = %q, want it to name the rejected source %q", err, tc.source)
				}
				if out.Len() != 0 {
					t.Errorf("stdout = %q, want no dry-run plan for a rejected source", out.String())
				}
			})
		}
	}
}

// TestNewCmdIngest_ValidSourcesPerResource pins the other half: each resource
// accepts "all" and the sources it is ingested from (an own-sourced resource
// thus accepts its own source's name), and --help lists exactly those.
func TestNewCmdIngest_ValidSourcesPerResource(t *testing.T) {
	for _, tc := range []struct {
		rt    resource.Source
		valid []string
	}{
		{&resource.CensusBlocks{}, []string{"all", "tigerweb"}},
		{&resource.Facility{Service: "fire"}, []string{"all", "overpass"}},
		{&resource.Pavement{}, []string{"all", "overpass", "arcgis"}},
	} {
		for _, source := range tc.valid {
			t.Run(string(tc.rt.Type())+" "+source, func(t *testing.T) {
				ios, _, _, _ := iostreams.Test()
				var got cmdutil.Source
				cmd := NewCmdIngest(testFactory(ios), tc.rt, func(_ context.Context, opts *Options) error {
					got = opts.Source
					return nil
				})
				cmd.SetArgs([]string{"--source", source})
				if err := cmd.Execute(); err != nil {
					t.Fatalf("--source %s: %v", source, err)
				}
				if got != cmdutil.Source(source) {
					t.Errorf("Source = %q, want %q", got, source)
				}
				usage := cmd.Flags().Lookup("source").Usage
				if want := "(" + strings.Join(tc.valid, "|") + ")"; !strings.Contains(usage, want) {
					t.Errorf("--source usage = %q, want it to list %s", usage, want)
				}
			})
		}
	}
}

// TestResolveSources_OwnSourcedTakesItsOwnSource pins that "all" and the
// resource's own source name both resolve to that one source, whatever the
// city's pavement sources are.
func TestResolveSources_OwnSourcedTakesItsOwnSource(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	for _, source := range []cmdutil.Source{cmdutil.SourceAll, "tigerweb"} {
		opts := &Options{ResourceType: &resource.CensusBlocks{}, Source: source}
		got, err := resolveSources(opts, [4]float64{}, nil, true, "https://x", false, ios)
		if err != nil {
			t.Fatalf("resolveSources(%s): %v", source, err)
		}
		if len(got) != 1 || got[0].Name() != "tigerweb" {
			names := make([]string, len(got))
			for i, s := range got {
				names[i] = s.Name()
			}
			t.Errorf("resolveSources(%s) = %v, want [tigerweb]", source, names)
		}
	}
}

// TestReplaceScope: only a --source all run whose
// every source succeeded replaces the whole resource. A single-source run on
// a city that also uses another source used to wipe that source's rows (all
// 9,992 Livermore OSM ways on `pvmt roads ingest --source arcgis`).
func TestReplaceScope(t *testing.T) {
	tests := []struct {
		name      string
		requested cmdutil.Source
		resolved  int
		succeeded []string
		want      []string
	}{
		{"all sources succeeded", cmdutil.SourceAll, 2, []string{"overpass", "arcgis"}, nil},
		{"one of two failed", cmdutil.SourceAll, 2, []string{"overpass"}, []string{"overpass"}},
		{"single source requested", cmdutil.SourceArcGIS, 1, []string{"arcgis"}, []string{"arcgis"}},
		{"overpass only requested", cmdutil.SourceOverpass, 1, []string{"overpass"}, []string{"overpass"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replaceScope(tt.requested, tt.resolved, tt.succeeded); !slices.Equal(got, tt.want) {
				t.Errorf("replaceScope = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRunIngest_SourceArcGISKeepsOverpassRows drives the --source arcgis
// wiring end to end against a real store:
// a city that uses both sources re-ingests ArcGIS only, and its stored
// Overpass rows must survive the save. TestReplaceScope covers the helper;
// this covers runIngest passing its result to UpsertFeatures.
func TestRunIngest_SourceArcGISKeepsOverpassRows(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"features":[
			{"properties":{"OBJECTID":7},"geometry":{"type":"LineString","coordinates":[[-121.80,37.68],[-121.79,37.68]]}},
			{"properties":{"OBJECTID":8},"geometry":{"type":"LineString","coordinates":[[-121.78,37.69],[-121.77,37.69]]}}
		],"properties":{"exceededTransferLimit":false}}`))
	}))
	t.Cleanup(srv.Close)

	root, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := root.EnsureCity(ctx, "test-city", "Test City", "test")
	if err != nil {
		t.Fatal(err)
	}
	store := root.ForCity(id)
	if err := store.SaveBoundary(ctx, noSourceBoundary, "nominatim"); err != nil {
		t.Fatal(err)
	}
	overpassRow := db.Feature{
		ID: "osm:way:1", ResourceType: resource.TypeRoads, Tags: map[string]string{"highway": "residential"},
		GeometryJSON: `{"type":"LineString","coordinates":[[-121.76,37.68],[-121.75,37.68]]}`,
		SourceAPI:    "overpass", FetchedAt: time.Now(),
	}
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, []db.Feature{overpassRow}, nil); err != nil {
		t.Fatal(err)
	}

	city := cmdtest.NewTestCity()
	city.Overpass = true
	city.ArcGISURL = srv.URL + "/query"
	city.AllowPrivateArcGIS = true // httptest binds 127.0.0.1
	ios, _, _, errBuf := iostreams.Test()
	opts := &Options{
		IO:           ios,
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
		HttpClient:   func() (*http.Client, error) { return srv.Client(), nil },
		ResourceType: &resource.Pavement{},
		Source:       cmdutil.SourceArcGIS,
	}
	if err := runIngest(ctx, opts); err != nil {
		t.Fatalf("runIngest: %v\nstderr: %s", err, errBuf)
	}

	rows, err := store.ListFeatures(ctx, resource.TypeRoads)
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string]int{}
	for _, r := range rows {
		bySource[r.SourceAPI]++
	}
	if bySource["overpass"] != 1 {
		t.Errorf("overpass rows after --source arcgis = %d, want the 1 stored row kept (all rows: %v)", bySource["overpass"], bySource)
	}
	if bySource["arcgis"] != 2 {
		t.Errorf("arcgis rows = %d, want 2 (all rows: %v)", bySource["arcgis"], bySource)
	}
}

// TestRunIngest_PartialSourceFailureSavesAndFails:
// when one of a city's sources fails, what answered is saved, but the run
// reports ErrPartialIngest naming the failed source, so `pvmt all ingest`
// exits 1 instead of publishing a city missing a source as if complete.
func TestRunIngest_PartialSourceFailureSavesAndFails(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"features":[
			{"properties":{"OBJECTID":7},"geometry":{"type":"LineString","coordinates":[[-121.80,37.68],[-121.79,37.68]]}}
		],"properties":{"exceededTransferLimit":false}}`))
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == ingestpkg.DefaultOverpassHost {
			return nil, errors.New("http2: timeout awaiting response headers")
		}
		return srv.Client().Transport.RoundTrip(r)
	})}

	var saved []db.Feature
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return noSourceBoundary, nil },
		UpsertFeaturesFunc: func(_ context.Context, _ resource.Type, f []db.Feature, _ []string) error {
			saved = append(saved, f...)
			return nil
		},
	}
	city := cmdtest.NewTestCity()
	city.Overpass = true
	city.ArcGISURL = srv.URL + "/query"
	city.AllowPrivateArcGIS = true
	ios, _, _, _ := iostreams.Test()
	err := runIngest(ctx, &Options{
		IO:           ios,
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
		HttpClient:   func() (*http.Client, error) { return client, nil },
		ResourceType: &resource.Pavement{},
		Source:       cmdutil.SourceAll,
	})
	if !errors.Is(err, cmdutil.ErrPartialIngest) || !strings.Contains(err.Error(), "overpass failed") {
		t.Fatalf("runIngest = %v; want ErrPartialIngest naming overpass", err)
	}
	if n := len(saved); n != 1 {
		t.Errorf("saved %d features, want the 1 ArcGIS row kept despite the failure", n)
	}
	// Both answers are on record: the partial-source status reads them.
	got := map[string]db.IngestOutcome{}
	for _, a := range store.Attempts {
		got[a.Source] = a.Outcome
	}
	if got["overpass"] != db.IngestFailed || got["arcgis"] != db.IngestOK || len(store.Attempts) != 2 {
		t.Errorf("attempts = %+v, want overpass failed and arcgis ok", store.Attempts)
	}
}

// TestRunIngest_SaveFailureRecordsFailedAttempts: a
// source that answered but whose rows could not be saved is recorded as
// failed, with the save error, not as a success.
func TestRunIngest_SaveFailureRecordsFailedAttempts(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"features":[
			{"properties":{"OBJECTID":7},"geometry":{"type":"LineString","coordinates":[[-121.80,37.68],[-121.79,37.68]]}}
		],"properties":{"exceededTransferLimit":false}}`))
	}))
	t.Cleanup(srv.Close)
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return noSourceBoundary, nil },
		UpsertFeaturesFunc: func(_ context.Context, _ resource.Type, _ []db.Feature, _ []string) error {
			return errors.New("disk full")
		},
	}
	city := cmdtest.NewTestCity()
	city.ArcGISURL = srv.URL + "/query"
	city.AllowPrivateArcGIS = true
	ios, _, _, _ := iostreams.Test()
	err := runIngest(ctx, &Options{
		IO:           ios,
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
		HttpClient:   func() (*http.Client, error) { return srv.Client(), nil },
		ResourceType: &resource.Pavement{},
		Source:       cmdutil.SourceAll,
	})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("runIngest = %v, want the save error", err)
	}
	if len(store.Attempts) != 1 || store.Attempts[0].Outcome != db.IngestFailed ||
		!strings.Contains(store.Attempts[0].Error, "disk full") {
		t.Errorf("attempts = %+v, want arcgis failed with the save error", store.Attempts)
	}
}

// TestRunIngest_PartialFailureWithEmptySurvivor: a
// failed source outranks an empty survivor. Reporting ErrNoResults would let
// `pvmt all ingest` skip the layer as empty and exit 0 with it missing. The
// store is not touched, so previously-stored rows are kept.
func TestRunIngest_PartialFailureWithEmptySurvivor(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"elements":[]}`))
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() != ingestpkg.DefaultOverpassHost {
			return nil, errors.New("http2: timeout awaiting response headers")
		}
		// Overpass answers, with no features: send it to the fake server.
		target, _ := url.Parse(srv.URL)
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return srv.Client().Transport.RoundTrip(r)
	})}

	upserts := 0
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return noSourceBoundary, nil },
		UpsertFeaturesFunc: func(_ context.Context, _ resource.Type, _ []db.Feature, _ []string) error {
			upserts++
			return nil
		},
	}
	city := cmdtest.NewTestCity()
	city.Overpass = true
	city.ArcGISURL = srv.URL + "/query"
	city.AllowPrivateArcGIS = true
	ios, _, _, _ := iostreams.Test()
	err := runIngest(ctx, &Options{
		IO:           ios,
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
		HttpClient:   func() (*http.Client, error) { return client, nil },
		ResourceType: &resource.Pavement{},
		Source:       cmdutil.SourceAll,
	})
	if !errors.Is(err, cmdutil.ErrPartialIngest) || errors.Is(err, cmdutil.ErrNoResults) ||
		!strings.Contains(err.Error(), "arcgis failed") {
		t.Fatalf("runIngest = %v; want ErrPartialIngest naming arcgis", err)
	}
	if upserts != 0 {
		t.Errorf("UpsertFeatures called %d times; an empty fetch must not touch the store", upserts)
	}
}

// dryRunOutput runs printDryRunPlan for city against a store whose cached
// boundary is cachedBoundary.
func dryRunOutput(t *testing.T, city *config.CityConfig, source cmdutil.Source, cachedBoundary string) string {
	t.Helper()
	out, _ := dryRunStreams(t, city, source, cachedBoundary)
	return out
}

// dryRunStreams is dryRunOutput's stdout (the plan) and stderr (warnings).
func dryRunStreams(t *testing.T, city *config.CityConfig, source cmdutil.Source, cachedBoundary string) (string, string) {
	t.Helper()
	ios, _, out, errOut := iostreams.Test()
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return cachedBoundary, nil },
	}
	opts := &Options{IO: ios, ResourceType: resource.ByType(resource.TypeRoads), Source: source, DryRun: true}
	if err := printDryRunPlan(context.Background(), opts, store, city); err != nil {
		t.Fatalf("printDryRunPlan: %v", err)
	}
	return out.String(), errOut.String()
}

// TestPrintDryRunPlan_BoundarySourceMatchesTheRealRun: the dry run names the
// upstream the real run fetches the boundary from, which is the OSM relation
// via Overpass for a city with boundary_relation_id and Nominatim otherwise.
func TestPrintDryRunPlan_BoundarySourceMatchesTheRealRun(t *testing.T) {
	cases := []struct {
		name string
		city *config.CityConfig
	}{
		{"relation", &config.CityConfig{Name: "Albuquerque, NM", Overpass: true, BoundaryRelationID: 171262}},
		{"nominatim", &config.CityConfig{Name: "Oakland, CA", Overpass: true}},
	}
	const boundary = `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// What the real run says it fetches from.
			ios, _, _, errOut := iostreams.Test()
			store := &dbtest.MockStore{
				GetBoundaryFunc:  func(_ context.Context) (string, error) { return "", nil },
				SaveBoundaryFunc: func(_ context.Context, _, _ string) error { return nil },
			}
			_, _, err := resolveBoundary(context.Background(), &Options{IO: ios}, store, &http.Client{}, tc.city,
				boundaryFetchers{
					byName:     recordingNominatim(boundary, nil),
					byRelation: recordingRelation(boundary, nil),
					water:      fakeWaterFetcher(""),
					roads:      noRoads,
				})
			if err != nil {
				t.Fatalf("resolveBoundary: %v", err)
			}
			from := boundarySourceFor(tc.city).from
			if !strings.Contains(errOut.String(), "from "+from+"...") {
				t.Fatalf("real run stderr = %q; want it to fetch from %q", errOut.String(), from)
			}

			got := dryRunOutput(t, tc.city, cmdutil.SourceAll, "")
			if !strings.Contains(got, "would fetch boundary for \""+tc.city.Name+"\" from "+from+"\n") {
				t.Errorf("dry run = %q; want the boundary fetched from %q", got, from)
			}
			if tc.city.BoundaryRelationID > 0 && strings.Contains(got, "Nominatim") {
				t.Errorf("dry run = %q; a city with boundary_relation_id never asks Nominatim", got)
			}

			cached := dryRunOutput(t, tc.city, cmdutil.SourceAll, boundary)
			if !strings.Contains(cached, "boundary cached, would skip "+from+"\n") {
				t.Errorf("dry run with a cached boundary = %q; want it to skip %q", cached, from)
			}
		})
	}
}

// TestPrintDryRunPlan_DisabledSourceGivesTheRealRunsReason: for a --source the
// city has switched off, the dry run warns with the remedy the real run's
// error hint carries.
func TestPrintDryRunPlan_DisabledSourceGivesTheRealRunsReason(t *testing.T) {
	cases := []struct {
		name   string
		source cmdutil.Source
		city   *config.CityConfig
	}{
		{"overpass off", cmdutil.SourceOverpass, &config.CityConfig{Name: "X", ArcGISURL: "https://x"}},
		{"no arcgis url", cmdutil.SourceArcGIS, &config.CityConfig{Name: "X", Overpass: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			_, err := resolveSources(&Options{Source: tc.source}, [4]float64{}, nil, tc.city.Overpass, tc.city.ArcGISURL, false, ios)
			var he *hint.ErrHint
			if !errors.As(err, &he) {
				t.Fatalf("resolveSources error = %v; want an ErrHint", err)
			}
			_, got := dryRunStreams(t, tc.city, tc.source, "cached")
			if !strings.HasPrefix(got, "warning: ") || !strings.Contains(got, he.Hint) {
				t.Errorf("dry run stderr = %q; want a warning carrying %q", got, he.Hint)
			}
		})
	}
	_, enabled := dryRunStreams(t, &config.CityConfig{Name: "X", Overpass: true}, cmdutil.SourceOverpass, "cached")
	if enabled != "" {
		t.Errorf("dry run stderr = %q; an enabled source needs no warning", enabled)
	}
}

// TestRunIngest_CensusOptOut: a city with
// census = false is skipped by `pvmt census ingest` (and its --dry-run), as
// `pvmt census compute` and `pvmt all` already skip it, before any fetch.
func TestRunIngest_CensusOptOut(t *testing.T) {
	for _, dry := range []bool{false, true} {
		off := false
		city := cmdtest.NewTestCity()
		city.Census = &off
		ios, _, _, errOut := iostreams.Test()
		err := runIngest(context.Background(), &Options{
			IO:           ios,
			CityDB:       func() (db.Store, error) { t.Fatal("store opened"); return nil, errors.New("unreachable") },
			CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
			HttpClient:   func() (*http.Client, error) { t.Fatal("http client built"); return nil, errors.New("unreachable") },
			ResourceType: &resource.CensusBlocks{},
			Source:       cmdutil.SourceAll,
			DryRun:       dry,
		})
		if !errors.Is(err, cmdutil.ErrNoResults) {
			t.Errorf("dry-run=%v: err = %v, want ErrNoResults", dry, err)
		}
		if !strings.Contains(errOut.String(), "census = false") {
			t.Errorf("dry-run=%v: stderr = %q, want the opt-out named", dry, errOut.String())
		}
	}
}

// TestRunIngest_DisabledSourceFailsBeforeBoundary: a
// --source the city's pvmt.toml switched off fails before any network work,
// with the hint naming the setting.
func TestRunIngest_DisabledSourceFailsBeforeBoundary(t *testing.T) {
	city := cmdtest.NewTestCity()
	city.Overpass = false
	city.ArcGISURL = "https://example.test/query"
	ios, _, _, _ := iostreams.Test()
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { t.Fatal("boundary resolved"); return "", nil },
	}
	err := runIngest(context.Background(), &Options{
		IO:           ios,
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
		HttpClient:   func() (*http.Client, error) { return &http.Client{}, nil },
		ResourceType: &resource.Pavement{},
		Source:       cmdutil.SourceOverpass,
	})
	var h *hint.ErrHint
	if err == nil || !strings.Contains(err.Error(), "not available for Test City") || !errors.As(err, &h) {
		t.Fatalf("err = %v, want the disabled-source error with a hint", err)
	}
}

// TestRunResourceForCity_FacilitiesFetchPaddedWithRoutingRoads pins:
// A service's facilities are fetched over the boundary
// bbox padded by resource.ServiceAreaPadM, so a neighbouring station just
// across the line exists, and the ingest also fetches the routing roads (a
// companion) over the same padded bbox, so the graph reaches it. Each is
// saved under its own type.
func TestRunResourceForCity_FacilitiesFetchPaddedWithRoutingRoads(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	city := cmdtest.NewTestCity()
	var queries []string
	var upserted []resource.Type
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(context.Context) (string, error) { return bigSquareBoundary, nil },
		UpsertFeaturesFunc: func(_ context.Context, rt resource.Type, _ []db.Feature, _ []string) error {
			upserted = append(upserted, rt)
			return nil
		},
	}
	const body = `{"elements":[
		{"type":"node","id":1,"lat":-0.02,"lon":0.5,"tags":{"amenity":"fire_station"}},
		{"type":"node","id":2,"lat":-0.03,"lon":0.5},
		{"type":"way","id":10,"nodes":[1,2],"tags":{"highway":"residential"}}]}`
	f := &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		HttpClient: func() (*http.Client, error) {
			return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if err := r.ParseForm(); err != nil {
					return nil, err
				}
				queries = append(queries, r.PostForm.Get("data"))
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(body))}, nil
			})}, nil
		},
	}
	fac := &resource.Facility{Service: "fire", Selectors: []string{`["amenity"="fire_station"]`}}
	if err := RunResourceForCity(context.Background(), f, fac); err != nil {
		t.Fatalf("facility ingest = %v", err)
	}
	if want := []resource.Type{resource.FacilityType("fire"), resource.TypeRoutingRoads}; !slices.Equal(upserted, want) {
		t.Errorf("upserted %v, want %v", upserted, want)
	}
	if len(queries) != 2 {
		t.Fatalf("overpass queries = %d, want 2 (stations, then routing roads)", len(queries))
	}
	padded := geo.PadBBox([4]float64{0, 0, 2, 2}, resource.ServiceAreaPadM)
	wantBBox := fmt.Sprintf("(%f,%f,%f,%f)", padded[0], padded[1], padded[2], padded[3])
	for i, q := range queries {
		if !strings.Contains(q, wantBBox) {
			t.Errorf("query %d is not over the padded bbox %s:\n%s", i, wantBBox, q)
		}
	}
	if !strings.Contains(queries[0], "fire_station") || !strings.Contains(queries[1], `"highway"~`) {
		t.Errorf("queries = %q, want the stations then the routing roads", queries)
	}
}

// TestRunIngest_RoutingRoadsFetchTheRing: a city whose
// roads come from Overpass fetches its routing roads as the ring outside its
// boundary bbox; a city without OSM roads fetches the whole padded bbox.
func TestRunIngest_RoutingRoadsFetchTheRing(t *testing.T) {
	for _, overpass := range []bool{true, false} {
		t.Run(fmt.Sprintf("overpass=%v", overpass), func(t *testing.T) {
			var query string
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				form, _ := url.ParseQuery(string(body))
				query = form.Get("data")
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: r,
					Body: io.NopCloser(strings.NewReader(`{"elements":[]}`))}, nil
			})}
			store := &dbtest.MockStore{
				GetBoundaryFunc: func(_ context.Context) (string, error) { return noSourceBoundary, nil },
			}
			city := cmdtest.NewTestCity()
			city.Overpass = overpass
			if !overpass {
				city.ArcGISURL = "https://example.com/arcgis/query"
			}
			ios, _, _, _ := iostreams.Test()
			err := runIngest(context.Background(), &Options{
				IO:           ios,
				CityDB:       func() (db.Store, error) { return store, nil },
				CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
				HttpClient:   func() (*http.Client, error) { return client, nil },
				ResourceType: &resource.RoutingRoads{},
				Source:       cmdutil.SourceAll,
			})
			if err != nil && !errors.Is(err, cmdutil.ErrNoResults) {
				t.Fatalf("runIngest: %v", err)
			}
			if got := strings.Contains(query, "- way["); got != overpass {
				t.Errorf("difference query = %v, want %v:\n%s", got, overpass, query)
			}
		})
	}
}

func TestNewCmdIngest_RebuildBoundaryFlag(t *testing.T) {
	// --rebuild-boundary goes with --dry-run (it changes the plan) and with
	// --force (which implies it).
	for _, args := range [][]string{{"--rebuild-boundary"}, {"--rebuild-boundary", "--dry-run"}, {"--rebuild-boundary", "--force"}} {
		ios, _, _, _ := iostreams.Test()
		var gotOpts *Options
		cmd := NewCmdIngest(testFactory(ios), &resource.Pavement{}, func(_ context.Context, opts *Options) error {
			gotOpts = opts
			return nil
		})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !gotOpts.RebuildBoundary {
			t.Errorf("%v: RebuildBoundary = false, want true", args)
		}
	}
}

// TestResolveBoundary_RebuildReResolvesStoredBoundary: --rebuild-boundary
// re-resolves a boundary the store already holds, as --force does, but
// without marking the context to bypass the HTTP cache, so the replay reads
// the cached responses.
func TestResolveBoundary_RebuildReResolvesStoredBoundary(t *testing.T) {
	stored := `{"type":"Polygon","coordinates":[[[-122.5,37.5],[-122.4,37.5],[-122.4,37.6],[-122.5,37.6],[-122.5,37.5]]]}`
	var saves []string
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(context.Context) (string, error) { return stored, nil },
		SaveBoundaryFunc: func(_ context.Context, _, source string) error { saves = append(saves, source); return nil },
	}
	var name string
	ios, _, _, errOut := iostreams.Test()
	_, fresh, err := resolveBoundary(context.Background(), &Options{IO: ios, RebuildBoundary: true}, store, &http.Client{},
		&config.CityConfig{Name: "Oakland, CA"},
		boundaryFetchers{byName: recordingNominatim(stored, &name), byRelation: recordingRelation("", nil), water: fakeWaterFetcher(""), roads: noRoads})
	if err != nil {
		t.Fatal(err)
	}
	if name != "Oakland, CA" || len(saves) != 1 || fresh == nil {
		t.Errorf("nominatim asked for %q, saves = %q, fresh = %v; want the stored boundary re-resolved and saved once", name, saves, fresh)
	}
	if strings.Contains(errOut.String(), "Using cached boundary") {
		t.Errorf("stderr = %q; --rebuild-boundary must not reuse the stored boundary", errOut.String())
	}
}

// TestPrintDryRunPlan_RebuildBoundary: the dry run of --rebuild-boundary on a
// stored boundary says it would re-resolve it from the HTTP cache.
func TestPrintDryRunPlan_RebuildBoundary(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	store := &dbtest.MockStore{GetBoundaryFunc: func(context.Context) (string, error) { return noSourceBoundary, nil }}
	opts := &Options{IO: ios, ResourceType: &resource.Pavement{}, Source: cmdutil.SourceAll, DryRun: true, RebuildBoundary: true}
	city := &config.CityConfig{Name: "Oakland, CA", Overpass: true}
	if err := printDryRunPlan(context.Background(), opts, store, city); err != nil {
		t.Fatal(err)
	}
	want := `would re-resolve the stored boundary for "Oakland, CA" from Nominatim, through the HTTP cache`
	if got := out.String(); !strings.Contains(got, want) || strings.Contains(got, "boundary cached, would skip") {
		t.Errorf("dry run = %q; want %q", got, want)
	}
	// A fresh boundary fetches the roads as the strip's land evidence; a
	// cached one does not.
	const roads = "would fetch roads over the unstripped boundary bbox as land evidence for the water strip"
	if got := out.String(); !strings.Contains(got, roads) {
		t.Errorf("dry run = %q; want %q", got, roads)
	}
	if got := dryRunOutput(t, city, cmdutil.SourceAll, noSourceBoundary); strings.Contains(got, roads) {
		t.Errorf("dry run with a cached boundary = %q; want no roads fetch for the strip", got)
	}
}

// nominatimAndEmptyOverpass answers Nominatim with boundary as a city result
// and every other request with an empty Overpass result, counting the
// Nominatim requests.
func nominatimAndEmptyOverpass(boundary string, nominatim *int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"elements":[]}`
		if r.URL.Hostname() == "nominatim.openstreetmap.org" {
			*nominatim++
			body = `[{"addresstype":"city","geojson":` + boundary + `}]`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
}

// TestRunIngest_CompanionDoesNotRebuildBoundaryAgain: a facility ingest with
// --rebuild-boundary re-resolves the boundary once; its routing-roads
// companion reads the result from the store instead of resolving it again.
func TestRunIngest_CompanionDoesNotRebuildBoundaryAgain(t *testing.T) {
	nominatim := 0
	store := &dbtest.MockStore{
		GetBoundaryFunc:  func(context.Context) (string, error) { return noSourceBoundary, nil },
		SaveBoundaryFunc: func(context.Context, string, string) error { return nil },
	}
	city := cmdtest.NewTestCity()
	ios, _, _, _ := iostreams.Test()
	err := runIngest(context.Background(), &Options{
		IO:              ios,
		CityDB:          func() (db.Store, error) { return store, nil },
		CurrentCity:     func() (*config.CityConfig, error) { return city, nil },
		HttpClient:      func() (*http.Client, error) { return nominatimAndEmptyOverpass(noSourceBoundary, &nominatim), nil },
		ResourceType:    &resource.Facility{Service: "fire", Selectors: []string{`["amenity"="fire_station"]`}},
		Source:          cmdutil.SourceAll,
		RebuildBoundary: true,
	})
	if err != nil && !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("runIngest: %v", err)
	}
	if nominatim != 1 {
		t.Errorf("nominatim asked %d times, want 1: the companion must not re-resolve the boundary", nominatim)
	}
}

// roadLine is an Overpass road feature along (lon0,lat0)->(lon1,lat1).
func roadLine(id string, lon0, lat0, lon1, lat1 float64) db.Feature {
	return db.Feature{ID: id, ResourceType: resource.TypeRoads, SourceAPI: "overpass", Tags: map[string]string{"highway": "residential"},
		GeometryJSON: fmt.Sprintf(`{"type":"LineString","coordinates":[[%v,%v],[%v,%v]]}`, lon0, lat0, lon1, lat1)}
}

// recordingRoads is a roadsFetcher answering features from overpass, and
// recording the bbox it was asked for.
func recordingRoads(features []db.Feature, gotBBox *[4]float64) roadsFetcher {
	return func(_ context.Context, _ *http.Client, bbox [4]float64) (roadsFetch, error) {
		if gotBBox != nil {
			*gotBBox = bbox
		}
		return roadsFetch{features: features, sources: []string{"overpass"}, succeeded: []string{"overpass"},
			attempts: []db.IngestAttempt{{Input: "roads", Source: "overpass", Outcome: db.IngestOK, FeatureCount: len(features)}}}, nil
	}
}

// recordingWater is a waterFetcher answering body and recording the probes
// it was given.
func recordingWater(body string, got *ingestpkg.LandProbes) waterFetcher {
	return func(_ context.Context, _ *http.Client, _ [4]float64, probes ingestpkg.LandProbes) (string, error) {
		*got = probes
		return body, nil
	}
}

// TestFetchBoundary_FetchesRoadsOverUnstrippedBBox:
// the roads are fetched over the unstripped boundary's bbox, which holds
// every boundary the strip can produce, and become the water strip's dense
// land probes.
func TestFetchBoundary_FetchesRoadsOverUnstrippedBBox(t *testing.T) {
	// The water is the right half, so the stripped boundary's bbox is half
	// the unstripped one's.
	water := `{"type":"MultiPolygon","coordinates":[[[[1,0],[2,0],[2,2],[1,2],[1,0]]]]}`
	var roads []db.Feature
	for i := range 40 {
		y := 0.02 + 0.045*float64(i)
		roads = append(roads, roadLine(fmt.Sprint("r", i), 0.2, y, 0.3, y))
	}
	var gotBBox [4]float64
	var probes ingestpkg.LandProbes
	var saved, upserts []string
	store := boundaryStore(&saved, &upserts)
	_, b, err := resolveBoundary(context.Background(), resolveBoundaryHarness(t), store, &http.Client{},
		&config.CityConfig{Name: "Oakland, CA"},
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: recordingWater(water, &probes),
			roads: recordingRoads(roads, &gotBBox)})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := geo.BBoxFromGeoJSON(bigSquareBoundary); gotBBox != want {
		t.Errorf("roads fetched over %v, want the unstripped boundary's bbox %v", gotBBox, want)
	}
	if !probes.Dense || len(probes.Points) != 40 {
		t.Errorf("water probes = %+v, want the 40 roads' dense probes", probes)
	}
	if want := []string{"nominatim+osm-water"}; !slices.Equal(saved, want) {
		t.Errorf("boundary saves = %q, want %q", saved, want)
	}
	if b == nil || b.roads == nil || b.roads.saved != 40 || b.roads.result() != nil {
		t.Errorf("boundary roads = %+v, want every road saved", b)
	}
}

// TestFetchBoundary_InvertedStripKeepsRoadsForFinalBoundary
// (the El Segundo case): when the backstop replaces an
// inverted strip with its complement, the boundary is saved once, and the
// saved roads are the ones touching the FINAL boundary's bbox, not the
// inverted shape's.
func TestFetchBoundary_InvertedStripKeepsRoadsForFinalBoundary(t *testing.T) {
	// The "water" came back as the land: [0,1]×[0,1.9], leaving the
	// right half plus a strip along the top (invertedStripBoundary).
	water := `{"type":"MultiPolygon","coordinates":[[[[0,0],[1,0],[1,1.9],[0,1.9],[0,0]]]]}`
	var roads []db.Feature
	for i := range 20 {
		y := 0.1 + 0.08*float64(i)
		roads = append(roads, roadLine(fmt.Sprintf("land%d", i), 0.2, y, 0.4, y))
	}
	roads = append(roads,
		roadLine("east", 1.5, 0.5, 1.6, 0.5),     // in the inverted strip, outside the land's bbox
		roadLine("crossing", 1.2, 1.0, 0.8, 2.0), // no vertex in the land's bbox, a segment crosses it
	)
	var probes ingestpkg.LandProbes
	var saved []string
	var upserted []db.Feature
	store := &dbtest.MockStore{
		SaveBoundaryFunc: func(_ context.Context, _, source string) error { saved = append(saved, source); return nil },
		UpsertFeaturesFunc: func(_ context.Context, _ resource.Type, f []db.Feature, _ []string) error {
			upserted = f
			return nil
		},
	}
	ios, _, _, errOut := iostreams.Test()
	_, b, err := resolveBoundary(context.Background(), &Options{IO: ios}, store, &http.Client{},
		&config.CityConfig{Name: "El Segundo, CA"},
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: recordingWater(water, &probes),
			roads: recordingRoads(roads, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nominatim+osm-water-inverted"}; !slices.Equal(saved, want) {
		t.Errorf("boundary saves = %q, want the complement saved once: %q", saved, want)
	}
	var ids []string
	for _, f := range upserted {
		ids = append(ids, f.ID)
	}
	if len(ids) != 21 || slices.Contains(ids, "east") || !slices.Contains(ids, "crossing") {
		t.Errorf("saved roads = %q, want the 20 land roads and the crossing one, not the east one", ids)
	}
	if b.roads.saved != 21 {
		t.Errorf("roads outcome saved = %d, want 21", b.roads.saved)
	}
	if !strings.Contains(errOut.String(), "Water-strip inversion recovered") {
		t.Errorf("stderr = %q; want the backstop's warning", errOut)
	}
	if len(store.Attempts) == 0 || store.Attempts[0].Input != "roads" || store.Attempts[0].FeatureCount != 21 {
		t.Errorf("attempts = %+v, want the roads attempt to record the 21 roads saved", store.Attempts)
	}
}

// TestResolveBoundary_RoadsFetchErrorSavesNothing: when every road source
// fails, the boundary has no land evidence; like a failed water fetch that is
// a hard error that saves nothing, so the next ingest retries.
func TestResolveBoundary_RoadsFetchErrorSavesNothing(t *testing.T) {
	store := &dbtest.MockStore{
		SaveBoundaryFunc: func(context.Context, string, string) error { t.Error("SaveBoundary called"); return nil },
		UpsertFeaturesFunc: func(context.Context, resource.Type, []db.Feature, []string) error {
			t.Error("UpsertFeatures called")
			return nil
		},
	}
	failing := func(context.Context, *http.Client, [4]float64) (roadsFetch, error) {
		return roadsFetch{sources: []string{"overpass"}, attempts: []db.IngestAttempt{{Input: "roads", Source: "overpass", Outcome: db.IngestFailed, Error: "timeout"}}},
			fmt.Errorf("roads: %w", cmdutil.ErrAllSourcesFailed)
	}
	water := func(context.Context, *http.Client, [4]float64, ingestpkg.LandProbes) (string, error) {
		t.Error("water fetched after the roads failed")
		return "", nil
	}
	_, b, err := resolveBoundary(context.Background(), resolveBoundaryHarness(t), store, &http.Client{},
		&config.CityConfig{Name: "Oakland, CA"},
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: water, roads: failing})
	if !errors.Is(err, ErrRoadsFetch) || !errors.Is(err, cmdutil.ErrAllSourcesFailed) {
		t.Fatalf("err = %v, want ErrRoadsFetch wrapping the source failure", err)
	}
	var he *hint.ErrHint
	if !errors.As(err, &he) || !strings.Contains(he.Hint, "next ingest retries") {
		t.Errorf("err = %v, want the retry hint", err)
	}
	if b != nil {
		t.Errorf("boundary = %+v, want nil on error", b)
	}
	got := map[string]db.IngestOutcome{}
	for _, a := range store.Attempts {
		got[a.Input] = a.Outcome
	}
	if got["roads"] != db.IngestFailed || got[db.InputBoundary] != db.IngestFailed {
		t.Errorf("attempts = %+v, want failed roads and boundary attempts", store.Attempts)
	}
}

// TestResolveBoundary_CanceledRoadsFetchHasNoHint: a Ctrl-C during the roads
// fetch is a plain cancellation, recorded as no boundary attempt.
func TestResolveBoundary_CanceledRoadsFetchHasNoHint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	canceling := func(context.Context, *http.Client, [4]float64) (roadsFetch, error) {
		cancel()
		return roadsFetch{sources: []string{"overpass"}}, fmt.Errorf("roads: %w", cmdutil.ErrAllSourcesFailed)
	}
	store := &dbtest.MockStore{}
	_, _, err := resolveBoundary(ctx, resolveBoundaryHarness(t), store, &http.Client{}, &config.CityConfig{Name: "Oakland, CA"},
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: fakeWaterFetcher(""), roads: canceling})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if he, ok := errors.AsType[*hint.ErrHint](err); ok {
		t.Errorf("a canceled roads fetch carries hint %q, want none", he.Hint)
	}
	if len(store.Attempts) != 0 {
		t.Errorf("attempts = %+v, want none for a canceled run", store.Attempts)
	}
}

// TestResolveBoundary_NoRoadsFallsBackToInteriorPoints: a city with no road
// inside its boundary strips water with the boundary's interior points, as
// before roads were land evidence, and warns that it did.
func TestResolveBoundary_NoRoadsFallsBackToInteriorPoints(t *testing.T) {
	var probes ingestpkg.LandProbes
	ios, _, _, errOut := iostreams.Test()
	outside := []db.Feature{roadLine("far", 5, 5, 5.1, 5.1)}
	_, _, err := resolveBoundary(context.Background(), &Options{IO: ios}, &dbtest.MockStore{}, &http.Client{},
		&config.CityConfig{Name: "Oakland, CA"},
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: recordingWater("", &probes),
			roads: recordingRoads(outside, nil)})
	if err != nil {
		t.Fatal(err)
	}
	want := interiorProbes(t, bigSquareBoundary)
	if probes.Dense || probes.MaxHits != 0 || !slices.Equal(probes.Points, want.Points) {
		t.Errorf("water probes = %+v, want the interior points %+v", probes, want)
	}
	if !strings.Contains(errOut.String(), "no road probes") {
		t.Errorf("stderr = %q; want the fallback warned about", errOut)
	}
}

// bridgedRiverCity is the review's ArcGIS-bridge scenario: a 0.06°×0.05°
// city cut by a north-south river, residential roads on both banks, and
// four bridges over the river, each mapped in OSM (bridge=yes) and again
// by the agency ArcGIS layer, which carries no bridge tag.
func bridgedRiverCity() (boundary, waterBody string, osm, arcgis []db.Feature) {
	boundary = `{"type":"Polygon","coordinates":[[[0,0],[0.06,0],[0.06,0.05],[0,0.05],[0,0]]]}`
	waterBody = `{"elements":[{"type":"way","id":7,"tags":{"natural":"water"},"geometry":[` +
		`{"lat":-0.01,"lon":0.022},{"lat":-0.01,"lon":0.028},{"lat":0.06,"lon":0.028},{"lat":0.06,"lon":0.022},{"lat":-0.01,"lon":0.022}]}]}`
	road := func(id, src string, tags map[string]string, x0, y0, x1, y1 float64) db.Feature {
		return db.Feature{ID: id, ResourceType: resource.TypeRoads, SourceAPI: src, Tags: tags,
			GeometryJSON: fmt.Sprintf(`{"type":"LineString","coordinates":[[%g,%g],[%g,%g]]}`, x0, y0, x1, y1)}
	}
	n := 0
	for x := 0.001; x < 0.06; x += 0.002 {
		if x > 0.021 && x < 0.029 {
			continue
		}
		for y := 0.001; y < 0.05; y += 0.002 {
			n++
			res := map[string]string{"highway": "residential"}
			osm = append(osm, road(fmt.Sprint("o", n), "overpass", res, x, y, x+0.0005, y+0.0005))
			arcgis = append(arcgis, road(fmt.Sprint("a", n), "arcgis", res, x, y, x+0.0005, y+0.0005))
		}
	}
	for i, y := range []float64{0.0105, 0.0205, 0.0305, 0.0405} {
		osm = append(osm, road(fmt.Sprint("ob", i), "overpass", map[string]string{"highway": "primary", "bridge": "yes"}, 0.021, y, 0.029, y))
		arcgis = append(arcgis, road(fmt.Sprint("ab", i), "arcgis", map[string]string{"highway": "primary"}, 0.021, y, 0.029, y))
	}
	return boundary, waterBody, osm, arcgis
}

// TestFetchBoundary_ArcGISBridgesDoNotKeepTheRiver: in a city with both road
// sources, only the OSM roads are land probes, so the ArcGIS copies of its
// bridges (untagged) do not make the river read as city land; an
// ArcGIS-only city still gets road probes from ArcGIS.
func TestFetchBoundary_ArcGISBridgesDoNotKeepTheRiver(t *testing.T) {
	boundary, waterBody, osm, arcgis := bridgedRiverCity()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(waterBody)), Request: r}, nil
	})}
	both := &config.CityConfig{Name: "Bridges, CA", Overpass: true, ArcGISURL: "https://example.test/query"}
	var saved, upserts []string
	_, _, err := resolveBoundary(context.Background(), resolveBoundaryHarness(t), boundaryStore(&saved, &upserts), client, both,
		boundaryFetchers{byName: recordingNominatim(boundary, nil), water: defaultOverpassWater,
			roads: recordingRoads(append(slices.Clone(osm), arcgis...), nil)})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nominatim+osm-water"}; !slices.Equal(saved, want) {
		t.Errorf("boundary saves = %q, want the river stripped: %q", saved, want)
	}

	if got := landEvidenceRoads(append(slices.Clone(osm), arcgis...), both); len(got) != len(osm) {
		t.Errorf("land evidence for an Overpass city = %d roads, want the %d OSM ones", len(got), len(osm))
	}
	arcgisOnly := &config.CityConfig{Name: "Agency, CA", ArcGISURL: "https://example.test/query"}
	if got := landEvidenceRoads(arcgis, arcgisOnly); len(got) != len(arcgis) {
		t.Errorf("land evidence for an ArcGIS-only city = %d roads, want all %d", len(got), len(arcgis))
	}
}

// TestResolveBoundary_FewRoadsFallBackToInteriorPoints: a few roads inside
// the boundary are too few to be dense probes, so the strip uses the
// interior points, with the fallback warning.
func TestResolveBoundary_FewRoadsFallBackToInteriorPoints(t *testing.T) {
	var probes ingestpkg.LandProbes
	ios, _, _, errOut := iostreams.Test()
	few := []db.Feature{roadLine("a", 0.2, 0.2, 0.3, 0.3), roadLine("b", 0.5, 1.5, 0.6, 1.5)}
	_, _, err := resolveBoundary(context.Background(), &Options{IO: ios}, &dbtest.MockStore{}, &http.Client{},
		&config.CityConfig{Name: "Oakland, CA"},
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: recordingWater("", &probes),
			roads: recordingRoads(few, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if probes.Dense || !slices.Equal(probes.Points, interiorProbes(t, bigSquareBoundary).Points) {
		t.Errorf("water probes = %+v, want the interior points", probes)
	}
	if !strings.Contains(errOut.String(), "no road probes") {
		t.Errorf("stderr = %q; want the fallback warned about", errOut)
	}
}

// TestResolveBoundary_FailureAfterRoadsRecordsThem: roads fetched for a
// boundary that then fails are not saved, and their attempt is recorded as
// failed with the reason, so `pvmt status` does not read them as stored.
func TestResolveBoundary_FailureAfterRoadsRecordsThem(t *testing.T) {
	store := &dbtest.MockStore{}
	_, _, err := resolveBoundary(context.Background(), resolveBoundaryHarness(t), store, &http.Client{},
		&config.CityConfig{Name: "Oakland, CA"},
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: failingWaterFetcher,
			roads: recordingRoads([]db.Feature{roadLine("a", 0.2, 0.2, 0.3, 0.3)}, nil)})
	if !errors.Is(err, ErrWaterFetch) {
		t.Fatalf("err = %v, want ErrWaterFetch", err)
	}
	var roads *db.IngestAttempt
	for i, a := range store.Attempts {
		if a.Input == "roads" {
			roads = &store.Attempts[i]
		}
	}
	if roads == nil || roads.Outcome != db.IngestFailed || !strings.Contains(roads.Error, "not saved") ||
		!strings.Contains(roads.Error, "simulated overpass outage") {
		t.Errorf("attempts = %+v, want the roads recorded as failed, not saved, with the water error", store.Attempts)
	}
}

// TestRunIngest_ParkingFailsOnBoundaryRoadsSaveFailure: a parking ingest on
// a fresh boundary whose roads could not be saved fails, naming the roads
// and their retry command, rather than exiting as an empty parking layer.
func TestRunIngest_ParkingFailsOnBoundaryRoadsSaveFailure(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	city := cmdtest.NewTestCity()
	city.Overpass = true
	up := &fakeUpstream{boundary: noSourceBoundary, roadsBody: oneRoadInNoSourceBoundary}
	var saved, upserts []string
	store := boundaryStore(&saved, &upserts)
	store.UpsertFeaturesFunc = func(_ context.Context, rt resource.Type, _ []db.Feature, _ []string) error {
		if rt == resource.TypeRoads {
			return errors.New("disk full")
		}
		return nil
	}
	err := runIngest(context.Background(), &Options{
		IO:           ios,
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
		HttpClient:   func() (*http.Client, error) { return up.client(), nil },
		ResourceType: &resource.Parking{},
		Source:       cmdutil.SourceAll,
	})
	if err == nil || errors.Is(err, cmdutil.ErrNoResults) || !strings.Contains(err.Error(), "disk full") ||
		!slices.Contains(hint.Hints(err), "retry it with 'pvmt --city "+city.Slug()+" roads ingest'") {
		t.Fatalf("parking ingest = %v, want the roads save failure with its retry command", err)
	}
}

// TestReportBoundaryRoads: how a non-roads ingest reports the roads its
// fresh boundary saved.
func TestReportBoundaryRoads(t *testing.T) {
	city := cmdtest.NewTestCity()
	parkingErr := errors.New("parking: arcgis failed")
	cases := []struct {
		name     string
		roads    *roadsOutcome
		err      error
		wantErr  []error
		noErr    bool
		wantWarn bool
	}{
		{name: "cached boundary", roads: nil, err: parkingErr, wantErr: []error{parkingErr}},
		{name: "all saved", roads: &roadsOutcome{saved: 3, succeeded: []string{"overpass"}}, noErr: true},
		{name: "a source failed", roads: &roadsOutcome{saved: 3, succeeded: []string{"arcgis"}, failed: []string{"overpass"}},
			noErr: true, wantWarn: true},
		{name: "save failed, own run empty", roads: &roadsOutcome{err: errors.New("disk full")}, err: cmdutil.ErrNoResults},
		{name: "save failed, own run failed", roads: &roadsOutcome{err: errors.New("disk full")}, err: parkingErr,
			wantErr: []error{parkingErr}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ios, _, _, errOut := iostreams.Test()
			err := reportBoundaryRoads(ios, city, c.roads, c.err)
			if c.noErr != (err == nil) {
				t.Fatalf("err = %v, want nil: %v", err, c.noErr)
			}
			for _, want := range c.wantErr {
				if !errors.Is(err, want) {
					t.Errorf("err = %v, want it to wrap %v", err, want)
				}
			}
			if c.roads != nil && c.roads.err != nil && (errors.Is(err, cmdutil.ErrNoResults) || !strings.Contains(err.Error(), "disk full")) {
				t.Errorf("err = %v, want the roads save failure, not an empty run", err)
			}
			warned := strings.Contains(errOut.String(), "overpass failed") &&
				strings.Contains(errOut.String(), "hint: retry it with 'pvmt --city "+city.Slug()+" roads ingest'")
			if warned != c.wantWarn {
				t.Errorf("stderr = %q, want the failed-source warning: %v", errOut, c.wantWarn)
			}
		})
	}
}

// TestSaveBoundaryRoads_ReplacesOnlyTheSourcesThatAnswered: when a road
// source fails, the boundary's roads replace only the rows of the sources
// that answered, so the failed source's stored rows survive; when every
// source answered they replace the whole set.
func TestSaveBoundaryRoads_ReplacesOnlyTheSourcesThatAnswered(t *testing.T) {
	city := cmdtest.NewTestCity()
	features := []db.Feature{roadLine("a", 0.2, 0.2, 0.3, 0.3)}
	features[0].SourceAPI = "arcgis"
	for _, c := range []struct {
		name      string
		succeeded []string
		want      []string
		failed    []string
	}{
		{"one failed", []string{"arcgis"}, []string{"arcgis"}, []string{"overpass"}},
		{"all answered", []string{"overpass", "arcgis"}, nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var scope []string
			called := false
			store := &dbtest.MockStore{UpsertFeaturesFunc: func(_ context.Context, _ resource.Type, _ []db.Feature, s []string) error {
				called, scope = true, s
				return nil
			}}
			out := saveBoundaryRoads(context.Background(), resolveBoundaryHarness(t), store, city, bigSquareBoundary,
				roadsFetch{features: features, sources: []string{"overpass", "arcgis"}, succeeded: c.succeeded})
			if !called || !slices.Equal(scope, c.want) {
				t.Errorf("replace scope = %q (called %v), want %q", scope, called, c.want)
			}
			if !slices.Equal(out.failed, c.failed) || out.saved != 1 {
				t.Errorf("outcome = %+v, want failed %q and 1 saved", out, c.failed)
			}
		})
	}
}

func TestRoadsOutcome_Result(t *testing.T) {
	saveErr := errors.New("disk full")
	for _, c := range []struct {
		name    string
		o       roadsOutcome
		want    error
		partial string
	}{
		{"saved", roadsOutcome{saved: 2, succeeded: []string{"overpass"}}, nil, ""},
		{"save failed", roadsOutcome{saved: 0, err: saveErr}, saveErr, ""},
		{"nothing upstream", roadsOutcome{succeeded: []string{"overpass"}}, cmdutil.ErrNoResults, ""},
		{"empty survivor", roadsOutcome{succeeded: []string{"arcgis"}, failed: []string{"overpass"}}, cmdutil.ErrPartialIngest, "overpass failed"},
		{"partial", roadsOutcome{saved: 2, succeeded: []string{"arcgis"}, failed: []string{"overpass"}}, cmdutil.ErrPartialIngest, "overpass failed, saved 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.o.result()
			if c.want == nil {
				if err != nil {
					t.Fatalf("result = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.partial) {
				t.Errorf("result = %v, want %v naming %q", err, c.want, c.partial)
			}
			if errors.Is(c.want, cmdutil.ErrPartialIngest) && errors.Is(err, cmdutil.ErrNoResults) {
				t.Errorf("result = %v also matches ErrNoResults", err)
			}
		})
	}
}

// TestFetchBoundary_CityOnALakeIslandIsNotASubtractFailure: a city wholly on
// an island (the inner ring) of a lake relation has no water to strip; it
// is saved unstripped with no "water strip skipped" warning.
func TestFetchBoundary_CityOnALakeIslandIsNotASubtractFailure(t *testing.T) {
	body := `{"elements":[{"type":"relation","id":88,"tags":{"natural":"water","type":"multipolygon"},"members":[` +
		`{"type":"way","ref":1,"role":"outer","geometry":[{"lat":-5,"lon":-5},{"lat":-5,"lon":6},{"lat":6,"lon":6},{"lat":6,"lon":-5},{"lat":-5,"lon":-5}]},` +
		`{"type":"way","ref":2,"role":"inner","geometry":[{"lat":-2,"lon":-2},{"lat":-2,"lon":3},{"lat":3,"lon":3},{"lat":3,"lon":-2},{"lat":-2,"lon":-2}]}]}]}`
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	var roads []db.Feature
	for i := range 40 {
		y := 0.02 + 0.045*float64(i)
		roads = append(roads, roadLine(fmt.Sprint("r", i), 0.2, y, 0.3, y))
	}
	ios, _, _, errOut := iostreams.Test()
	var saved, upserts []string
	_, _, err := resolveBoundary(context.Background(), &Options{IO: ios}, boundaryStore(&saved, &upserts), client,
		&config.CityConfig{Name: "Island, NY"},
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: defaultOverpassWater, roads: recordingRoads(roads, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "water strip skipped") || !slices.Equal(saved, []string{"nominatim"}) {
		t.Errorf("saves = %q, stderr = %q; want the boundary saved with no water and no skipped-strip warning", saved, errOut)
	}
}

// TestResolveBoundary_OverpassRoadsFailureIsHardEvenIfArcGISAnswered: for a
// city that uses Overpass, its OSM roads are the water strip's land
// evidence, so their failing saves nothing (no boundary, no roads) and asks
// for a retry, though ArcGIS answered; the boundary is not built on the
// interior points.
func TestResolveBoundary_OverpassRoadsFailureIsHardEvenIfArcGISAnswered(t *testing.T) {
	var arcgis []db.Feature
	for i := range 40 {
		y := 0.02 + 0.045*float64(i)
		f := roadLine(fmt.Sprint("a", i), 0.2, y, 0.3, y)
		f.SourceAPI = "arcgis"
		arcgis = append(arcgis, f)
	}
	partial := func(context.Context, *http.Client, [4]float64) (roadsFetch, error) {
		return roadsFetch{features: arcgis, sources: []string{"overpass", "arcgis"}, succeeded: []string{"arcgis"},
			attempts: []db.IngestAttempt{
				{Input: "roads", Source: "overpass", Outcome: db.IngestFailed, Error: "http2: timeout awaiting response headers"},
				{Input: "roads", Source: "arcgis", Outcome: db.IngestOK, FeatureCount: len(arcgis)},
			}}, nil
	}
	water := func(context.Context, *http.Client, [4]float64, ingestpkg.LandProbes) (string, error) {
		t.Error("water fetched without the OSM roads")
		return "", nil
	}
	store := &dbtest.MockStore{
		SaveBoundaryFunc: func(context.Context, string, string) error { t.Error("SaveBoundary called"); return nil },
		UpsertFeaturesFunc: func(context.Context, resource.Type, []db.Feature, []string) error {
			t.Error("UpsertFeatures called")
			return nil
		},
	}
	city := &config.CityConfig{Name: "Fort Worth, TX", Overpass: true, ArcGISURL: "https://example.test/query"}
	ios, _, _, errOut := iostreams.Test()
	_, b, err := resolveBoundary(context.Background(), &Options{IO: ios}, store, &http.Client{}, city,
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: water, roads: partial})
	if !errors.Is(err, ErrRoadsFetch) || b != nil {
		t.Fatalf("resolveBoundary = (%v, %v), want ErrRoadsFetch and no boundary", b, err)
	}
	var he *hint.ErrHint
	if !errors.As(err, &he) || !strings.Contains(he.Hint, "next ingest retries") {
		t.Errorf("err = %v, want the retry hint", err)
	}
	if strings.Contains(errOut.String(), "no road probes") {
		t.Errorf("stderr = %q; the interior-point fallback must not run", errOut)
	}
	got := map[string]db.IngestOutcome{}
	for _, a := range store.Attempts {
		got[a.Input+"/"+a.Source] = a.Outcome
	}
	if got["roads/overpass"] != db.IngestFailed || got["roads/arcgis"] != db.IngestFailed || got["boundary/nominatim"] != db.IngestFailed {
		t.Errorf("attempts = %+v, want every roads source and the boundary recorded as failed", store.Attempts)
	}

	// An ArcGIS-only city has no OSM roads to miss: its ArcGIS roads are
	// its probes.
	var probes ingestpkg.LandProbes
	arcgisOnly := &config.CityConfig{Name: "Agency, CA", ArcGISURL: "https://example.test/query"}
	ok := func(context.Context, *http.Client, [4]float64) (roadsFetch, error) {
		return roadsFetch{features: arcgis, sources: []string{"arcgis"}, succeeded: []string{"arcgis"}}, nil
	}
	if _, _, err := resolveBoundary(context.Background(), resolveBoundaryHarness(t), &dbtest.MockStore{}, &http.Client{}, arcgisOnly,
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: recordingWater("", &probes), roads: ok}); err != nil {
		t.Fatalf("ArcGIS-only city: %v", err)
	}
	if !probes.Dense || len(probes.Points) != len(arcgis) {
		t.Errorf("ArcGIS-only probes = %+v, want its %d roads", probes, len(arcgis))
	}
}

// TestResolveBoundary_CanceledWaterStripRecordsNoRoads: a Ctrl-C during the
// water fetch records nothing for the roads that already answered.
func TestResolveBoundary_CanceledWaterStripRecordsNoRoads(t *testing.T) {
	canceled := func(context.Context, *http.Client, [4]float64, ingestpkg.LandProbes) (string, error) {
		return "", context.Canceled
	}
	store := &dbtest.MockStore{}
	_, _, err := resolveBoundary(context.Background(), resolveBoundaryHarness(t), store, &http.Client{},
		&config.CityConfig{Name: "Oakland, CA"},
		boundaryFetchers{byName: recordingNominatim(bigSquareBoundary, nil), water: canceled,
			roads: recordingRoads([]db.Feature{roadLine("a", 0.2, 0.2, 0.3, 0.3)}, nil)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(store.Attempts) != 0 {
		t.Errorf("attempts = %+v, want none for a canceled run", store.Attempts)
	}
}

// TestRunIngest_FacilityRunsCompanionDespiteBoundaryRoadsSaveFailure: in a
// services ingest on a fresh boundary, a failed save of the boundary's
// roads still fails the run, but only after the routing-roads companion,
// which does not depend on those roads, has been fetched.
func TestRunIngest_FacilityRunsCompanionDespiteBoundaryRoadsSaveFailure(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	city := cmdtest.NewTestCity()
	city.Overpass = true
	up := &fakeUpstream{boundary: noSourceBoundary, roadsBody: oneRoadInNoSourceBoundary}
	inner := up.client().Transport
	routing := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			if form, _ := url.ParseQuery(string(raw)); strings.Contains(form.Get("data"), "living_street") {
				routing++
			}
			r.Body = io.NopCloser(strings.NewReader(string(raw)))
		}
		return inner.RoundTrip(r)
	})}
	var saved, upserts []string
	store := boundaryStore(&saved, &upserts)
	store.UpsertFeaturesFunc = func(_ context.Context, rt resource.Type, _ []db.Feature, _ []string) error {
		if rt == resource.TypeRoads {
			return errors.New("disk full")
		}
		return nil
	}
	err := runIngest(context.Background(), &Options{
		IO:           ios,
		CityDB:       func() (db.Store, error) { return store, nil },
		CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
		HttpClient:   func() (*http.Client, error) { return client, nil },
		ResourceType: &resource.Facility{Service: "fire", Selectors: []string{`["amenity"="fire_station"]`}},
		Source:       cmdutil.SourceAll,
	})
	if routing != 1 {
		t.Errorf("routing-roads queries = %d, want 1: the companion must run", routing)
	}
	if err == nil || errors.Is(err, cmdutil.ErrNoResults) || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("services ingest = %v, want the roads save failure", err)
	}
}

// TestIngest_DisabledSourceIsAUsageErrorUpFront: a --source the config
// switches off for any selected city is refused as a usage error (exit 2)
// before any city runs, so it cannot hide another city's network failure.
func TestIngest_DisabledSourceIsAUsageErrorUpFront(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	cfg := &config.Config{Cities: []config.CityConfig{
		{Name: "Alpha, CA", Overpass: true, ArcGISURL: "https://example.test/query"},
		{Name: "Beta, CA", Overpass: true},
	}}
	f := &cmdutil.Factory{
		IOStreams:   ios,
		Config:      func() (*config.Config, error) { return cfg, nil },
		CurrentCity: func() (*config.CityConfig, error) { return &cfg.Cities[0], nil },
		CityFlagSet: func() bool { return false },
		CityDB:      func() (db.Store, error) { t.Fatal("a city ran"); return nil, errors.New("unreachable") },
		HttpClient:  func() (*http.Client, error) { t.Fatal("a city ran"); return nil, errors.New("unreachable") },
	}
	cmd := NewCmdIngest(f, &resource.Pavement{}, nil)
	cmd.SetArgs([]string{"--source", "arcgis"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	var fe *cmdutil.FlagError
	if !errors.As(err, &fe) || !strings.Contains(err.Error(), "not available for Beta, CA") {
		t.Errorf("err = %v, want a usage error naming Beta, CA", err)
	}
}

// TestRunResourceForCity_DryRunContext: steps run under WithDryRun (as
// `pvmt all ingest --dry-run` runs them) print their plans on stdout without
// building an HTTP client, and the city's boundary plan appears once.
func TestRunResourceForCity_DryRunContext(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	city := cmdtest.NewTestCity()
	city.Overpass = true
	store := &dbtest.MockStore{GetBoundaryFunc: func(context.Context) (string, error) { return "", nil }}
	f := &cmdutil.Factory{
		IOStreams:   ios,
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return city, nil },
		HttpClient: func() (*http.Client, error) {
			t.Error("a dry run built an HTTP client")
			return &http.Client{Transport: failTransport{t}}, nil
		},
	}
	ctx := WithDryRun(context.Background())
	if !IsDryRun(ctx) || IsDryRun(context.Background()) {
		t.Fatal("WithDryRun does not mark the context")
	}
	for _, rt := range []resource.Source{&resource.Pavement{}, &resource.Parking{}} {
		if err := RunResourceForCity(ctx, f, rt); err != nil {
			t.Fatalf("dry run %s: %v", rt.Type(), err)
		}
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "[dry-run] ingest roads for Test City") || !strings.Contains(out, "[dry-run] ingest parking for Test City") {
		t.Errorf("stdout = %q, want the roads and parking plans", out)
	}
	if n := strings.Count(out, "would fetch boundary"); n != 1 {
		t.Errorf("boundary plan printed %d times, want once:\n%s", n, out)
	}
}

// failTransport fails the test on any request.
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("dry run sent %s %s", r.Method, r.URL)
	return nil, errors.New("no requests in a dry run")
}

// TestFetchFromSources_ArcGISSkipsNonRoads: ArcGIS serves only roads, so a
// parking ingest says it skipped it instead of reporting a fetch that came
// back empty, and still records its attempt.
func TestFetchFromSources_ArcGISSkipsNonRoads(t *testing.T) {
	ios, _, _, stderr := iostreams.Test()
	opts := &Options{IO: ios, ResourceType: &resource.Parking{}}
	src := &ingestpkg.ArcGISSource{URL: "https://example.test/query"}
	_, _, attempts, err := fetchFromSources(context.Background(), []ingestpkg.Source{src}, &http.Client{Transport: failTransport{t}}, opts, "Test City")
	if err != nil {
		t.Fatal(err)
	}
	out := stderr.String()
	if !strings.Contains(out, "Skipping arcgis for parking: it serves only roads") || strings.Contains(out, "Got ") || strings.Contains(out, "Fetching") {
		t.Errorf("stderr = %q, want only the skip line", out)
	}
	if len(attempts) != 1 || attempts[0].Outcome != db.IngestEmpty {
		t.Errorf("attempts = %+v, want one empty arcgis attempt", attempts)
	}
}

// TestFmtArcgis_RedactsToken: the dry-run plan names the ArcGIS layer
// without the arcgis_url's access token.
func TestFmtArcgis_RedactsToken(t *testing.T) {
	got := fmtArcgis("https://gis.example/FeatureServer/0/query?token=s3cr3t", resource.ByType(resource.TypeRoads))
	if strings.Contains(got, "s3cr3t") || !strings.Contains(got, "token=REDACTED") {
		t.Errorf("fmtArcgis = %q, want the token redacted", got)
	}
}
