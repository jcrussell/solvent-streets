package resource

import (
	"context"
	"strconv"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

// OwnSourced is implemented by a Source whose data comes from one fixed
// source regardless of the city's overpass / arcgis_url settings: facility
// points always come from Overpass, census blocks always from TIGERweb.
// Ingest resolves such a resource to that source alone (ingest.SourcesFor),
// so a city's pavement source choices neither add sources to it nor take
// them away.
//
// Pavement-ness is a separate question: only the types in All are swept by
// `pvmt gc` against the city's configured sources, so an own-sourced type
// (never in All) is never stale on the pavement sources' account.
type OwnSourced interface {
	// OwnSource is the name of the ingest source (ingest.Source.Name, and
	// the source_api written on its rows).
	OwnSource() string
}

// OwnSource implements OwnSourced: facilities are always fetched from
// Overpass, even for a city that sources its pavement from ArcGIS.
func (f *Facility) OwnSource() string { return "overpass" }

var _ OwnSourced = (*Facility)(nil)

// layerPrefix namespaces attribute-layer resource types: polygons that carry
// numeric attributes for a per-hex metric (census blocks) rather than paved
// surface. Hyphenated for the same reason as facilityPrefix.
const layerPrefix = "layer-"

// LayerType returns the resource type an attribute layer is stored under:
// "layer-census-blocks". Like facilities these are deliberately NOT in All.
func LayerType(name string) Type {
	return Type(layerPrefix + name)
}

// IsLayer reports whether t is an attribute-layer type.
func (t Type) IsLayer() bool {
	return strings.HasPrefix(string(t), layerPrefix)
}

// TypeCensusBlocks is the resource type of 2020 Census blocks.
var TypeCensusBlocks = LayerType("census-blocks")

// Census block attribute tags, as TIGERweb names them.
const (
	TagGEOID  = "GEOID"
	TagPOP100 = "POP100" // 2020 population count
	TagHU100  = "HU100"  // 2020 housing-unit count
)

// CensusBlocks is the Source for 2020 Census block polygons with their
// population and housing-unit counts (ingest: TIGERweb). It has no paved
// surface; BufferFeaturesPaired projects and cleans the polygons, unbuffered,
// so the population compute can apportion their counts onto hexes by area.
type CensusBlocks struct{}

var (
	_ Source     = (*CensusBlocks)(nil)
	_ OwnSourced = (*CensusBlocks)(nil)
)

func (*CensusBlocks) Type() Type                      { return TypeCensusBlocks }
func (*CensusBlocks) HasCohorts() bool                { return false }
func (*CensusBlocks) AsphaltSurfaced() bool           { return false }
func (*CensusBlocks) OwnSource() string               { return "tigerweb" }
func (*CensusBlocks) OverpassQuery([4]float64) string { return "" }

// BufferFeaturesPaired projects each block polygon and cleans it (Buffer(0)
// via validatePolygonOK), without buffering — the same treatment as parking
// lots. Non-polygonal or invalid rows are dropped.
func (*CensusBlocks) BufferFeaturesPaired(ctx context.Context, features []Feature, proj geo.Projector) []BufferedFeature {
	return geo.ParallelMap(ctx, features, func(_ int, f Feature) []BufferedFeature {
		g, gtype, err := geo.GeoJSONToProjectedGeometry(f.GeometryJSON, proj)
		if err != nil || (gtype != GeomPolygon && gtype != GeomMultiPolygon) {
			return nil
		}
		cleaned, ok := validatePolygonOK(g)
		if !ok {
			return nil
		}
		return []BufferedFeature{{Feature: f, Geom: cleaned}}
	}, nil)
}

// CensusCounts returns a block's population and housing units from its tags;
// a missing or malformed count reads as 0.
func CensusCounts(f Feature) (population, housingUnits float64) {
	return tagNumber(f.Tags, TagPOP100), tagNumber(f.Tags, TagHU100)
}

func tagNumber(tags map[string]string, key string) float64 {
	v, err := strconv.ParseFloat(tags[key], 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}
