// Package resourcecmd builds the per-resource command group — `pvmt roads`,
// `pvmt parking`, `pvmt sidewalks`, `pvmt services` — from one description.
// The three pavement groups were structural twins (ingest + compute + status
// over a resource.Source) that differed only in their help text; the fourth
// group, services, is the trigger the deferred note on that duplication
// (solvent-streets-2a1c) said to wait for. It lives under pkg/cmd rather than
// pkg/cmdutil because it imports the ingest/compute/status commands, which
// themselves import cmdutil.
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
	cmd.AddCommand(ingest.NewCmdIngest(f, rt, nil))
	if spec.Compute != nil {
		cmd.AddCommand(spec.Compute(f, rt))
	} else {
		cmd.AddCommand(compute.NewCmdCompute(f, rt, nil))
	}
	cmd.AddCommand(status.NewCmdStatus(f, rt, nil))
	return cmd
}
