package export

import (
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func TestNewCmdExport_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdExport(f, func(opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{"-o", "/tmp/site", "--clean"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts == nil {
		t.Fatal("runF was not invoked")
	}
	if gotOpts.OutputDir != "/tmp/site" {
		t.Errorf("expected OutputDir /tmp/site, got %q", gotOpts.OutputDir)
	}
	if !gotOpts.Clean {
		t.Errorf("expected --clean to be true")
	}
}

func TestNewCmdExport_Defaults(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdExport(f, func(opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts.OutputDir != "dist" {
		t.Errorf("expected default OutputDir dist, got %q", gotOpts.OutputDir)
	}
	if gotOpts.Clean {
		t.Errorf("expected default --clean to be false")
	}
}
