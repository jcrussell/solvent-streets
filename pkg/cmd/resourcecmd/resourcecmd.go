// Package resourcecmd builds the per-resource command groups — `pvmt roads`,
// `pvmt parking`, `pvmt sidewalks`, `pvmt services`, `pvmt census` — from one
// description: ingest + compute + status over a resource.Source, differing
// in help text and, where a group needs it, its own compute command. It
// lives under pkg/cmd rather than pkg/cmdutil because it imports the
// ingest/compute/status commands, which themselves import cmdutil.
package resourcecmd

import (
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/ingest"
	"github.com/jcrussell/solvent-streets/pkg/cmd/status"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// Spec is the help text of a resource command group plus an optional
// replacement for the compute subcommand.
type Spec struct {
	Use     string
	Short   string
	Long    string
	Example string
	// Compute, when set, builds the group's compute subcommand in place of
	// the pavement coverage pass (compute.NewCmdCompute). Services use it for
	// their routing-based coverage.
	Compute func(f *cmdutil.Factory, rt resource.Source) *cobra.Command
}

// New builds the group: ingest, compute and status subcommands over rt.
func New(f *cmdutil.Factory, rt resource.Source, spec Spec) *cobra.Command {
	cmd := &cobra.Command{
		Use:     spec.Use,
		Short:   spec.Short,
		Long:    spec.Long,
		Example: spec.Example,
	}
	cmdutil.MarkGroup(cmd)
	cmd.AddCommand(ingest.NewCmdIngest(f, rt, nil))
	if spec.Compute != nil {
		cmd.AddCommand(spec.Compute(f, rt))
	} else {
		cmd.AddCommand(compute.NewCmdCompute(f, rt, nil))
	}
	cmd.AddCommand(status.NewCmdStatus(f, rt, nil))
	return cmd
}
