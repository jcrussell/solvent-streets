package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func gridTestServer(t *testing.T) (*Server, export.CityEntry) {
	t.Helper()
	boundary := `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}}
	entry := export.CityEntry{
		Config: cfg,
		City:   cfg.Cities[0],
		Slug:   cfg.Cities[0].Slug(),
		Store: &dbtest.MockStore{
			GetBoundaryFunc: func(context.Context) (string, error) { return boundary, nil },
		},
	}
	ios, _, _, _ := iostreams.Test()
	return New([]export.CityEntry{entry}, "127.0.0.1", 0, ios, testAssets(t)), entry
}

// TestWithDrawnGrid_SharedPerSnapshotAndEvicted pins the drawn-grid cache:
// every build for one city and
// snapshot gets the same grid, so meta.json, hex-metrics.json and
// hexgrid.geojson clip the lattice once between them; another snapshot gets
// its own; and a build that fails or panics drops the grid, as every cache
// in the server evicts errors, so a grid that kept a transient error is not
// reused.
func TestWithDrawnGrid_SharedPerSnapshotAndEvicted(t *testing.T) {
	srv, entry := gridTestServer(t)
	grab := func(snapshotID int64, err error) *export.DrawnGrid {
		t.Helper()
		var got *export.DrawnGrid
		_, gotErr := srv.withDrawnGrid(entry, snapshotID, func(g *export.DrawnGrid) (any, error) {
			got = g
			return nil, err
		})
		if !errors.Is(gotErr, err) {
			t.Fatalf("withDrawnGrid error = %v, want %v", gotErr, err)
		}
		return got
	}

	first := grab(0, nil)
	if again := grab(0, nil); again != first {
		t.Error("a second build for the same snapshot got a different grid; want it shared")
	}
	if other := grab(7, nil); other == first {
		t.Error("another snapshot got the pinned view's grid; want its own")
	}

	boom := errors.New("transient")
	if failed := grab(0, boom); failed != first {
		t.Fatal("the failing build did not run on the cached grid")
	}
	afterErr := grab(0, nil)
	if afterErr == first {
		t.Error("the grid a failed build ran on is still cached; want it dropped")
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not propagate")
			}
		}()
		_, _ = srv.withDrawnGrid(entry, 0, func(*export.DrawnGrid) (any, error) { panic("kaboom") })
	}()
	if grab(0, nil) == afterErr {
		t.Error("the grid a panicking build ran on is still cached; want it dropped")
	}
}

// TestDataFiles_ShareOneDrawnGrid: the three files that describe the drawn
// grid, and the page, all build on the one cached grid of their snapshot.
func TestDataFiles_ShareOneDrawnGrid(t *testing.T) {
	srv, entry := gridTestServer(t)
	for _, file := range []string{export.MetaFileName, export.HexMetricsFileName, export.HexGridFileName} {
		w := httptest.NewRecorder()
		srv.dataFiles()[file](w, nil, entry, 0)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", file, w.Code, w.Body.String())
		}
	}
	if _, err := srv.buildIndexData(context.Background(), entry); err != nil {
		t.Fatalf("buildIndexData: %v", err)
	}
	n := 0
	srv.grids.Range(func(key, _ any) bool {
		n++
		if key != cacheKey("grid", entry.Slug, 0) {
			t.Errorf("unexpected grid key %v", key)
		}
		return true
	})
	if n != 1 {
		t.Errorf("%d grids cached, want the one the files and the page share", n)
	}
}
