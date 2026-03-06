package compute

import (
	"fmt"

	"pvmt/internal/db"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

type Options struct {
	Factory      *cmdutil.Factory
	ResourceType resource.ResourceType
}

func NewCmdCompute(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		Factory:      f,
		ResourceType: rt,
	}

	cmd := &cobra.Command{
		Use:   "compute",
		Short: fmt.Sprintf("Compute %s area statistics", rt.Name()),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			return runCompute(opts)
		},
	}

	return cmd
}

func runCompute(opts *Options) error {
	ios := opts.Factory.IOStreams

	store, err := opts.Factory.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	fmt.Fprintf(ios.Out, "Loading %s features from database...\n", opts.ResourceType.Name())
	dbFeatures, err := store.ListFeatures(opts.ResourceType.Name())
	if err != nil {
		return fmt.Errorf("list features: %w", err)
	}

	if len(dbFeatures) == 0 {
		return fmt.Errorf("no %s features in database. Run 'pvmt %s ingest' first", opts.ResourceType.Name(), cmdName(opts.ResourceType))
	}

	fmt.Fprintf(ios.Out, "Processing %d features...\n", len(dbFeatures))

	// Convert db.Feature to resource.Feature
	resFeatures := make([]resource.Feature, len(dbFeatures))
	for i, f := range dbFeatures {
		resFeatures[i] = resource.Feature{
			ID:           f.ID,
			Name:         f.Name,
			Tags:         f.Tags,
			GeometryJSON: f.GeometryJSON,
			SourceAPI:    f.SourceAPI,
		}
	}

	gjson, areaSqFt, err := opts.ResourceType.ProcessFeatures(resFeatures)
	if err != nil {
		return fmt.Errorf("process features: %w", err)
	}

	areaAcres := geo.AreaAcres(areaSqFt)

	result := db.ComputeResult{
		ResourceType:   opts.ResourceType.Name(),
		TotalAreaSqFt:  areaSqFt,
		TotalAreaAcres: areaAcres,
		FeatureCount:   len(dbFeatures),
		GeometryJSON:   gjson,
	}

	if err := store.SaveComputeResult(result); err != nil {
		return fmt.Errorf("save result: %w", err)
	}

	fmt.Fprintf(ios.Out, "\n%s Results:\n", opts.ResourceType.Name())
	fmt.Fprintf(ios.Out, "  Features:  %d\n", len(dbFeatures))
	fmt.Fprintf(ios.Out, "  Area:      %.0f sq ft\n", areaSqFt)
	fmt.Fprintf(ios.Out, "  Area:      %.1f acres\n", areaAcres)
	fmt.Fprintf(ios.Out, "  Area:      %.2f sq mi\n", areaAcres/640)

	return nil
}

func cmdName(rt resource.ResourceType) string {
	switch rt.Name() {
	case "pavements":
		return "roads"
	case "parking":
		return "parking"
	default:
		return "all"
	}
}
