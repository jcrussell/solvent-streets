package ingest

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"pvmt/internal/db"
)

// CSVSource ingests features from a local CSV file.
// Expects columns: id, name, geometry_json, and any additional columns become tags.
type CSVSource struct {
	Path         string
	ResourceType string
	IDColumn     string // column name for feature ID, default "id"
	NameColumn   string // column name for feature name, default "name"
	GeomColumn   string // column name for GeoJSON geometry, default "geometry_json"
}

func (s *CSVSource) Load() ([]db.Feature, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, fmt.Errorf("open csv %s: %w", s.Path, err)
	}
	defer func() { _ = f.Close() }()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1 // allow variable-length rows; short rows are skipped below
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read csv header: %w", err)
	}

	idCol := colIndex(header, s.IDColumn, "id")
	nameCol := colIndex(header, s.NameColumn, "name")
	geomCol := colIndex(header, s.GeomColumn, "geometry_json")

	if idCol < 0 {
		return nil, fmt.Errorf("id column not found in CSV")
	}
	if geomCol < 0 {
		return nil, fmt.Errorf("geometry column not found in CSV")
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

		tags := make(map[string]string)
		for i, col := range header {
			if i < len(record) && i != idCol && i != nameCol && i != geomCol {
				tags[col] = record[i]
			}
		}

		if idCol >= len(record) || geomCol >= len(record) {
			continue // skip malformed row with fewer fields than header
		}

		name := ""
		if nameCol >= 0 && nameCol < len(record) {
			name = record[nameCol]
		}

		features = append(features, db.Feature{
			ID:           record[idCol],
			ResourceType: s.ResourceType,
			Name:         name,
			Tags:         tags,
			GeometryJSON: record[geomCol],
			SourceAPI:    "csv:" + s.Path,
			FetchedAt:    time.Now(),
		})
	}
	return features, nil
}

// GeoJSONFileSource ingests features from a local GeoJSON file.
type GeoJSONFileSource struct {
	Path         string
	ResourceType string
	IDProperty   string // property name for feature ID, default "id"
}

func (s *GeoJSONFileSource) Load() ([]db.Feature, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return nil, fmt.Errorf("read geojson %s: %w", s.Path, err)
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
			SourceAPI:    "geojson:" + s.Path,
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
