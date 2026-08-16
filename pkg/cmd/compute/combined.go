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
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

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
//
// grid is the clipped hex grid to run the union passes against; pass the shared
// grid `all compute` built once per city (see BuildCityGrid) so the boundary
// clip isn't repeated here. Pass nil to have RunCombined build its own grid
// (standalone use).
func RunCombined(ctx context.Context, f *cmdutil.Factory, grid []geo.Hex) (retErr error) {
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

	// Reuse the grid `all compute` already built + clipped for this city; only
	// build one here when called standalone (grid == nil).
	hexes := grid
	if hexes == nil {
		hexes = buildClippedHexGrid(ctx, cfg, city, proj, bbox, boundaryGJSON, ios.ErrOut)
	}
	cr := &combinedRunner{
		store:      store,
		io:         ios,
		sys:        f.UnitSystem(),
		snapshotID: createSnapshot(ctx, ios.ErrOut, store, cfg, city),
	}
	// Same single cleanup chokepoint as doCompute: cr.save's cancel guard (and
	// any post-snapshot error path) returns without deleting the snapshot row
	// created above. An empty run from a SIGINT'd combined compute would
	// otherwise consume a `snapshots prune` --keep slot. Delete it on cancel
	// via a value-preserving, cancellation-ignoring derived ctx.
	if cr.snapshotID != nil {
		id := *cr.snapshotID
		defer func() {
			if retErr == nil || !errors.Is(retErr, context.Canceled) {
				return
			}
			cleanupCtx := context.WithoutCancel(ctx)
			if _, err := store.DeleteSnapshot(cleanupCtx, id); err != nil {
				fmt.Fprintf(ios.ErrOut, "Warning: failed to delete snapshot %d after cancellation: %v\n", id, err)
			}
		}()
	}

	if err := cr.save(ctx, combinedPass{hexes: hexes, buffered: bufs.all, label: resource.CombinedAll, featureCount: bufs.allCount}); err != nil {
		return err
	}
	if len(bufs.city) > 0 {
		if err := cr.save(ctx, combinedPass{hexes: hexes, buffered: bufs.city, label: resource.CombinedCity, featureCount: bufs.cityCount}); err != nil {
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
			paired = rt.BufferFeaturesPaired(ctx, resFeatures, proj)
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

// BuildCityGrid loads the city boundary and returns the clipped hex grid keyed
// on (boundary, hex edge). `all compute` builds it once per city and threads it
// into each per-resource compute (RunResourceForCity) and RunCombined, so the
// expensive HexGrid + ClipHexesToBoundary runs once instead of four times.
//
// On any failure loading the boundary it returns a nil grid and the error. The
// grid depends only on (boundary, hex edge), so a boundary failure here means
// every per-resource pass would fail identically; `all compute` surfaces the
// error once and skips the city rather than repeating it. A caller may instead
// pass nil to RunResourceForCity/RunCombined to have each pass build its own.
func BuildCityGrid(ctx context.Context, f *cmdutil.Factory) ([]geo.Hex, error) {
	cfg, err := f.Config()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	city, err := f.CurrentCity()
	if err != nil {
		return nil, fmt.Errorf("city: %w", err)
	}
	store, err := f.CityDB()
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	boundaryGJSON, bbox, proj, err := loadBoundary(ctx, store, city)
	if err != nil {
		return nil, err
	}
	return buildClippedHexGrid(ctx, cfg, city, proj, bbox, boundaryGJSON, f.IOStreams.ErrOut), nil
}

func buildClippedHexGrid(ctx context.Context, cfg *config.Config, city *config.CityConfig, proj *geo.UTMProjector, bbox [4]float64, boundaryGJSON string, errOut io.Writer) []geo.Hex {
	hexEdge := cfg.ResolvedHexEdge(city)
	minX, minY, maxX, maxY := geo.ProjectedBBoxExtent(proj, bbox)
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)
	boundaryGeom, err := parseGeoJSONGeometry(boundaryGJSON, proj)
	switch {
	case err != nil:
		// Skipping the clip runs combined stats over the full bbox grid.
		fmt.Fprintf(errOut, "combined: boundary parse failed for city %q, computing over full bbox: %v\n", city.Name, err)
	case boundaryGeom.IsEmpty():
		fmt.Fprintf(errOut, "combined: boundary empty after cleaning for city %q, computing over full bbox\n", city.Name)
	default:
		hexes = geo.ClipHexesToBoundary(ctx, hexes, boundaryGeom, nil)
	}
	return hexes
}

// combinedRunner bundles the cross-pass dependencies (DB, IO, units, snapshot)
// so each combinedPass save() invocation carries only its varying payload.
// Mirrors the `computer` struct in compute.go.
type combinedRunner struct {
	store      db.Store
	io         *iostreams.IOStreams
	sys        units.System
	snapshotID *int64
}

// combinedPass is one cross-resource union pass: the buffered geometries to
// index, the hex grid to clip them against, the row label, and the input
// feature count to persist on the ComputeResult row.
type combinedPass struct {
	hexes        []geo.Hex
	buffered     []geom.Geometry
	label        resource.Type
	featureCount int
}

func (cr *combinedRunner) save(ctx context.Context, p combinedPass) error {
	var area float64
	if err := cmdutil.GuardPanic(cr.io.ErrOut, func() error {
		idx := geo.NewGeomIndexFromGeoms(p.buffered)
		hexStats := geo.ComputeHexStats(ctx, p.hexes, idx, string(p.label), nil)
		for _, s := range hexStats {
			area += s.Area
		}
		return nil
	}); err != nil {
		return fmt.Errorf("compute %s hex stats: %w", p.label, err)
	}
	// ComputeHexStats (ParallelMap-backed) returns partial results with no
	// error on cancellation; bail before persisting a truncated combined
	// result under a complete-looking snapshot.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("compute %s cancelled: %w", p.label, err)
	}
	// Only the aggregate ComputeResult is persisted — deliberately no
	// SaveHexStats, unlike doCompute. ListHexStats has exactly one caller in
	// the tree (export's aggregateHexStats), and it only ever asks for the
	// resource.All types — "roads", "parking", "sidewalks" (× ":city") — so
	// "combined"/"combined:city" hex rows would never be read; every combined
	// consumer goes through LatestComputeResult instead. Nor would they all
	// render if something did read them: the export grid (cityHexGrid) agrees
	// with the grid used here on hex ids, but additionally drops sliver hexes,
	// so that subset of the rows would land on hexes the export excludes and
	// be silently dropped by buildHexFeature's hexMap lookup. Writing them
	// would add a row per covered hex per scope for zero readers.
	if err := cr.store.SaveComputeResult(ctx, db.ComputeResult{
		ResourceType: p.label,
		TotalArea:    area,
		FeatureCount: p.featureCount,
		SnapshotID:   cr.snapshotID,
	}); err != nil {
		return fmt.Errorf("save %s result: %w", p.label, err)
	}
	suffix := "Results (all)"
	if p.label == resource.CombinedCity {
		suffix = "Results (city only)"
	}
	printResults(cr.io.Out, "combined "+suffix, p.featureCount, area, cr.sys)
	return nil
}
