package compute

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmd/cmdtest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/peterstace/simplefeatures/geom"
)

var testCity = cmdtest.NewTestCity()

var testCfg = cmdtest.NewTestConfig(testCity)

var testBoundary = `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`

func TestNewCmdCompute_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	// Only IOStreams is needed: runF short-circuits before any other Factory field is accessed.
	f := &cmdutil.Factory{IOStreams: ios, UnitSystem: func() units.System { return units.Imperial }}
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdCompute(f, rt, func(_ context.Context, opts *Options) error {
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

func TestRunCompute_NoFeatures(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
	}
	ios, _, _, errBuf := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
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

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("expected ErrNoResults, got: %v", err)
	}
	if !strings.Contains(errBuf.String(), "ingest") {
		t.Errorf("stderr should suggest running ingest, got: %s", errBuf.String())
	}
}

func TestRunCompute_Success(t *testing.T) {
	var savedResult *db.ComputeResult
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{
				{
					ID:           "test1",
					ResourceType: resource.TypeRoads,
					Name:         "Test Rd",
					Tags:         map[string]string{"highway": "residential"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
				},
			}, nil
		},
		SaveComputeResultFunc: func(_ context.Context, r db.ComputeResult) error {
			savedResult = &r
			return nil
		},
	}
	ios, _, stdout, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
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

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "sq ft") {
		t.Errorf("expected area output with imperial units, got: %s", output)
	}
	if savedResult == nil {
		t.Error("expected SaveComputeResult to be called")
	}
}

// countingSource wraps a resource.Source to count buffer-and-index invocations
// so tests can assert the pipeline doesn't re-buffer the city subset or per
// classification.
type countingSource struct {
	inner       resource.Source
	bufferCalls int
	pairedCalls int
}

func (c *countingSource) Type() resource.Type               { return c.inner.Type() }
func (c *countingSource) OverpassQuery(b [4]float64) string { return c.inner.OverpassQuery(b) }
func (c *countingSource) HasCohorts() bool                  { return c.inner.HasCohorts() }
func (c *countingSource) BufferFeatures(f []resource.Feature, p *geo.UTMProjector) ([]geom.Geometry, error) {
	c.bufferCalls++
	return c.inner.BufferFeatures(f, p)
}
func (c *countingSource) BufferFeaturesPaired(f []resource.Feature, p *geo.UTMProjector) []resource.BufferedFeature {
	c.pairedCalls++
	return c.inner.BufferFeaturesPaired(f, p)
}

// TestRunCompute_BuffersFeaturesOncePerRun pins the solvent-streets-2nc fix:
// across the all-jurisdiction pass, the city-only pass, and the per-class
// cohort breakdown, every feature is buffered exactly once. Regression
// guard against re-introducing a second BufferFeatures pass on the city
// subset.
func TestRunCompute_BuffersFeaturesOncePerRun(t *testing.T) {
	store := &dbtest.MockStore{
		GetBoundaryFunc: func(_ context.Context) (string, error) { return testBoundary, nil },
		ListFeaturesFunc: func(_ context.Context, _ resource.Type) ([]db.Feature, error) {
			return []db.Feature{
				{
					ID:           "city1",
					ResourceType: resource.TypeRoads,
					Name:         "City Rd",
					Tags:         map[string]string{"highway": "residential"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
				},
				{
					ID:           "city2",
					ResourceType: resource.TypeRoads,
					Tags:         map[string]string{"highway": "tertiary"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6820],[-121.7690,37.6830]]}`,
				},
				{
					ID:           "state1",
					ResourceType: resource.TypeRoads,
					Tags:         map[string]string{"highway": "trunk"},
					GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6840],[-121.7690,37.6850]]}`,
				},
			}, nil
		},
	}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:   ios,
		UnitSystem:  func() units.System { return units.Metric },
		CityDB:      func() (db.Store, error) { return store, nil },
		CurrentCity: func() (*config.CityConfig, error) { return testCity, nil },
		Config:      func() (*config.Config, error) { return testCfg, nil },
	}

	counter := &countingSource{inner: &resource.Pavement{}}
	cmd := NewCmdCompute(f, counter, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("compute: %v", err)
	}

	if counter.pairedCalls != 1 {
		t.Errorf("BufferFeaturesPaired called %d times; want 1 (all jurisdictions, city subset, and per-class cohorts must share the buffered slice)",
			counter.pairedCalls)
	}
	if counter.bufferCalls != 0 {
		t.Errorf("BufferFeatures called %d times; legacy path should be unused by the compute pipeline", counter.bufferCalls)
	}
}

func TestRunCompute_DBError(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
		CurrentCity: func() (*config.CityConfig, error) {
			return testCity, nil
		},
		CityDB: func() (db.Store, error) {
			return nil, fmt.Errorf("db connection failed")
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdCompute(f, rt, nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for DB failure")
	}
	if !strings.Contains(err.Error(), "database") {
		t.Errorf("expected database error, got: %s", err.Error())
	}
}
