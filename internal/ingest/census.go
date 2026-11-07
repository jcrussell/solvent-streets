package ingest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// tigerwebCensus2020Blocks is the 2020 Census blocks layer of TIGERweb's
// Census 2020 service. That service is fixed to the 2020 vintage; the
// "current" Tracts_Blocks service renumbers its layers as yearly vintages are
// added, so it is not used.
const tigerwebCensus2020Blocks = "https://tigerweb.geo.census.gov/arcgis/rest/services/TIGERweb/tigerWMS_Census2020/MapServer/10/query"

// CensusBlocksSource fetches 2020 Census blocks with their population and
// housing-unit counts from TIGERweb. It is an adapter over ArcGISQuery that
// names only the fields the population compute needs, skips blocks with no
// people and no homes server-side, and asks for ~1 m generalized geometry:
// a large city is tens of thousands of blocks.
type CensusBlocksSource struct {
	BBox     [4]float64 // [south, west, north, east]
	URL      string     // override the TIGERweb layer (tests); empty uses the default
	Progress io.Writer
	// AllowPrivate skips the SSRF guard (tests against httptest servers).
	AllowPrivate bool
}

var _ Source = (*CensusBlocksSource)(nil)

func (s *CensusBlocksSource) Name() string { return "tigerweb" }

func (s *CensusBlocksSource) Fetch(ctx context.Context, client *http.Client, rt resource.Source) ([]db.Feature, error) {
	endpoint := s.URL
	if endpoint == "" {
		endpoint = tigerwebCensus2020Blocks
	}
	q := ArcGISQuery{
		Endpoint:           endpoint,
		BBox:               s.BBox,
		Where:              resource.TagPOP100 + " > 0 OR " + resource.TagHU100 + " > 0",
		OutFields:          []string{resource.TagGEOID, resource.TagPOP100, resource.TagHU100},
		IDField:            resource.TagGEOID,
		IDPrefix:           "census",
		GeometryPrecision:  6,
		MaxAllowableOffset: 0.00001,
		AllowPrivate:       s.AllowPrivate,
		Progress:           s.Progress,
	}
	rows, err := q.FetchAll(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("census blocks: %w", err)
	}
	now := time.Now()
	features := make([]db.Feature, 0, len(rows))
	for _, r := range rows {
		features = append(features, db.Feature{
			ID:           r.ID,
			ResourceType: rt.Type(),
			Name:         r.Props[resource.TagGEOID],
			Tags:         r.Props,
			GeometryJSON: string(r.Geometry),
			SourceAPI:    s.Name(),
			FetchedAt:    now,
		})
	}
	return features, nil
}

// SourcesFor resolves the ingest sources for one resource in one city. An
// own-sourced resource (resource.OwnSourced: facilities, census blocks)
// always gets exactly its own source, whatever the city's overpass /
// arcgis_url settings; every other resource gets the city's configured
// sources (AllSources).
func SourcesFor(rt resource.Source, bbox [4]float64, overpass bool, arcgisURL string, opts Options) []Source {
	if own, ok := rt.(resource.OwnSourced); ok {
		if src := ownSource(own.OwnSource(), bbox, opts); src != nil {
			return []Source{src}
		}
		return nil
	}
	return AllSources(bbox, overpass, arcgisURL, opts)
}

// ownSource builds the named fixed source, or nil for an unknown name.
func ownSource(name string, bbox [4]float64, opts Options) Source {
	switch name {
	case sourceOverpass:
		return &OverpassSource{BBox: bbox}
	case "tigerweb":
		return &CensusBlocksSource{BBox: bbox, Progress: opts.Progress}
	}
	return nil
}
