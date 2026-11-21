package cache

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mirror is an upstream that answers every POST and counts the calls.
func mirror(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func post(t *testing.T, ctx context.Context, ct *CachingTransport, url, body string) {
	t.Helper()
	resp, err := (&http.Client{Transport: ct}).Do(postRequest(ctx, url, body))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
}

// fill caches body under url through a transport of its own, as an earlier
// run against that endpoint would have.
func fill(t *testing.T, dir, url string, bodies ...string) {
	t.Helper()
	ct := NewTransport(http.DefaultTransport, dir, time.Hour)
	for _, b := range bodies {
		post(t, context.Background(), ct, url, b)
	}
}

func TestCachingTransport_WarnsWhenTheMissIsCachedUnderAnotherEndpoint(t *testing.T) {
	cached, _ := mirror(t)
	live, liveCalls := mirror(t)
	dir := t.TempDir()
	fill(t, dir, cached.URL+"/api/interpreter", "data=a", "data=b")

	var warnings strings.Builder
	ct := NewTransport(http.DefaultTransport, dir, time.Hour)
	ct.Warn = &warnings
	post(t, context.Background(), ct, live.URL+"/api/interpreter", "data=a")

	if *liveCalls != 1 {
		t.Fatalf("the miss must still be fetched live; upstream saw %d calls", *liveCalls)
	}
	got := warnings.String()
	for _, want := range []string{
		"warning: fetching from " + live.URL + "/api/interpreter live",
		"the cache holds these requests under " + cached.URL + "/api/interpreter",
		"hint: point this source at " + cached.URL + "/api/interpreter",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q lacks %q", got, want)
		}
	}

	// The next miss for the same pair of endpoints is not reported again.
	post(t, context.Background(), ct, live.URL+"/api/interpreter", "data=b")
	if n := strings.Count(warnings.String(), "warning: "); n != 1 {
		t.Errorf("want one warning per pair of endpoints, got %d:\n%s", n, warnings.String())
	}
}

func TestCachingTransport_NoElsewhereWarning(t *testing.T) {
	cases := map[string]struct {
		ctx  context.Context
		body string
	}{
		// The request is cached under no endpoint: a plain miss.
		"nothing cached for this body": {context.Background(), "data=uncached"},
		// --force asked for a live fetch, so there is nothing to point out.
		"bypass": {WithBypass(context.Background()), "data=a"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cached, _ := mirror(t)
			live, _ := mirror(t)
			dir := t.TempDir()
			fill(t, dir, cached.URL+"/api/interpreter", "data=a")

			var warnings strings.Builder
			ct := NewTransport(http.DefaultTransport, dir, time.Hour)
			ct.Warn = &warnings
			post(t, tc.ctx, ct, live.URL+"/api/interpreter", tc.body)
			if warnings.Len() != 0 {
				t.Errorf("unexpected warning: %s", warnings.String())
			}
		})
	}
}

func TestCachingTransport_StaleEntryElsewhereIsNotReported(t *testing.T) {
	cached, _ := mirror(t)
	live, _ := mirror(t)
	dir := t.TempDir()
	fill(t, dir, cached.URL+"/api/interpreter", "data=a")

	var warnings strings.Builder
	ct := NewTransport(http.DefaultTransport, dir, time.Nanosecond)
	ct.Warn = &warnings
	time.Sleep(time.Millisecond)
	post(t, context.Background(), ct, live.URL+"/api/interpreter", "data=a")
	if warnings.Len() != 0 {
		t.Errorf("an expired entry would be re-fetched under either endpoint; got %s", warnings.String())
	}
}
