package ingest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	ingestpkg "github.com/jcrussell/solvent-streets/internal/ingest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

var testCity = &config.CityConfig{
	Name:     "Test City",
	Overpass: true,
}

var testCfg = &config.Config{
	Cities: []config.CityConfig{*testCity},
}

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
	cmd := NewCmdIngest(f, rt, func(opts *Options) error {
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
	cmd := NewCmdIngest(f, rt, func(opts *Options) error {
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
	cmd := NewCmdIngest(f, rt, func(opts *Options) error {
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

// TestNewCmdIngest_ForceAndDryRunMutuallyExclusive covers byob-command-shape.6:
// --force (bypass HTTP cache) and --dry-run (no fetch) are nonsense together,
// so cobra's MarkFlagsMutuallyExclusive helper rejects the combination at
// flag-parse time rather than each command silently ignoring one.
func TestNewCmdIngest_ForceAndDryRunMutuallyExclusive(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := testFactory(ios)
	rt := &resource.Pavement{}

	cmd := NewCmdIngest(f, rt, func(opts *Options) error { return nil })
	cmd.SetArgs([]string{"--force", "--dry-run"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected --force and --dry-run to be mutually exclusive")
	}
}

func TestNewCmdIngest_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := testFactory(ios)
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdIngest(f, rt, func(opts *Options) error {
		called = true
		if opts.ResourceType.Kind() != resource.KindRoads {
			t.Errorf("expected KindRoads, got %v", opts.ResourceType.Kind())
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
	_, err := fetchFromSources(context.Background(), sources, &http.Client{}, opts, "Test City")
	if !errors.Is(err, cmdutil.ErrAllSourcesFailed) {
		t.Errorf("expected ErrAllSourcesFailed, got %v", err)
	}
}

func TestFetchFromSources_PartialSuccessNoError(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	opts := &Options{IO: ios, ResourceType: &resource.Pavement{}}
	sources := []ingestpkg.Source{&failingSource{name: "a"}, &emptySource{name: "b"}}
	features, err := fetchFromSources(context.Background(), sources, &http.Client{}, opts, "Test City")
	if err != nil {
		t.Errorf("expected nil error when at least one source returns cleanly, got %v", err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
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
	var flagErr *cmdutil.FlagError
	if !errors.As(err, &flagErr) {
		t.Errorf("expected FlagError through pflag wrapping, got %T: %v", err, err)
	}
}
