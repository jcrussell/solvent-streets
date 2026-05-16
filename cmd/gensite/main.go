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
	"sort"
	"strings"

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
	flag.Parse()

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
	if err := safeCleanDir(*outputDir); err != nil {
		return err
	}

	var examples []export.ExampleInfo
	for _, cfgPath := range matches {
		info, err := exportExample(ctx, rootDB, cfgPath, *outputDir)
		if err != nil {
			return err
		}
		examples = append(examples, info)
	}

	// Write shared WASM assets at site root
	if err := export.WriteSharedWasmAssets(*outputDir); err != nil {
		return fmt.Errorf("write shared WASM: %w", err)
	}

	// Generate landing page
	if err := export.RenderLandingPage(*outputDir, examples); err != nil {
		return fmt.Errorf("render landing page: %w", err)
	}

	fmt.Printf("Site exported to %s/ (%d examples)\n", *outputDir, len(examples))
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

// safeCleanDir removes outputDir only if it is empty or looks like a
// previously generated site (contains index.html). It then re-creates it.
func safeCleanDir(outputDir string) error {
	if _, err := cmdutil.ResolveOutputDir(outputDir); err != nil {
		return err
	}

	info, err := os.Stat(outputDir)
	if os.IsNotExist(err) {
		return os.MkdirAll(outputDir, 0o755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("output path %q exists but is not a directory", outputDir)
	}

	// Allow removal if the directory contains index.html (our sentinel) or is empty.
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("read output dir: %w", err)
	}
	if len(entries) > 0 {
		hasIndex := false
		for _, e := range entries {
			if e.Name() == "index.html" {
				hasIndex = true
				break
			}
		}
		if !hasIndex {
			return fmt.Errorf("output directory %q is non-empty and does not look like a generated site (no index.html); refusing to delete", outputDir)
		}
	}

	if err := os.RemoveAll(outputDir); err != nil {
		return fmt.Errorf("clean output dir: %w", err)
	}
	return os.MkdirAll(outputDir, 0o755)
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
