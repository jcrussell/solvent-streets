package config

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO        *iostreams.IOStreams
	Config    func() (*config.Config, error)
	FlagUnits string // --units value from root, "" when unset
	Sources   bool   // --sources mode
	Exporter  cmdutil.Exporter
}

func NewCmdShow(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:     f.IOStreams,
		Config: f.Config,
	}

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the resolved configuration",
		Long: `Print the resolved pvmt configuration.

Without flags, emits TOML ready to paste back into pvmt.toml.
With --sources, emits 'key = value (source)' lines so you can answer
"why is this value X?". With --json, emits a JSON array (follows the
standard --json <fields> pattern with --jq and --template).`,
		Example: `  # TOML view, ready to paste back into pvmt.toml
  pvmt config show

  # Annotated view explaining where each value came from
  pvmt config show --sources

  # JSON for scripting
  pvmt config show --json key,value,source`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fl := cmd.Root().PersistentFlags().Lookup("units"); fl != nil && fl.Changed {
				opts.FlagUnits = fl.Value.String()
			}
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runShow(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Sources, "sources", false, "Annotate each line with its source (env, file, flag, default)")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, config.ResolvedFieldNames)
	cmd.MarkFlagsMutuallyExclusive("sources", "json")

	return cmd
}

func runShow(opts *Options) error {
	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	if opts.Exporter != nil {
		return cmdutil.WriteRows(opts.IO, opts.Exporter, cfg.Resolve(opts.FlagUnits))
	}

	if opts.Sources {
		for _, f := range cfg.Resolve(opts.FlagUnits) {
			fmt.Fprintf(opts.IO.Out, "%s = %v (%s)\n", f.Key, f.Value, f.Source)
		}
		return nil
	}

	fmt.Fprint(opts.IO.Out, export.ResolvedTOML(cfg))
	return nil
}
