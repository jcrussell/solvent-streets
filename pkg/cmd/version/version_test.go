package version

import (
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func TestNewCmdVersion_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	called := false
	cmd := NewCmdVersion(f, func(opts *Options) error {
		called = true
		if opts.IO != ios {
			t.Errorf("opts.IO not propagated from factory")
		}
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Errorf("runF was not invoked")
	}
}

// TestNewCmdVersion_DefaultPrintsVersion locks in byob-iostreams.3 routing
// for a pure-data command: the version string is the command's DATA and
// must land on Out alone. ErrOut stays silent so `pvmt version | cat`
// captures the string with no chatter contamination.
func TestNewCmdVersion_DefaultPrintsVersion(t *testing.T) {
	ios, _, stdout, stderr := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	cmd := NewCmdVersion(f, nil)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "pvmt ") {
		t.Errorf("expected output to start with 'pvmt ', got: %q", out)
	}
	if got := stderr.String(); got != "" {
		t.Errorf("stderr should be empty for pure-data command (byob-iostreams.3); got: %q", got)
	}
}
