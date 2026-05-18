package compute

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/filter"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/peterstace/simplefeatures/geom"
)

// RunCombined buffers features from every resource type, indexes them as one
// geometry collection, and runs the hex pipeline once to produce a true
// cross-resource paved-area total. ComputeHexStats's per-hex local union dedupes
// overlap between, e.g., a road buffer and a sidewalk buffer that sit inside
// it. Per-resource compute results are unaffected; this writes new rows under
// the "combined" / "combined:city" labels.
//
// Run after `all compute` has populated each resource's features. Safe when
// some resources have no features — those are skipped.
func RunCombined(ctx context.Context, f *cmdutil.Factory) error {
	cfg, err := f.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	city, err := f.CurrentCity()
	if err != nil {
		return fmt.Errorf("city: %w", err)
	}
	store, err := f.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	ios := f.IOStreams

	boundaryGJSON, bbox, proj, err := loadBoundary(ctx, store, city)
	if err != nil {
		return err
	}

	bufs := bufferAllResources(ctx, store, proj, ios.ErrOut)
	if len(bufs.all) == 0 {
		fmt.Fprintf(ios.ErrOut, "combined: no features across resources, skipping\n")
		return nil
	}

	hexes := buildClippedHexGrid(ctx, cfg, city, proj, bbox, boundaryGJSON)
	snapshotID := createSnapshot(ctx, ios.ErrOut, store, cfg)
	sys := f.UnitSystem()

	if err := saveCombinedResult(ctx, store, hexes, bufs.all, resource.CombinedAll, bufs.allCount, snapshotID, ios.Out, ios.ErrOut, sys); err != nil {
		return err
	}
	if len(bufs.city) > 0 {
		if err := saveCombinedResult(ctx, store, hexes, bufs.city, resource.CombinedCity, bufs.cityCount, snapshotID, ios.Out, ios.ErrOut, sys); err != nil {
			return err
		}
	}
	return nil
}

type combinedBuffers struct {
	all       []geom.Geometry
	city      []geom.Geometry
	allCount  int
	cityCount int
}

// bufferAllResources loads features for each resource type and buffers them,
// returning two slices: every-jurisdiction and city-only. Each feature is
// buffered exactly once — the city slice is derived by filtering the
// already-buffered set on jurisdiction. Resources with missing or
// unbufferable data are warned about and skipped. Geometry panics on one
// resource are caught (with stack to errOut) and turned into per-resource
// warnings, so a single malformed feature can't crash the whole compute run.
func bufferAllResources(ctx context.Context, store db.Store, proj *geo.UTMProjector, errOut io.Writer) combinedBuffers {
	var bufs combinedBuffers
	for _, rt := range resource.All {
		resFeatures, ok := loadFeaturesForCombined(ctx, store, rt, errOut)
		if !ok {
			continue
		}
		var paired []resource.BufferedFeature
		if err := cmdutil.GuardPanic(errOut, func() error {
			paired = rt.BufferFeaturesPaired(resFeatures, proj)
			if len(paired) == 0 {
				return errNoValidGeoms
			}
			return nil
		}); err != nil {
			fmt.Fprintf(errOut, "combined: buffer %s: %v\n", rt.Type(), err)
			continue
		}
		// Preserve pre-refactor count semantics: bufs counts reflect input
		// feature counts per jurisdiction, not the post-buffer survivor count.
		cityInputs := 0
		for _, f := range resFeatures {
			if filter.ClassifyJurisdiction(f.Tags) == filter.JurisdictionCity {
				cityInputs++
			}
		}
		for _, bf := range paired {
			bufs.all = append(bufs.all, bf.Geom)
			if filter.ClassifyJurisdiction(bf.Feature.Tags) == filter.JurisdictionCity {
				bufs.city = append(bufs.city, bf.Geom)
			}
		}
		bufs.allCount += len(resFeatures)
		bufs.cityCount += cityInputs
	}
	return bufs
}

// errNoValidGeoms surfaces inside the panic-guarded buffer closure when a
// resource yields zero valid geometries — handled the same way as an
// underlying buffer error so callers log and skip the resource.
var errNoValidGeoms = errors.New("no valid geometries to process")

func loadFeaturesForCombined(ctx context.Context, store db.Store, rt resource.Source, errOut io.Writer) ([]resource.Feature, bool) {
	dbFeatures, err := store.ListFeatures(ctx, rt.Type())
	if err != nil {
		fmt.Fprintf(errOut, "combined: skip %s: %v\n", rt.Type(), err)
		return nil, false
	}
	if len(dbFeatures) == 0 {
		return nil, false
	}
	out := make([]resource.Feature, len(dbFeatures))
	for i, f := range dbFeatures {
		out[i] = resource.Feature{
			ID:           f.ID,
			Name:         f.Name,
			Tags:         f.Tags,
			GeometryJSON: f.GeometryJSON,
			SourceAPI:    f.SourceAPI,
		}
	}
	return out, true
}

func buildClippedHexGrid(ctx context.Context, cfg *config.Config, city *config.CityConfig, proj *geo.UTMProjector, bbox [4]float64, boundaryGJSON string) []geo.Hex {
	hexEdge := cfg.ResolvedHexEdge(city)
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0])
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2])
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	if boundaryGeom, err := parseGeoJSONGeometry(boundaryGJSON, proj); err == nil && !boundaryGeom.IsEmpty() {
		hexes = geo.ClipHexesToBoundary(ctx, hexes, boundaryGeom, nil)
	}
	return hexes
}

func saveCombinedResult(ctx context.Context, store db.Store, hexes []geo.Hex, buffered []geom.Geometry, label resource.Type, featureCount int, snapshotID *int64, out io.Writer, errOut io.Writer, sys units.System) error {
	var area float64
	if err := cmdutil.GuardPanic(errOut, func() error {
		idx := geo.NewGeomIndexFromGeoms(buffered)
		hexStats := geo.ComputeHexStats(ctx, hexes, idx, string(label), nil)
		for _, s := range hexStats {
			area += s.AreaSqM
		}
		return nil
	}); err != nil {
		return fmt.Errorf("compute %s hex stats: %w", label, err)
	}
	if err := store.SaveComputeResult(ctx, db.ComputeResult{
		ResourceType: label,
		TotalAreaSqM: area,
		FeatureCount: featureCount,
		SnapshotID:   snapshotID,
	}); err != nil {
		return fmt.Errorf("save %s result: %w", label, err)
	}
	suffix := "Results (all)"
	if label == resource.CombinedCity {
		suffix = "Results (city only)"
	}
	printResults(out, "combined "+suffix, featureCount, area, sys)
	return nil
}
