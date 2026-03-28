package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCSVSource_ShortRowSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.csv")
	// Row 2 has only 1 field, fewer than the id/geometry columns.
	content := `id,name,geometry_json
1,road1,"{""type"":""LineString"",""coordinates"":[[0,0],[1,1]]}"
short
2,road2,"{""type"":""LineString"",""coordinates"":[[2,2],[3,3]]}"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	src := &CSVSource{Path: path, ResourceType: "roads"}
	features, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 2 {
		t.Errorf("expected 2 features (short row skipped), got %d", len(features))
	}
}
