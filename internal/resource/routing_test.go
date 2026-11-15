package resource

import (
	"strings"
	"testing"
)

// TestRoutingRoads_OverpassQueryExcluding (solvent-streets-530m): the ring
// query selects the same road classes as the full query, minus those over
// the excluded bbox, in one Overpass difference statement.
func TestRoutingRoads_OverpassQueryExcluding(t *testing.T) {
	r := &RoutingRoads{}
	padded := [4]float64{37.58, -121.90, 37.78, -121.63}
	inner := [4]float64{37.62, -121.84, 37.73, -121.69}
	q := r.OverpassQueryExcluding(padded, inner)
	want := "(\n  " + routingRoadsFilter + "(37.580000,-121.900000,37.780000,-121.630000);\n  - " +
		routingRoadsFilter + "(37.620000,-121.840000,37.730000,-121.690000);\n);\nout geom;"
	if !strings.HasSuffix(q, want) {
		t.Errorf("query =\n%s\nwant it to end with\n%s", q, want)
	}
	if !strings.Contains(r.OverpassQuery(padded), routingRoadsFilter+"(37.580000,-121.900000,37.780000,-121.630000)") {
		t.Errorf("OverpassQuery lost the filter:\n%s", r.OverpassQuery(padded))
	}
}
