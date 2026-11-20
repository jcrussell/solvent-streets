package serve

import (
	"context"
	"errors"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	exportpkg "github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/export/exporttest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestRun_UnknownSnapshotIsAUsageError: a --snapshot name no snapshot has is
// refused with the shared unknown-snapshot usage error (exit 2), before
// anything is pinned or written.
func TestRun_UnknownSnapshotIsAUsageError(t *testing.T) {
	root, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	ios, _, _, _ := iostreams.Test()
	cfg := &config.Config{ConfigID: "test", Cities: []config.CityConfig{{Name: "Alpha, CA"}}}
	opts := &Options{
		IO:       ios,
		RootDB:   func() (*db.RootStore, error) { return root, nil },
		Config:   func() (*config.Config, error) { return cfg, nil },
		Cities:   func() ([]config.CityConfig, error) { return cfg.Cities, nil },
		Assets:   func() (*exportpkg.Assets, error) { return exportpkg.NewAssets(exporttest.AssetFiles()) },
		Snapshot: "nope",
	}
	snapshotTestSetUp(t, opts)
	var fe *cmdutil.FlagError
	if err := snapshotTestRun(context.Background(), opts); !errors.As(err, &fe) || err.Error() != `no snapshot named "nope"` {
		t.Errorf("err = %v, want the unknown-snapshot usage error", err)
	}
}

// snapshotTestSetUp gives the run a valid --port.
func snapshotTestSetUp(t *testing.T, opts *Options) {
	t.Helper()
	opts.Port = 8080
}

var snapshotTestRun = runServe
