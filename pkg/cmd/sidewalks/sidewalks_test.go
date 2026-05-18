package sidewalks

import (
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestNewCmdSidewalks_RegistersSubcommands pins the thin-wrapper contract:
// the factory returns a cobra.Command with ingest/compute/status wired
// for the sidewalks resource type. Catches typos like wiring a Pavement
// or Parking resource into the sidewalks tree.
func TestNewCmdSidewalks_RegistersSubcommands(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
	}

	cmd := NewCmdSidewalks(f)
	if cmd == nil {
		t.Fatal("NewCmdSidewalks returned nil")
	}
	if cmd.Use != "sidewalks" {
		t.Errorf("Use = %q, want %q", cmd.Use, "sidewalks")
	}

	subs := map[string]string{}
	for _, sub := range cmd.Commands() {
		subs[sub.Name()] = sub.Short
	}
	for _, name := range []string{"ingest", "compute", "status"} {
		short, ok := subs[name]
		if !ok {
			t.Errorf("missing %q subcommand", name)
			continue
		}
		if !strings.Contains(short, "sidewalks") {
			t.Errorf("%q subcommand short %q does not reference 'sidewalks' (wrong resource wired?)", name, short)
		}
	}
}
