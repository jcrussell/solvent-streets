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
	Use string
	// Noun names the group's features in help prose, plural ("roads",
	// "Census blocks").
	Noun    string
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
	// The subcommands' help names the group (`pvmt census ingest`), not
	// rt.Type(), which for census and services is a storage name
	// ("layer-census-blocks", "facility-fire") rather than a command.
	ing := ingest.NewCmdIngest(f, rt, nil)
	ing.Short, ing.Long, ing.Example = ingest.Short(spec.Noun), ingest.Long(spec.Use, spec.Noun, rt), ingest.Example(spec.Use, spec.Noun, rt)
	cmd.AddCommand(ing)
	if spec.Compute != nil {
		cmd.AddCommand(spec.Compute(f, rt))
	} else {
		comp := compute.NewCmdCompute(f, rt, nil)
		comp.Short, comp.Long, comp.Example = compute.Short(spec.Noun), compute.Long(spec.Use, spec.Noun), compute.Example(spec.Use, spec.Noun)
		cmd.AddCommand(comp)
	}
	st := status.NewCmdStatus(f, rt, nil)
	st.Short, st.Long, st.Example = status.Short(spec.Noun), status.Long(spec.Noun, rt), status.Example(spec.Use, rt)
	cmd.AddCommand(st)
	return cmd
}
