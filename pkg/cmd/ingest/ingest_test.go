package ingest

import (
	"errors"
	"net/http"
	"testing"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/db/dbtest"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"
)

var testCfg = &config.Config{
	Project: config.ProjectConfig{Name: "Test City"},
	Area:    config.AreaConfig{BBox: [4]float64{37.64, -121.84, 37.72, -121.68}},
	Sources: config.SourcesConfig{Overpass: true},
}

func TestNewCmdIngest_DefaultFlags(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
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
	f := &cmdutil.Factory{IOStreams: ios}
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
	f := &cmdutil.Factory{IOStreams: ios}
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

func TestNewCmdIngest_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	rt := &resource.Pavement{}

	called := false
	cmd := NewCmdIngest(f, rt, func(opts *Options) error {
		called = true
		if opts.ResourceType.Name() != "roads" {
			t.Errorf("expected pavements, got %s", opts.ResourceType.Name())
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

func TestNewCmdIngest_InvalidSource(t *testing.T) {
	store := &dbtest.MockStore{}
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		HttpClient: func() (*http.Client, error) {
			return &http.Client{}, nil
		},
		DB: func() (db.Store, error) {
			return store, nil
		},
		Config: func() (*config.Config, error) {
			return testCfg, nil
		},
	}
	rt := &resource.Pavement{}

	cmd := NewCmdIngest(f, rt, nil)
	cmd.SetArgs([]string{"--source=bogus"})
	// Cobra wraps RunE errors, so we need SilenceErrors
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for bogus source")
	}
	var flagErr *cmdutil.FlagError
	if !errors.As(err, &flagErr) {
		t.Errorf("expected FlagError, got %T: %v", err, err)
	}
}
