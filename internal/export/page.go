package export

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"os"

	"github.com/jcrussell/solvent-streets/internal/config"
)

// SiteTitle is the name a multi-city dashboard carries: [export] title, else
// fallback (the exporter passes its output folder's name).
func SiteTitle(cfg *config.Config, fallback string) string {
	if cfg.Export.Title != "" {
		return cfg.Export.Title
	}
	return fallback
}

// PageMetaAndSeed builds the MetaJSON and forecast seed a page embeds. One
// entry gives that city's own, under its resolved forecast settings. Several
// give the config-wide aggregates under the top-level forecast settings and
// title: a page that shows every city must not present one city's totals as
// the headline for the whole config.
func PageMetaAndSeed(ctx context.Context, cfg *config.Config, entries []CityEntry, title string) (MetaJSON, template.JS, error) {
	if len(entries) == 1 {
		entry := entries[0]
		meta, err := BuildMeta(ctx, entry, 0)
		if err != nil {
			return MetaJSON{}, "", err
		}
		fc := cfg.ResolvedForecast(&entry.City)
		seed, err := BuildForecastSeed(ctx, &fc, entry.Store)
		if err != nil {
			return MetaJSON{}, "", fmt.Errorf("build forecast seed: %w", err)
		}
		return meta, seed, nil
	}
	meta, err := BuildMultiCityMeta(ctx, entries, title)
	if err != nil {
		return MetaJSON{}, "", err
	}
	fc := cfg.ResolvedForecast(nil)
	seed, err := BuildMultiCityForecastSeed(ctx, &fc, entries)
	if err != nil {
		return MetaJSON{}, "", err
	}
	return meta, seed, nil
}

// NewTemplateData assembles what index.html and play.html render against, for
// the static export and the live server alike. cities is nil for a single-city
// site. The caller sets IsLiveServer and ActiveSlug, which only the live
// server uses.
func NewTemplateData(cfg *config.Config, meta MetaJSON, seed template.JS, cities []CityInfo, unitSystem string) (TemplateData, error) {
	methodology, err := MethodologyHTML()
	if err != nil {
		return TemplateData{}, fmt.Errorf("render methodology: %w", err)
	}
	// The Config tab shows the file as written beside the resolved settings;
	// an unreadable or absent file leaves the raw view empty.
	var rawTOML string
	if cfg.SourcePath != "" {
		if data, err := os.ReadFile(cfg.SourcePath); err == nil {
			rawTOML = string(data)
		}
	}
	date, ver := FooterInfo()
	return TemplateData{
		MetaJSON:        meta,
		ForecastSeed:    seed,
		LayerColors:     ResourceColorsJS(),
		RawTOML:         rawTOML,
		ResolvedTOML:    ResolvedTOML(cfg),
		UnitSystem:      unitSystem,
		Cities:          cities,
		TagDimensions:   GroupCitiesByDimension(cities),
		MethodologyHTML: methodology,
		GeneratedDate:   date,
		BuildVersion:    ver,
	}, nil
}

// RenderPage parses a page template (ParseIndexTemplate, ParseGameTemplate)
// and renders it against td into memory, so a failed Execute never leaves a
// partial page behind.
func RenderPage(parse func() (*template.Template, error), td TemplateData) ([]byte, error) {
	tmpl, err := parse()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, td); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
