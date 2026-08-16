package export

import (
	"context"
	"encoding/json"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

// BuildBoundaryGeoJSON builds the city's DISPLAY boundary FeatureCollection,
// shared by the static exporter and the live server so the two can't drift.
//
// The emitted geometry is simplified (export.boundary_simplify_m) and rounded
// (export.coordinate_decimals). It is for drawing only — the client uses it for
// a dashed line layer and fitBounds, nothing else. Every consumer that needs
// the real polygon (hex clipping, the city coverage scope, meta.json's
// CityArea/PctPaved) reads store.GetBoundary directly and is unaffected.
//
// Returns (nil, nil) when the city has no boundary stored, which both callers
// already handle: the exporter skips the file, the server returns an empty FC.
//
// UNUSUAL ERROR CONTRACT — read before adding a call site. This can return a
// USABLE FeatureCollection together with a NON-NIL error. That combination means
// "simplification was skipped, the raw boundary is in the map, warn if you have
// somewhere to warn". Do NOT write the reflexive `if err != nil { return err }`:
// every one of those cases (a stored Feature wrapper, a GeometryCollection, an
// antimeridian span) renders fine today, and failing on them would turn a
// working export into a hard error and a working request into a permanent 500.
// A nil map is the only signal that there is nothing to write. Only a
// GetBoundary failure returns (nil, err), and that one must propagate.
//
// proj is a PARAMETER rather than derived here on purpose. Deriving it means
// BBoxFromGeoJSON, which json.Unmarshals once per coordinate — 205,961 times
// for Jacksonville — and the export path has already paid that cost by the time
// it calls this. Both sibling builders (BuildHexGeoJSON, BuildPlayHexes) take a
// projector for the same reason.
//
// The two knobs are read from entry.Config, NOT from any caller-side config
// value. BuildCityEntries hands every entry the same *config.Config the
// Exporter holds, so reading them here is what keeps the exported bytes and the
// served bytes identical; resolving them at one call site and not the other
// would be a silent parity break.
func BuildBoundaryGeoJSON(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) (map[string]any, error) {
	boundaryGJSON, err := entry.Store.GetBoundary(ctx)
	if err != nil {
		return nil, err
	}
	if boundaryGJSON == "" {
		return nil, nil //nolint:nilnil // nil map = no boundary, distinct from the propagated DB error above
	}

	geometry := boundaryGJSON
	var simplifyErr error
	if proj != nil && entry.Config != nil {
		// SimplifyGeoJSONMeters returns the input unchanged alongside any
		// error, so this assignment is safe to make unconditionally: a failure
		// leaves `geometry` exactly as it was. The error is advisory — the
		// caller decides whether anyone is listening for a warning.
		geometry, simplifyErr = geo.SimplifyGeoJSONMeters(
			boundaryGJSON, proj,
			entry.Config.BoundarySimplifyM(),
			entry.Config.CoordinateDecimals(),
		)
	}

	return map[string]any{
		"type": "FeatureCollection",
		"features": []map[string]any{
			{
				"type":       "Feature",
				"geometry":   json.RawMessage(geometry),
				"properties": map[string]any{"type": "boundary"},
			},
		},
	}, simplifyErr
}
