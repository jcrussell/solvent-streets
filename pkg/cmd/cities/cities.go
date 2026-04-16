package cities

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"pvmt/internal/db"
	"pvmt/internal/resource"
	"pvmt/internal/units"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO         *iostreams.IOStreams
	RootDB     func() (db.RootStorer, error)
	UnitSystem func() units.System
	Exporter   cmdutil.Exporter
}

type cityRow struct {
	Slug         string         `json:"slug"`
	Name         string         `json:"name"`
	Features     map[string]int `json:"features"`
	TotalAreaSqM float64        `json:"totalAreaSqM"`
	LastIngest   string         `json:"lastIngest,omitempty"`
	LastCompute  string         `json:"lastCompute,omitempty"`
}

var citiesFields = []string{"slug", "name", "features", "totalAreaSqM", "lastIngest", "lastCompute"}

func NewCmdCities(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO: f.IOStreams,
		RootDB: func() (db.RootStorer, error) {
			return f.RootDB()
		},
		UnitSystem: f.UnitSystem,
	}

	cmd := &cobra.Command{
		Use:   "cities",
		Short: "List cities in the database",
		Long:  "List all cities stored in the shared pvmt database with per-resource feature counts.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			return runCities(cmd.Context(), opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, citiesFields)

	return cmd
}

func runCities(ctx context.Context, opts *Options) error {
	ios := opts.IO

	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	list, err := root.ListCities(ctx)
	if err != nil {
		return fmt.Errorf("list cities: %w", err)
	}

	rows := make([]cityRow, 0, len(list))
	for _, c := range list {
		store := root.ForCity(c.ID)
		row := cityRow{
			Slug:     c.Slug,
			Name:     c.Name,
			Features: make(map[string]int, len(resource.All)),
		}
		var latestIngest, latestCompute time.Time
		for _, rt := range resource.All {
			info, err := store.Stats(ctx, rt.Name())
			if err != nil {
				fmt.Fprintf(ios.ErrOut, "Warning: %s/%s: %v\n", c.Slug, rt.Name(), err)
				continue
			}
			row.Features[rt.Name()] = info.FeatureCount
			row.TotalAreaSqM += info.TotalAreaSqM
			if info.LastIngestAt != nil && info.LastIngestAt.After(latestIngest) {
				latestIngest = *info.LastIngestAt
			}
			if info.LastComputeAt != nil && info.LastComputeAt.After(latestCompute) {
				latestCompute = *info.LastComputeAt
			}
		}
		if !latestIngest.IsZero() {
			row.LastIngest = latestIngest.Format(time.RFC3339)
		}
		if !latestCompute.IsZero() {
			row.LastCompute = latestCompute.Format(time.RFC3339)
		}
		rows = append(rows, row)
	}

	if opts.Exporter != nil {
		return opts.Exporter.Write(ios, rows)
	}

	if len(rows) == 0 {
		fmt.Fprintln(ios.Out, "No cities in database. Run 'pvmt all ingest' to populate.")
		return nil
	}

	sys := opts.UnitSystem()
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Slug", "Name", "Roads", "Parking", "Sidewalks", units.AreaLargeLabel(sys), "Last Ingest", "Last Compute")
	for _, r := range rows {
		tp.AddRow(
			r.Slug,
			r.Name,
			strconv.Itoa(r.Features["roads"]),
			strconv.Itoa(r.Features["parking"]),
			strconv.Itoa(r.Features["sidewalks"]),
			fmt.Sprintf("%.1f", units.AreaLargeValue(r.TotalAreaSqM, sys)),
			formatTimestamp(r.LastIngest, ios.IsTTY()),
			formatTimestamp(r.LastCompute, ios.IsTTY()),
		)
	}
	return tp.Render()
}

func formatTimestamp(raw string, isTTY bool) string {
	if raw == "" {
		return "never"
	}
	if isTTY {
		t, _ := time.Parse(time.RFC3339, raw)
		return fmt.Sprintf("%s (%s)", raw, iostreams.RelativeTime(t))
	}
	return raw
}
