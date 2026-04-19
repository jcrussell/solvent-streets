package ingest

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"

	"pvmt/internal/db"
)

// CSVSource ingests features from a CSV file accessed via fs.FS. In production,
// pass os.DirFS(dir). In tests, pass fstest.MapFS for hermetic fixtures.
// Label is used as the SourceAPI provenance tag; if empty, Name is used.
type CSVSource struct {
	FS           fs.FS
	Name         string // path within FS
	Label        string // provenance tag on emitted features; defaults to Name
	ResourceType string
	IDColumn     string // column name for feature ID, default "id"
	NameColumn   string // column name for feature name, default "name"
	GeomColumn   string // column name for GeoJSON geometry, default "geometry_json"
}

func (s *CSVSource) Load() ([]db.Feature, error) {
	data, err := fs.ReadFile(s.FS, s.Name)
	if err != nil {
		return nil, fmt.Errorf("read csv %s: %w", s.Name, err)
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1 // allow variable-length rows; short rows are skipped below
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read csv header: %w", err)
	}

	idCol := colIndex(header, s.IDColumn, "id")
	nameCol := colIndex(header, s.NameColumn, "name")
	geomCol := colIndex(header, s.GeomColumn, "geometry_json")

	if idCol < 0 {
		return nil, errors.New("id column not found in CSV")
	}
	if geomCol < 0 {
		return nil, errors.New("geometry column not found in CSV")
	}

	var features []db.Feature
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read csv row: %w", err)
		}

		if feat, ok := s.parseRow(header, record, idCol, nameCol, geomCol); ok {
			features = append(features, feat)
		}
	}
	return features, nil
}

func (s *CSVSource) parseRow(header, record []string, idCol, nameCol, geomCol int) (db.Feature, bool) {
	if idCol >= len(record) || geomCol >= len(record) {
		return db.Feature{}, false
	}

	tags := make(map[string]string)
	for i, col := range header {
		if i < len(record) && i != idCol && i != nameCol && i != geomCol {
			tags[col] = record[i]
		}
	}

	name := ""
	if nameCol >= 0 && nameCol < len(record) {
		name = record[nameCol]
	}

	label := s.Label
	if label == "" {
		label = s.Name
	}

	return db.Feature{
		ID:           record[idCol],
		ResourceType: s.ResourceType,
		Name:         name,
		Tags:         tags,
		GeometryJSON: record[geomCol],
		SourceAPI:    "csv:" + label,
		FetchedAt:    time.Now(),
	}, true
}

// GeoJSONFileSource ingests features from a GeoJSON file accessed via fs.FS.
// Label is used as the SourceAPI provenance tag; if empty, Name is used.
type GeoJSONFileSource struct {
	FS           fs.FS
	Name         string // path within FS
	Label        string // provenance tag on emitted features; defaults to Name
	ResourceType string
	IDProperty   string // property name for feature ID, default "id"
}

func (s *GeoJSONFileSource) Load() ([]db.Feature, error) {
	data, err := fs.ReadFile(s.FS, s.Name)
	if err != nil {
		return nil, fmt.Errorf("read geojson %s: %w", s.Name, err)
	}

	var fc struct {
		Features []struct {
			Properties map[string]any  `json:"properties"`
			Geometry   json.RawMessage `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parse geojson: %w", err)
	}

	idProp := s.IDProperty
	if idProp == "" {
		idProp = "id"
	}

	label := s.Label
	if label == "" {
		label = s.Name
	}

	var features []db.Feature
	for i, f := range fc.Features {
		if f.Geometry == nil {
			continue
		}

		tags := make(map[string]string)
		var name string
		id := fmt.Sprintf("geojson:%d", i)
		for k, v := range f.Properties {
			if v != nil {
				tags[k] = fmt.Sprintf("%v", v)
			}
			if k == idProp {
				id = fmt.Sprintf("%v", v)
			}
			if k == "name" || k == "NAME" {
				name = fmt.Sprintf("%v", v)
			}
		}

		features = append(features, db.Feature{
			ID:           id,
			ResourceType: s.ResourceType,
			Name:         name,
			Tags:         tags,
			GeometryJSON: string(f.Geometry),
			SourceAPI:    "geojson:" + label,
			FetchedAt:    time.Now(),
		})
	}
	return features, nil
}

func colIndex(header []string, name, fallback string) int {
	if name == "" {
		name = fallback
	}
	for i, h := range header {
		if h == name {
			return i
		}
	}
	return -1
}
