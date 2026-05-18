package parking

import (
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestNewCmdParking_RegistersSubcommands pins the thin-wrapper contract:
// the factory returns a cobra.Command with ingest/compute/status wired
// for the parking resource type. Catches typos like wiring a Pavement
// or Sidewalk resource into the parking tree.
func TestNewCmdParking_RegistersSubcommands(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
	}

	cmd := NewCmdParking(f)
	if cmd == nil {
		t.Fatal("NewCmdParking returned nil")
	}
	if cmd.Use != "parking" {
		t.Errorf("Use = %q, want %q", cmd.Use, "parking")
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
		if !strings.Contains(short, "parking") {
			t.Errorf("%q subcommand short %q does not reference 'parking' (wrong resource wired?)", name, short)
		}
	}
}
