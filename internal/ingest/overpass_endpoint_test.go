package ingest

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// recordingClient answers every request with an empty Overpass result and
// records each request's URL.
func recordingClient() (*http.Client, func() []string) {
	var mu sync.Mutex
	var urls []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		urls = append(urls, r.URL.String())
		mu.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"elements":[]}`)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
	return client, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), urls...)
	}
}

// TestOverpassEndpoint_ThreadedToEveryCaller pins solvent-streets-ern4.18:
// every Overpass caller (the OverpassSource that SourcesFor and AllSources
// build for roads, parking, sidewalks and facilities, the OSM water fetch and
// the boundary-relation fetch) queries the configured endpoint, and an empty
// endpoint queries DefaultOverpassURL exactly, so default runs keep their
// cache keys.
func TestOverpassEndpoint_ThreadedToEveryCaller(t *testing.T) {
	const custom = "https://overpass.example.org/api/interpreter"
	ctx := context.Background()
	callers := map[string]func(client *http.Client, endpoint string){
		"AllSources roads": func(client *http.Client, endpoint string) {
			for _, src := range AllSources(testBBox, true, "", Options{OverpassURL: endpoint}) {
				_, _ = src.Fetch(ctx, client, resource.ByType(resource.TypeRoads))
			}
		},
		"SourcesFor parking": func(client *http.Client, endpoint string) {
			for _, src := range SourcesFor(resource.ByType(resource.TypeParking), testBBox, true, "", Options{OverpassURL: endpoint}) {
				_, _ = src.Fetch(ctx, client, resource.ByType(resource.TypeParking))
			}
		},
		"SourcesFor facilities": func(client *http.Client, endpoint string) {
			rt := &resource.Facility{Service: "fire"}
			for _, src := range SourcesFor(rt, testBBox, false, "", Options{OverpassURL: endpoint}) {
				_, _ = src.Fetch(ctx, client, rt)
			}
		},
		"SourceByName sidewalks": func(client *http.Client, endpoint string) {
			src, err := SourceByName(resource.SourceOverpass, testBBox, true, "", Options{OverpassURL: endpoint})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = src.Fetch(ctx, client, resource.ByType(resource.TypeSidewalks))
		},
		"FetchOSMWater": func(client *http.Client, endpoint string) {
			_, _ = FetchOSMWater(ctx, client, endpoint, testBBox, LandProbes{})
		},
		"FetchCityBoundaryFromRelation": func(client *http.Client, endpoint string) {
			_, _ = FetchCityBoundaryFromRelation(ctx, client, endpoint, 2833530)
		},
	}
	for name, call := range callers {
		for endpoint, want := range map[string]string{custom: custom, "": DefaultOverpassURL} {
			t.Run(name+"/"+want, func(t *testing.T) {
				client, urls := recordingClient()
				call(client, endpoint)
				got := urls()
				if len(got) == 0 {
					t.Fatal("no request sent")
				}
				for _, u := range got {
					if u != want {
						t.Errorf("request went to %q; want %q", u, want)
					}
				}
			})
		}
	}
}
