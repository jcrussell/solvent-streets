package ingest

import (
	"testing"
	"testing/fstest"
)

func TestCSVSource_ShortRowSkipped(t *testing.T) {
	// Row 2 has only 1 field, fewer than the id/geometry columns.
	content := `id,name,geometry_json
1,road1,"{""type"":""LineString"",""coordinates"":[[0,0],[1,1]]}"
short
2,road2,"{""type"":""LineString"",""coordinates"":[[2,2],[3,3]]}"
`
	fsys := fstest.MapFS{"test.csv": &fstest.MapFile{Data: []byte(content)}}

	src := &CSVSource{FS: fsys, Name: "test.csv", ResourceType: "roads"}
	features, err := src.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 2 {
		t.Errorf("expected 2 features (short row skipped), got %d", len(features))
	}
}
