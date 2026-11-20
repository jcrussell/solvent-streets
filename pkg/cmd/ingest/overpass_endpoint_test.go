package ingest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
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

const customOverpass = "https://overpass.example.org/api/interpreter"

// overpassRecorder answers every request with an empty Overpass result and
// records each request's URL.
type overpassRecorder struct {
	mu   sync.Mutex
	urls []string
}

func (r *overpassRecorder) client() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		r.mu.Lock()
		r.urls = append(r.urls, req.URL.String())
		r.mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"elements":[]}`)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
}

func (r *overpassRecorder) assertAll(t *testing.T, want string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.urls) == 0 {
		t.Fatal("no request sent")
	}
	for _, u := range r.urls {
		if u != want {
			t.Errorf("request went to %q; want %q", u, want)
		}
	}
}

// TestLiveBoundaryFetchers_UseConfiguredOverpass pins that the boundary
// step's Overpass fetches (a boundary relation, the water strip and the roads
// fetched as its land evidence) go to the run's configured endpoint, and to
// ingest.DefaultOverpassURL when none is configured.
func TestLiveBoundaryFetchers_UseConfiguredOverpass(t *testing.T) {
	ctx := context.Background()
	city := cmdtest.NewTestCity()
	city.Overpass = true
	bbox := [4]float64{37.64, -121.84, 37.72, -121.68}
	for endpoint, want := range map[string]string{customOverpass: customOverpass, "": ingestpkg.DefaultOverpassURL} {
		ios, _, _, _ := iostreams.Test()
		fetch := liveBoundaryFetchers(&Options{IO: ios, overpassURL: endpoint}, city)
		steps := map[string]func(*http.Client){
			"relation": func(c *http.Client) { _, _ = fetch.byRelation(ctx, c, 2833530) },
			"water":    func(c *http.Client) { _, _ = fetch.water(ctx, c, bbox, ingestpkg.LandProbes{}) },
			"roads":    func(c *http.Client) { _, _ = fetch.roads(ctx, c, bbox) },
		}
		for name, step := range steps {
			t.Run(name+"/"+want, func(t *testing.T) {
				var rec overpassRecorder
				step(rec.client())
				rec.assertAll(t, want)
			})
		}
	}
}

// TestRunIngest_UsesConfiguredOverpass pins that a resource's own fetch goes
// to the endpoint Options.OverpassURL resolves, and that an endpoint it
// refuses (the SSRF guard) stops the run before any request is sent.
func TestRunIngest_UsesConfiguredOverpass(t *testing.T) {
	ctx := context.Background()
	city := cmdtest.NewTestCity()
	city.Overpass = true
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return noSourceBoundary, nil },
	}
	run := func(rec *overpassRecorder, overpassURL func() (string, error)) error {
		ios, _, _, _ := iostreams.Test()
		client := rec.client()
		return runIngest(ctx, &Options{
			IO:           ios,
			CityDB:       func() (db.Store, error) { return store, nil },
			CurrentCity:  func() (*config.CityConfig, error) { return city, nil },
			HttpClient:   func() (*http.Client, error) { return client, nil },
			OverpassURL:  overpassURL,
			ResourceType: resource.ByType(resource.TypeParking),
			Source:       cmdutil.SourceAll,
		})
	}

	t.Run("configured", func(t *testing.T) {
		var rec overpassRecorder
		err := run(&rec, func() (string, error) { return customOverpass, nil })
		if err != nil && !errors.Is(err, cmdutil.ErrNoResults) {
			t.Fatalf("runIngest: %v", err)
		}
		rec.assertAll(t, customOverpass)
	})
	t.Run("refused", func(t *testing.T) {
		var rec overpassRecorder
		guardErr := errors.New("resolves to a private address")
		err := run(&rec, func() (string, error) { return "", guardErr })
		if !errors.Is(err, guardErr) {
			t.Fatalf("runIngest = %v; want the guard's error", err)
		}
		if len(rec.urls) != 0 {
			t.Errorf("sent %v before the endpoint was validated", rec.urls)
		}
	})
}

// defaultOverpassWater is the live water fetch against the default endpoint,
// for tests that serve its responses from a fake client.
func defaultOverpassWater(ctx context.Context, client *http.Client, bbox [4]float64, probes ingestpkg.LandProbes) (string, error) {
	return ingestpkg.FetchOSMWater(ctx, client, "", bbox, probes)
}
