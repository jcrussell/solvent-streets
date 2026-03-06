package serve

import (
	"fmt"

	"pvmt/internal/server"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

type Options struct {
	Factory *cmdutil.Factory
	Port    int
}

func NewCmdServe(f *cmdutil.Factory) *cobra.Command {
	opts := &Options{
		Factory: f,
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the web server",
		Long:  "Start the MapLibre visualization server with API endpoints.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(opts)
		},
	}

	cmd.Flags().IntVar(&opts.Port, "port", 8080, "Port to listen on")

	return cmd
}

func runServe(opts *Options) error {
	store, err := opts.Factory.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	srv := server.New(store, opts.Port)
	return srv.ListenAndServe()
}
