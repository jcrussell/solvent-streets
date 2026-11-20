package resourcecmd

import (
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

// TestNew_SpecComputeReplacesTheDefault: a Spec.Compute hook replaces the
// group's default compute subcommand. The group shape itself is pinned by
// cmd/pvmt's tree tests.
func TestNew_SpecComputeReplacesTheDefault(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	custom := New(f, &resource.Parking{}, Spec{Use: "x", Compute: func(*cmdutil.Factory, resource.Source) *cobra.Command {
		return &cobra.Command{Use: "compute", Short: "custom"}
	}})
	cmd, _, err := custom.Find([]string{"compute"})
	if err != nil || cmd.Name() != "compute" {
		t.Fatalf("compute does not resolve: %v", err)
	}
	if cmd.Short != "custom" {
		t.Errorf("Spec.Compute must replace the default compute subcommand, got %q", cmd.Short)
	}
}
