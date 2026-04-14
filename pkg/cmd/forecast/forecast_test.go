package forecast

import (
	"testing"

	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"
)

func TestNewCmdForecast_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdForecast(f, func(opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{"--scenarios=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts == nil {
		t.Fatal("runF was not invoked")
	}
	if gotOpts.IO != ios {
		t.Errorf("opts.IO not propagated from factory")
	}
	if gotOpts.Scenarios {
		t.Errorf("expected --scenarios=false to set Scenarios to false")
	}
}

func TestNewCmdForecast_DefaultScenariosTrue(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdForecast(f, func(opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gotOpts.Scenarios {
		t.Errorf("expected default Scenarios to be true")
	}
}

func TestNewCmdForecast_JqAndTemplateMutuallyExclusive(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	cmd := NewCmdForecast(f, func(opts *Options) error { return nil })
	cmd.SetArgs([]string{"--json", "year", "--jq", ".", "--template", "{{.}}"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --jq and --template both set")
	}
}
