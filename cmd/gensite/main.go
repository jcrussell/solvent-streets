// Command gensite generates a multi-example static site for GitHub Pages.
// It discovers example configs under the examples directory, exports each
// using the export library, writes shared WASM assets once at the site root,
// and generates a landing page linking to each example.
package main

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/export"
)

type exampleInfo struct {
	Slug       string
	Title      string
	CityNames  string
	CityCount  int
	HexEdgeM   int
	UnitSystem string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gensite:", err)
		os.Exit(1)
	}
}

func run() error {
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
	defer rootDB.Close()

	// Clean and create output directory (refuse to wipe directories that
	// don't look like a previously generated site).
	if err := safeCleanDir(*outputDir); err != nil {
		return err
	}

	var examples []exampleInfo

	for _, cfgPath := range matches {
		slug := filepath.Base(filepath.Dir(cfgPath))
		fmt.Printf("=== Exporting %s ===\n", slug)

		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config %s: %w", cfgPath, err)
		}

		entries, err := export.LookupCityEntries(ctx, rootDB, cfg, cfg.Cities)
		if err != nil {
			return fmt.Errorf("build entries for %s: %w", slug, err)
		}
		if len(entries) == 0 {
			return fmt.Errorf("no city data for %s — run 'pvmt ingest' first", slug)
		}

		outDir := filepath.Join(*outputDir, slug)
		exporter := export.New(entries, cfg, outDir, cfg.UnitSystem().String())
		if err := exporter.SetWasmPrefix("../"); err != nil {
			return fmt.Errorf("set WASM prefix: %w", err)
		}
		exporter.SetSkipWasm(true)

		if err := exporter.Run(ctx); err != nil {
			return fmt.Errorf("export %s: %w", slug, err)
		}

		// Collect metadata for landing page
		var cityNames []string
		for _, c := range cfg.Cities {
			cityNames = append(cityNames, c.Name)
		}

		hexEdge := int(cfg.HexEdge())
		unitSys := "imperial"
		if cfg.UnitSystem().String() == "metric" {
			unitSys = "metric"
		}

		examples = append(examples, exampleInfo{
			Slug:       slug,
			Title:      formatTitle(slug),
			CityNames:  strings.Join(cityNames, ", "),
			CityCount:  len(cfg.Cities),
			HexEdgeM:   hexEdge,
			UnitSystem: unitSys,
		})
	}

	// Write shared WASM assets at site root
	if err := export.WriteSharedWasmAssets(*outputDir); err != nil {
		return fmt.Errorf("write shared WASM: %w", err)
	}

	// Generate landing page
	if err := renderLanding(*outputDir, examples); err != nil {
		return fmt.Errorf("render landing page: %w", err)
	}

	fmt.Printf("Site exported to %s/ (%d examples)\n", *outputDir, len(examples))
	return nil
}

func renderLanding(outputDir string, examples []exampleInfo) (err error) {
	tmplData, err := export.TemplateFS().ReadFile("templates/landing.html.tmpl")
	if err != nil {
		return fmt.Errorf("read landing template: %w", err)
	}

	tmpl, err := template.New("landing").Parse(string(tmplData))
	if err != nil {
		return fmt.Errorf("parse landing template: %w", err)
	}

	f, err := os.Create(filepath.Join(outputDir, "index.html"))
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close landing page: %w", cerr)
		}
	}()

	return tmpl.Execute(f, struct {
		Examples []exampleInfo
	}{
		Examples: examples,
	})
}

// safeCleanDir removes outputDir only if it is empty or looks like a
// previously generated site (contains index.html). It then re-creates it.
func safeCleanDir(outputDir string) error {
	abs, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve output dir: %w", err)
	}
	// Never wipe the filesystem root or home directory.
	if abs == "/" {
		return fmt.Errorf("refusing to use %q as output directory", abs)
	}
	if home, err := os.UserHomeDir(); err == nil && abs == home {
		return fmt.Errorf("refusing to use %q as output directory", abs)
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
