package ingest

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

var timeoutRe = regexp.MustCompile(`\[timeout:(\d+)\]`)

// TestOverpassQueriesFitTheHeaderDeadline pins solvent-streets-e7h3: every
// Overpass query asks for at most resource.OverpassTimeoutS of server time,
// and the client waits longer than that for headers, so a slow query is never
// cut off and retried.
func TestOverpassQueriesFitTheHeaderDeadline(t *testing.T) {
	bbox := [4]float64{37.6, -121.8, 37.7, -121.7}
	sources := []resource.Source{&resource.Pavement{}, &resource.Parking{}, &resource.Sidewalk{},
		&resource.Facility{Selectors: []string{`["amenity"="fire_station"]`}}}
	for _, src := range sources {
		m := timeoutRe.FindStringSubmatch(src.OverpassQuery(bbox))
		if m == nil || m[1] != strconv.Itoa(resource.OverpassTimeoutS) {
			t.Errorf("%s query timeout = %v, want [timeout:%d]", src.Type(), m, resource.OverpassTimeoutS)
		}
	}
	// The boundary and water queries are built inline in this package.
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range timeoutRe.FindAllStringSubmatch(string(raw), -1) {
			if n, _ := strconv.Atoi(m[1]); n > resource.OverpassTimeoutS {
				t.Errorf("%s sends [timeout:%d], above resource.OverpassTimeoutS (%d)", f, n, resource.OverpassTimeoutS)
			}
		}
	}
	if OverpassHeaderTimeout <= time.Duration(resource.OverpassTimeoutS)*time.Second {
		t.Errorf("OverpassHeaderTimeout %v does not exceed the %ds server timeout", OverpassHeaderTimeout, resource.OverpassTimeoutS)
	}
}
