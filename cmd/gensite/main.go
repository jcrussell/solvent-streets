// Command gensite generates a multi-example static site for GitHub Pages.
// It discovers example configs under the examples directory, exports each
// using the export library, writes shared WASM assets once at the site root,
// and generates a landing page linking to each example.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gensite:", err)
		os.Exit(1)
	}
}

func run() (err error) {
	examplesDir := flag.String("examples", "examples", "directory containing example pvmt.toml configs")
	outputDir := flag.String("o", "site", "output directory for generated site")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile to this path")
	memProfile := flag.String("memprofile", "", "write a heap profile to this path (after GC)")
	flag.Parse()

	stop, perr := startProfiling(*cpuProfile, *memProfile, &err)
	if perr != nil {
		return perr
	}
	defer stop()

	start := time.Now()
	ctx := context.Background()

	// Discover example configs
	pattern := filepath.Join(*examplesDir, "*", "pvmt.toml")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob examples: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no pvmt.toml files found in %s/*/", *examplesDir)
	}
	sort.Strings(matches)

	// Open shared database
	rootDB, err := db.Open("")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if cerr := rootDB.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close database: %w", cerr)
		}
	}()

	// Clean and create output directory (refuse to wipe directories that
	// don't look like a previously generated site).
	if err := cmdutil.SafeCleanDir(*outputDir); err != nil {
		return err
	}

	examples, err := exportAll(ctx, rootDB, matches, *outputDir)
	if err != nil {
		return err
	}

	// Write shared WASM assets at site root
	if err := export.WriteSharedWasmAssets(*outputDir); err != nil {
		return fmt.Errorf("write shared WASM: %w", err)
	}

	// Generate landing page
	if err := export.RenderLandingPage(*outputDir, examples); err != nil {
		return fmt.Errorf("render landing page: %w", err)
	}

	// Tell GitHub Pages to skip Jekyll processing. Without this, a 200+ MB
	// tree gets pushed through Jekyll on every publish (slow), and any file
	// or directory whose name starts with "_" is silently dropped.
	if err := os.WriteFile(filepath.Join(*outputDir, ".nojekyll"), nil, 0o644); err != nil {
		return fmt.Errorf("write .nojekyll: %w", err)
	}

	fmt.Printf("Site exported to %s/ (%d examples) in %s\n", *outputDir, len(examples), time.Since(start).Round(time.Millisecond))
	return nil
}

// startProfiling begins CPU profiling (if cpuPath is set) and arranges for a
// heap profile to be written (if memPath is set) by the returned stop func,
// which the caller should defer. runErr points at the caller's named return so
// profile-finalization errors surface without masking the primary error.
func startProfiling(cpuPath, memPath string, runErr *error) (stop func(), err error) {
	var stops []func()
	if cpuPath != "" {
		f, ferr := os.Create(cpuPath)
		if ferr != nil {
			return nil, fmt.Errorf("create cpu profile: %w", ferr)
		}
		if serr := pprof.StartCPUProfile(f); serr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("start cpu profile: %w", serr)
		}
		stops = append(stops, func() {
			pprof.StopCPUProfile()
			if cerr := f.Close(); cerr != nil && *runErr == nil {
				*runErr = fmt.Errorf("close cpu profile: %w", cerr)
			}
		})
	}
	if memPath != "" {
		// Written by stop() (deferred to run's end) so it reflects live
		// allocations after the work completes.
		stops = append(stops, func() {
			if werr := writeHeapProfile(memPath); werr != nil && *runErr == nil {
				*runErr = werr
			}
		})
	}
	return func() {
		for _, s := range stops {
			s()
		}
	}, nil
}

// exportAll exports every example config concurrently. Each example is
// independent — separate output directory, its own projected boundary/geometry,
// read-only DB access — so they run on a worker pool. The shared rootDB is safe
// for concurrent reads (WAL mode, single pooled connection), and
// BuildCityEntries' only write is an idempotent INSERT OR IGNORE keyed by
// per-example config_id. Results land in a fixed-position slice so the
// landing-page order stays deterministic regardless of completion order.
func exportAll(ctx context.Context, rootDB *db.RootStore, matches []string, outputDir string) ([]export.ExampleInfo, error) {
	examples := make([]export.ExampleInfo, len(matches))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(min(runtime.NumCPU(), len(matches)))
	for i, cfgPath := range matches {
		g.Go(func() error {
			exStart := time.Now()
			info, err := exportExample(gctx, rootDB, cfgPath, outputDir)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "  %s exported in %s\n", info.Slug, time.Since(exStart).Round(time.Millisecond))
			examples[i] = info
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return examples, nil
}

// writeHeapProfile runs a GC then writes a heap profile to path, so the
// profile reflects live (retained) allocations rather than transient garbage.
func writeHeapProfile(path string) (err error) {
	f, ferr := os.Create(path)
	if ferr != nil {
		return fmt.Errorf("create mem profile: %w", ferr)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close mem profile: %w", cerr)
		}
	}()
	runtime.GC()
	if werr := pprof.WriteHeapProfile(f); werr != nil {
		return fmt.Errorf("write heap profile: %w", werr)
	}
	return nil
}

// exportExample runs the export pipeline for a single example config and
// returns metadata for the landing page.
func exportExample(ctx context.Context, rootDB *db.RootStore, cfgPath, outputDir string) (export.ExampleInfo, error) {
	slug := filepath.Base(filepath.Dir(cfgPath))
	fmt.Printf("=== Exporting %s ===\n", slug)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return export.ExampleInfo{}, fmt.Errorf("load config %s: %w", cfgPath, err)
	}

	entries, err := export.BuildCityEntries(ctx, rootDB, cfg, cfg.Cities)
	if err != nil {
		return export.ExampleInfo{}, fmt.Errorf("build entries for %s: %w", slug, err)
	}
	if len(entries) == 0 {
		return export.ExampleInfo{}, fmt.Errorf("no cities defined in %s", cfgPath)
	}

	outDir := filepath.Join(outputDir, slug)
	exporter := export.New(entries, cfg, outDir, cfg.UnitSystem().String())
	if err := exporter.SetWasmPrefix("../"); err != nil {
		return export.ExampleInfo{}, fmt.Errorf("set WASM prefix: %w", err)
	}
	exporter.SetSkipWasm(true)

	if err := exporter.Run(ctx); err != nil {
		return export.ExampleInfo{}, fmt.Errorf("export %s: %w", slug, err)
	}

	cityNames := make([]string, 0, len(cfg.Cities))
	for _, c := range cfg.Cities {
		cityNames = append(cityNames, c.Name)
	}

	unitSys := "imperial"
	if cfg.UnitSystem().String() == "metric" {
		unitSys = "metric"
	}

	return export.ExampleInfo{
		Slug:       slug,
		Title:      formatTitle(slug),
		CityNames:  strings.Join(cityNames, ", "),
		CityCount:  len(cfg.Cities),
		HexEdgeM:   int(cfg.HexEdge()),
		UnitSystem: unitSys,
	}, nil
}

// formatTitle turns a slug like "bay-area-ca" into "Bay Area, CA".
// If the last segment is longer than 2 characters, it's treated as part of
// the name rather than a state abbreviation (e.g. "test-config" → "Test Config").
func formatTitle(slug string) string {
	parts := strings.Split(slug, "-")

	// Capitalize each word
	for i, w := range parts {
		if len(w) > 0 {
			parts[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}

	// If last segment is 1-2 chars, treat it as a state/region abbreviation
	if len(parts) >= 2 && len(parts[len(parts)-1]) <= 2 {
		state := strings.ToUpper(parts[len(parts)-1])
		return strings.Join(parts[:len(parts)-1], " ") + ", " + state
	}

	return strings.Join(parts, " ")
}
