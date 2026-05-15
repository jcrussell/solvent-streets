package serve

import (
	"errors"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
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

// TestOptions_Validate_Port covers byob-input-validation.5: bad --port
// surfaces as FlagError before any listener bind or DB open. Port 0 is
// rejected even though net/http accepts it (means "pick free") so the
// served address stays predictable.
func TestOptions_Validate_Port(t *testing.T) {
	cases := map[string]int{
		"zero":         0,
		"negative":     -1,
		"above range":  65536,
		"way too high": 99999,
	}
	for name, port := range cases {
		t.Run(name, func(t *testing.T) {
			opts := &Options{Port: port}
			err := opts.Validate()
			if err == nil {
				t.Fatalf("expected FlagError for port=%d, got nil", port)
			}
			var flag *cmdutil.FlagError
			if !errors.As(err, &flag) {
				t.Errorf("error %v is not *FlagError", err)
			}
		})
	}
	for name, port := range map[string]int{
		"low end":  1,
		"default":  8080,
		"high end": 65535,
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			opts := &Options{Port: port}
			if err := opts.Validate(); err != nil {
				t.Errorf("port=%d unexpectedly rejected: %v", port, err)
			}
		})
	}
}
