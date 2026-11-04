// Package services implements `pvmt services`: facility ingest, response-time
// coverage compute and status for a service (fire stations first), built on
// the same resource command group as roads/parking/sidewalks.
package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/services"
	"github.com/jcrussell/solvent-streets/pkg/cmd/resourcecmd"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// DefaultService is the service `pvmt services` acts on without --service.
const DefaultService = "fire"

// NewCmdServices builds the `services` group. --service (default fire) is a
// persistent flag; the facility Source the subcommands share is resolved
// from it before each subcommand runs, so one group serves every registered
// service without re-parsing flags through nested commands.
func NewCmdServices(f *cmdutil.Factory) *cobra.Command {
	fac := &resource.Facility{Service: DefaultService}
	if spec, ok := services.Lookup(DefaultService); ok {
		fac.Selectors = spec.OSMSelector
	}
	var service string
	cmd := resourcecmd.New(f, fac, resourcecmd.Spec{
		Use:   "services",
		Short: "Manage service facilities and response-time coverage",
		Long: `Ingest the facilities of a public service (fire stations, from
OpenStreetMap), compute how long that service takes to reach each hex over
the road network, and view status. The coverage lands as a per-hex metric
(fire_response_s) that the dashboard offers under "Color by".

Only the fire service is registered today: NFPA 1710 turnout (80 s) plus
travel at posted or class-default road speeds. [[services]] in pvmt.toml
overrides turnout_s / travel_threshold_s; [[cities.facilities]] adds
hand-placed stations where OpenStreetMap coverage is poor.`,
		Example: `  # Pull fire stations from OpenStreetMap for every configured city
  pvmt services ingest

  # Route from every station and store per-hex response seconds
  pvmt services compute

  # Show station counts and last ingest
  pvmt services status`,
		Compute: func(f *cmdutil.Factory, _ resource.Source) *cobra.Command {
			return NewCmdCompute(f, &service, nil)
		},
	})
	cmd.PersistentFlags().StringVar(&service, "service", DefaultService,
		"Service to act on ("+strings.Join(services.Names(), "|")+")")
	_ = cmd.RegisterFlagCompletionFunc("service", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return services.Names(), cobra.ShellCompDirectiveNoFileComp
	})
	// Resolve --service into the shared Facility before any subcommand runs.
	// Wrapping each RunE (rather than a PersistentPreRunE here) keeps the root
	// command's own PersistentPreRunE middleware in force — cobra runs only the
	// nearest persistent hook.
	for _, sub := range cmd.Commands() {
		orig := sub.RunE
		if orig == nil {
			continue
		}
		sub.RunE = func(c *cobra.Command, args []string) error {
			spec, err := lookupService(service)
			if err != nil {
				return err
			}
			fac.Service = spec.Name
			fac.Selectors = spec.OSMSelector
			return orig(c, args)
		}
	}
	return cmd
}

// lookupService resolves a --service value, mapping an unknown name to a
// flag error (exit 2) that lists the registered services.
func lookupService(name string) (services.Spec, error) {
	spec, ok := services.Lookup(name)
	if !ok {
		return services.Spec{}, cmdutil.FlagErrorf("unknown service %q (registered: %s)", name, strings.Join(services.Names(), ", "))
	}
	return spec, nil
}

// facilitySource is the Source facility ingest and status run against for a
// service.
func facilitySource(spec services.Spec) *resource.Facility {
	return &resource.Facility{Service: spec.Name, Selectors: spec.OSMSelector}
}

// RunIngestForCity ingests a service's facilities against the city-scoped
// factory cf — `pvmt all ingest` calls it in-process after the pavement
// resources, the same way it calls ingest.RunResourceForCity.
func RunIngestForCity(ctx context.Context, cf *cmdutil.Factory, service string) error {
	spec, err := lookupService(service)
	if err != nil {
		return err
	}
	return ingestRunResourceForCity(ctx, cf, facilitySource(spec))
}

// errorf keeps error wording in one place for the per-city runner.
func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }
