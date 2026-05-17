package export

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// TestExportScenariosForCity_PropagatesDBError pins the deployment-correctness
// contract: when LatestComputeResult fails with anything other than
// sql.ErrNoRows, exportScenariosForCity surfaces the error and writes nothing
// — so a transient DB failure aborts the static export rather than emitting
// partial output that would mislead the downstream dashboard.
//
// Mirrors internal/server/handlers_test.go TestBuildForecasts_DBErrorEvicts for
// the export-command path. Regression caught: reverting buildResourceForecast
// to silent-swallow the underlying error (the pre-bkz behavior) breaks
// errors.Is and lets exportScenariosForCity return nil with no files.
func TestExportScenariosForCity_PropagatesDBError(t *testing.T) {
	sentinel := errors.New("db unavailable")
	store := &dbtest.MockStore{
		LatestComputeResultFunc: func(_ context.Context, _ resource.ResourceType) (*db.ComputeResult, error) {
			return nil, sentinel
		},
	}
	entry := CityEntry{
		Config: &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}},
		City:   config.CityConfig{Name: "Test City"},
		Store:  store,
		Slug:   "test-city",
	}
	dataDir := t.TempDir()

	err := exportScenariosForCity(t.Context(), entry, dataDir)
	if err == nil {
		t.Fatal("expected error from exportScenariosForCity, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(err, sentinel) = false; err = %v", err)
	}

	for _, name := range []string{"forecast.json", "hex-cost-summary.json", "scenarios.json"} {
		if _, statErr := os.Stat(filepath.Join(dataDir, name)); !os.IsNotExist(statErr) {
			t.Errorf("expected %s not to exist on error path; stat = %v", name, statErr)
		}
	}
}
