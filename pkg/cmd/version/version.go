package version

import (
	"fmt"

	"pvmt/internal/build"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO *iostreams.IOStreams
}

func NewCmdVersion(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{IO: f.IOStreams}

	return &cobra.Command{
		Use:   "version",
		Short: "Show pvmt version information",
		Long:  "Display version, commit, and build date information.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			fmt.Fprintf(opts.IO.Out, "pvmt %s (commit: %s, built: %s)\n",
				build.Version, build.Commit, build.Date)
			return nil
		},
	}
}
