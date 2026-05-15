package version

import (
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/build"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

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
		Long:  "Display version, commit, build date, Go version, and os/arch.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			fmt.Fprint(opts.IO.Out, build.Current().Full())
			return nil
		},
	}
}
