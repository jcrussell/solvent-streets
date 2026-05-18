package all

import (
	"testing"

	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestNewCmdAll_RegistersSubcommands pins the thin-wrapper contract:
// the factory returns a cobra.Command exposing the cross-resource
// ingest+compute fan-out. status is intentionally absent — `pvmt status`
// already covers every resource without a per-resource flag, so wiring
// `pvmt all status` would just duplicate it.
func TestNewCmdAll_RegistersSubcommands(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
	}

	cmd := NewCmdAll(f)
	if cmd == nil {
		t.Fatal("NewCmdAll returned nil")
	}
	if cmd.Use != "all" {
		t.Errorf("Use = %q, want %q", cmd.Use, "all")
	}

	got := map[string]bool{}
	for _, sub := range cmd.Commands() {
		got[sub.Name()] = true
	}
	for _, name := range []string{"ingest", "compute"} {
		if !got[name] {
			t.Errorf("missing %q subcommand", name)
		}
	}
	if got["status"] {
		t.Errorf("unexpected %q subcommand on `all` — `pvmt status` already covers all resources", "status")
	}
}
