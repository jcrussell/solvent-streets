package roads

import (
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestNewCmdRoads_RegistersSubcommands pins the thin-wrapper contract:
// the factory returns a cobra.Command with ingest/compute/status wired
// for the roads resource type. Catches typos like wiring a Sidewalk or
// Parking resource into the roads tree.
func TestNewCmdRoads_RegistersSubcommands(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
	}

	cmd := NewCmdRoads(f)
	if cmd == nil {
		t.Fatal("NewCmdRoads returned nil")
	}
	if cmd.Use != "roads" {
		t.Errorf("Use = %q, want %q", cmd.Use, "roads")
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
		if !strings.Contains(short, "roads") {
			t.Errorf("%q subcommand short %q does not reference 'roads' (wrong resource wired?)", name, short)
		}
	}
}
