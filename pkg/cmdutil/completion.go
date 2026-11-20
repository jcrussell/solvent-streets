package cmdutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"

	"github.com/spf13/cobra"
)

// SnapshotNameCompletion completes snapshot names, newest first, for a
// command that takes up to maxArgs of them as arguments, or for a flag when
// maxArgs is negative. It reads the database read-only and briefly
// (db.SnapshotNames), and completes nothing when there is none yet.
func SnapshotNameCompletion(f *Factory, maxArgs int) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		path, ok := databasePath(f)
		if (maxArgs >= 0 && len(args) >= maxArgs) || !ok {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
		defer cancel()
		names, err := db.SnapshotNames(ctx, path)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []cobra.Completion
		for _, n := range names {
			if strings.HasPrefix(n, toComplete) {
				out = append(out, n)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
	}
}

// completionTimeout bounds a completion's database read, so TAB never
// waits on a busy database.
const completionTimeout = time.Second

// databasePath is the shared database's path when the file exists.
func databasePath(f *Factory) (string, bool) {
	if f.Paths == nil {
		return "", false
	}
	p, err := f.Paths()
	if err != nil {
		return "", false
	}
	path := filepath.Join(p.Data, db.FileName)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

// DefaultNoFileCompletion makes every command under root that takes no file
// argument complete no files: a command without its own ValidArgsFunction
// otherwise offers the working directory's files for an argument it refuses.
// A command whose arguments are paths sets its own ValidArgsFunction.
func DefaultNoFileCompletion(root *cobra.Command) {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if c.ValidArgsFunction == nil && c.ValidArgs == nil {
			c.ValidArgsFunction = cobra.NoFileCompletions
		}
	}
	walk(root)
}
