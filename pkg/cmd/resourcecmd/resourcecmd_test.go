package resourcecmd

import (
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

// TestNew_WiresTheThreeSubcommands pins the group shape every resource shares
// and the compute replacement hook.
func TestNew_WiresTheThreeSubcommands(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	cmd := New(f, &resource.Parking{}, Spec{Use: "parking", Short: "s"})
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"ingest", "compute", "status"} {
		if !names[want] {
			t.Errorf("missing %s subcommand; got %v", want, names)
		}
	}
	custom := New(f, &resource.Parking{}, Spec{Use: "x", Compute: func(*cmdutil.Factory, resource.Source) *cobra.Command {
		return &cobra.Command{Use: "compute", Short: "custom"}
	}})
	for _, c := range custom.Commands() {
		if c.Name() == "compute" && c.Short != "custom" {
			t.Errorf("Spec.Compute must replace the default compute subcommand, got %q", c.Short)
		}
	}
}
