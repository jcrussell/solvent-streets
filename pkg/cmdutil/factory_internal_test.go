package cmdutil

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
)

// TestStoreCache_BuildsOncePerKey pins the memoization primitive behind finding
// xygq: get runs the build func at most once per key, so a second CityDB() call
// (which routes through this cache) never re-runs EnsureCity. Distinct keys get
// distinct builds, and a build error is not cached (retryable).
func TestStoreCache_BuildsOncePerKey(t *testing.T) {
	c := newStoreCache()

	t.Run("same key builds once", func(t *testing.T) {
		var builds int
		build := func() (db.Store, error) {
			builds++
			return &dbtest.MockStore{}, nil
		}
		s1, err := c.get("k", build)
		if err != nil {
			t.Fatal(err)
		}
		s2, err := c.get("k", build)
		if err != nil {
			t.Fatal(err)
		}
		if builds != 1 {
			t.Errorf("build ran %d times; want 1 (second get must hit the cache)", builds)
		}
		if s1 != s2 {
			t.Error("cache returned different stores for the same key")
		}
	})

	t.Run("errors are not cached", func(t *testing.T) {
		cc := newStoreCache()
		var builds int
		sentinel := errors.New("boom")
		if _, err := cc.get("k", func() (db.Store, error) { builds++; return nil, sentinel }); !errors.Is(err, sentinel) {
			t.Fatalf("want sentinel, got %v", err)
		}
		if _, err := cc.get("k", func() (db.Store, error) { builds++; return &dbtest.MockStore{}, nil }); err != nil {
			t.Fatalf("retry after error: %v", err)
		}
		if builds != 2 {
			t.Errorf("build ran %d times; want 2 (error must not be cached)", builds)
		}
	})
}

// TestWithCity_MemoizesStore pins the withCity end of finding xygq: repeated
// CityDB() calls on a city-scoped factory copy return the SAME store without
// re-running the EnsureCity + ForCity + WithConfigHash chain. Without the memo
// each call rebuilds a fresh *sqliteStore (WithConfigHash copies), so identity
// equality is the observable proof the cache short-circuited the rebuild.
func TestWithCity_MemoizesStore(t *testing.T) {
	root, err := db.Open(filepath.Join(t.TempDir(), "pvmt.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })

	cfg := &config.Config{ConfigID: "cfg1"}
	city := config.CityConfig{Name: "Oakland"}
	f := &Factory{
		RootDB: func() (*db.RootStore, error) { return root, nil },
		Config: func() (*config.Config, error) { return cfg, nil },
	}

	cf := withCity(context.Background(), f, &city)
	s1, err := cf.CityDB()
	if err != nil {
		t.Fatalf("CityDB (first): %v", err)
	}
	s2, err := cf.CityDB()
	if err != nil {
		t.Fatalf("CityDB (second): %v", err)
	}
	if s1 != s2 {
		t.Error("withCity CityDB returned different stores across calls; memoization is broken")
	}
}
