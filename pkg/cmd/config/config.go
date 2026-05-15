package config

import (
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdConfig returns the `config` parent command. It only aggregates
// subcommands; it has no runFunc of its own.
func NewCmdConfig(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect resolved configuration",
		Long:  "Commands for inspecting the layered pvmt configuration after env, file, and flag resolution.",
	}

	cmd.AddCommand(NewCmdShow(f, nil))

	return cmd
}
