package serve

import (
	"testing"

	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"
)

func TestNewCmdServe_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdServe(f, func(opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{"--port", "9999"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts == nil {
		t.Fatal("runF was not invoked")
	}
	if gotOpts.IO != ios {
		t.Errorf("opts.IO not propagated from factory")
	}
	if gotOpts.Port != 9999 {
		t.Errorf("expected --port 9999, got %d", gotOpts.Port)
	}
}

func TestNewCmdServe_DefaultPort(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdServe(f, func(opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", gotOpts.Port)
	}
}
