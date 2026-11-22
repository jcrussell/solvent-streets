package checksite

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/export"
)

// Every versioned data file at the format this binary's page reads passes.
func TestCheckSite_VersionsMatch(t *testing.T) {
	dir := buildValidRootSite(t)
	data := filepath.Join(dir, "cities", "demo", "data")
	writeJSONFile(t, filepath.Join(data, export.HexMetricsFileName), map[string]any{"v": export.HexMetricsFileVersion, "metrics": map[string]any{}})
	out, err := run(t, dir, false)
	if err != nil {
		t.Fatalf("a site at the current formats failed: %v\n%s", err, out)
	}
	for _, name := range []string{export.CompareFileName, export.HexGridFileName, export.HexMetricsFileName} {
		if len(find(out, "PASS", chkVersions, name)) != 1 || len(find(out, "", chkVersions, name)) != 1 {
			t.Errorf("want one versions verdict for %s, a PASS:\n%s", name, out)
		}
	}
}

// A data file of another format fails, naming the file, both versions and the fix.
func TestCheckSite_VersionMismatchFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(dir string) string
		want int
	}{
		{export.HexGridFileName, func(dir string) string { return filepath.Join(dir, "cities", "demo", "data", export.HexGridFileName) }, export.HexGridFileVersion},
		{export.HexMetricsFileName, func(dir string) string {
			return filepath.Join(dir, "cities", "demo", "data", export.HexMetricsFileName)
		}, export.HexMetricsFileVersion},
		{export.CompareFileName, func(dir string) string { return filepath.Join(dir, export.CompareFileName) }, export.CompareFileVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := buildValidRootSite(t)
			writeJSONFile(t, tc.path(dir), map[string]any{"v": tc.want - 1})
			// The failure names the file, the version it has and the one
			// the page reads.
			assertFails(t, dir, chkVersions, tc.name, fmt.Sprintf("v%d,", tc.want-1), fmt.Sprintf("v%d ", tc.want))
		})
	}
}

// readVersion finds "v" after a large value it steps over, reads a file with
// no "v" as 0, and refuses a document that is not an object.
func TestReadVersion(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		body string
		want int
		err  bool
	}{
		"after":   {`{"features":[{"properties":{"k":[1,{"x":"v"}]}}],"type":"FeatureCollection","v":6}`, 6, false},
		"first":   {`{"v":2,"metrics":{}}`, 2, false},
		"missing": {`{"type":"FeatureCollection","features":[]}`, 0, false},
		"array":   {`[1,2]`, 0, true},
		"broken":  {`{"features":[`, 0, true},
		"textual": {`{"v":"6"}`, 0, true},
	} {
		p := filepath.Join(dir, name+".json")
		writeFile(t, p, tc.body)
		got, err := readVersion(p)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("%s: readVersion = %d, %v; want %d, error %v", name, got, err, tc.want, tc.err)
		}
	}
}
