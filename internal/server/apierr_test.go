package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestAPIErrors_AreJSON: an /api error comes back as {"error": ...} with a
// JSON content type, whatever wrote it (an unknown slug's or path's 404, a
// method the API does not take, a failed build's 500, a panic), while a
// /data error keeps the plain-text body.
func TestAPIErrors_AreJSON(t *testing.T) {
	failing := &dbtest.MockStore{
		ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) { return nil, errors.New("disk on fire") },
	}
	panicking := &dbtest.MockStore{
		ListSnapshotsFunc: func(context.Context) ([]db.SnapshotCity, error) { panic("boom") },
	}
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "City A"}, {Name: "City B"}, {Name: "City C"}}}
	entries := []export.CityEntry{
		{Config: cfg, City: cfg.Cities[0], Store: failing, Slug: cfg.Cities[0].Slug()},
		{Config: cfg, City: cfg.Cities[1], Store: &dbtest.MockStore{}, Slug: cfg.Cities[1].Slug()},
		{Config: cfg, City: cfg.Cities[2], Store: panicking, Slug: cfg.Cities[2].Slug()},
	}
	ios, _, _, _ := iostreams.Test()
	h := New(entries, "127.0.0.1", 0, ios, testAssets(t)).handler()

	for _, tc := range []struct {
		method, path string
		code         int
		msg          string
	}{
		{http.MethodGet, "/api/cities/no-such-city/snapshots", http.StatusNotFound, "404 page not found"},
		{http.MethodGet, "/api/nope", http.StatusNotFound, "404 page not found"},
		{http.MethodPost, "/api/cities", http.StatusMethodNotAllowed, "Method Not Allowed"},
		{http.MethodGet, "/api/cities/" + entries[0].Slug + "/snapshots", http.StatusInternalServerError, "Internal Server Error"},
		{http.MethodGet, "/api/cities/" + entries[2].Slug + "/snapshots", http.StatusInternalServerError, "Internal Server Error"},
	} {
		req := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, w.Code, tc.code)
		}
		if tc.code == http.StatusMethodNotAllowed && w.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s %s: Allow %q, want GET, HEAD", tc.method, tc.path, w.Header().Get("Allow"))
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type %q, want application/json", tc.path, ct)
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] != tc.msg {
			t.Errorf("%s: body %q (%v), want {\"error\": %q}", tc.path, w.Body.String(), err, tc.msg)
		}
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/cities/no-such-city/data/meta.json", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || strings.HasPrefix(w.Body.String(), "{") {
		t.Errorf("/data 404 = %d %q, want the plain-text 404", w.Code, w.Body.String())
	}
}
