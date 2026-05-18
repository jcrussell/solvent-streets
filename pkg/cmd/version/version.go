package version

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/build"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO *iostreams.IOStreams
}

func NewCmdVersion(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{IO: f.IOStreams}

	return &cobra.Command{
		Use:   "version",
		Short: "Show pvmt version information",
		Long:  "Display version, commit, build date, Go version, and os/arch.",
		Example: `  # Print build metadata (version, commit, date, go version, os/arch)
  pvmt version`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			fmt.Fprint(opts.IO.Out, build.Current().Full())
			return nil
		},
	}
}
