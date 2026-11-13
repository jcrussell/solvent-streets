package ingest

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

var timeoutRe = regexp.MustCompile(`\[timeout:(\d+)\]`)

// TestOverpassQueriesFitTheHeaderDeadline pins solvent-streets-e7h3: every
// Overpass query asks for exactly resource.OverpassTimeoutS of server time,
// and the client waits longer than that for headers, so a slow query is never
// cut off and retried.
func TestOverpassQueriesFitTheHeaderDeadline(t *testing.T) {
	bbox := [4]float64{37.6, -121.8, 37.7, -121.7}
	queries := map[string]string{
		"water":    buildWaterQuery(bbox),
		"boundary": buildBoundaryRelationQuery(171262),
	}
	for _, src := range []resource.Source{&resource.Pavement{}, &resource.Parking{}, &resource.Sidewalk{},
		&resource.Facility{Selectors: []string{`["amenity"="fire_station"]`}}} {
		queries[string(src.Type())] = src.OverpassQuery(bbox)
	}
	for name, q := range queries {
		m := timeoutRe.FindStringSubmatch(q)
		if m == nil || m[1] != strconv.Itoa(resource.OverpassTimeoutS) {
			t.Errorf("%s query timeout = %v, want [timeout:%d]", name, m, resource.OverpassTimeoutS)
		}
	}
	// No query in this package hard-codes its own timeout.
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := timeoutRe.FindString(string(raw)); m != "" {
			t.Errorf("%s hard-codes %s; use resource.OverpassTimeoutS", f, m)
		}
	}
	if OverpassHeaderTimeout <= time.Duration(resource.OverpassTimeoutS)*time.Second {
		t.Errorf("OverpassHeaderTimeout %v does not exceed the %ds server timeout", OverpassHeaderTimeout, resource.OverpassTimeoutS)
	}
}
