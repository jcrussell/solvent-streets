package cmdutil

import "github.com/spf13/cobra"

// MarkGroup makes a parent command — one that only aggregates subcommands —
// refuse stray arguments. Cobra returns flag.ErrHelp for a non-runnable
// command before it validates args, so without this `pvmt cache pruen`
// printed the group help and exited 0. NoArgs alone is not enough: on a
// command with no RunE it still yields help and exit 0. With both, a typo
// returns cobra's `unknown command "x" for "pvmt cache"`, which the runner's
// classifyUsageError maps to exit 2, and a bare `pvmt cache` still shows
// help (byob-command-shape.6).
func MarkGroup(cmd *cobra.Command) {
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
}
