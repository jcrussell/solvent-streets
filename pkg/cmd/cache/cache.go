// Package cache implements the `pvmt cache` command group for managing
// the on-disk HTTP response cache.
//
// The cache is deliberately NOT part of `pvmt gc`: gc resolves cities and
// opens the database, so it fails when the config is broken or the DB is
// unopenable — exactly the states in which a user most wants to reclaim
// disk. The HTTP cache is global, config-independent, and
// database-independent, so it gets its own group that touches neither.
package cache

import (
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdCache returns the parent `cache` command. It only aggregates
// subcommands; it has no runFunc of its own.
func NewCmdCache(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage the on-disk HTTP response cache",
		Long: `Inspect and bound the HTTP response cache pvmt keeps under the
user cache directory (` + "`$XDG_CACHE_HOME/pvmt/http`" + ` by default).

Entries are written by every ingest that hits Overpass, ArcGIS, or
Nominatim and are served for 24 hours before revalidation. Nothing
removes them automatically, so the directory grows for as long as the
install lives; ` + "`pvmt cache prune`" + ` is the reclaim step.

Pruning is always safe: a removed entry is re-fetched on next use.`,
		Example: `  # Report what would be reclaimed, without deleting
  pvmt cache prune --dry-run

  # Apply the default age and size caps
  pvmt cache prune

  # Keep only the last week, capped at 100 MiB
  pvmt cache prune --max-age=168h --max-size=100mb`,
	}
	cmd.AddCommand(NewCmdPrune(f, nil))
	return cmd
}
