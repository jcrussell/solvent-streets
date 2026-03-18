package serve

import (
	"fmt"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/server"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO     *iostreams.IOStreams
	DB     func() (db.Store, error)
	Config func() (*config.Config, error)
	Port   int
}

func NewCmdServe(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:     f.IOStreams,
		DB:     f.DB,
		Config: f.Config,
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the web server",
		Long:  "Start the MapLibre visualization server with API endpoints.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			return runServe(opts)
		},
	}

	cmd.Flags().IntVar(&opts.Port, "port", 8080, "Port to listen on")

	return cmd
}

func runServe(opts *Options) error {
	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	store, err := opts.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	srv := server.New(store, cfg, opts.Port)
	return srv.ListenAndServe()
}
