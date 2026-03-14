package db

import (
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	features := []Feature{
		{ID: "osm:way:1", ResourceType: "roads", Name: "Main St", Tags: map[string]string{"highway": "primary"}, GeometryJSON: `{"type":"LineString","coordinates":[[-121.76,37.68],[-121.75,37.68]]}`, SourceAPI: "overpass", FetchedAt: time.Now()},
		{ID: "osm:way:2", ResourceType: "roads", Name: "Oak Ave", Tags: map[string]string{"highway": "residential"}, GeometryJSON: `{"type":"LineString","coordinates":[[-121.76,37.69],[-121.75,37.69]]}`, SourceAPI: "overpass", FetchedAt: time.Now()},
	}

	if err := store.UpsertFeatures("roads", features); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListFeatures("roads")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 features, got %d", len(got))
	}

	// Upsert same features — should update, not duplicate
	if err := store.UpsertFeatures("roads", features); err != nil {
		t.Fatal(err)
	}
	got, err = store.ListFeatures("roads")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 features after upsert, got %d", len(got))
	}
}

func TestStoreComputeResult(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	result := ComputeResult{
		ResourceType:   "roads",
		TotalAreaSqFt:  1000000,
		TotalAreaAcres: 22.96,
		FeatureCount:   500,
		GeometryJSON:   `{"type":"Polygon","coordinates":[]}`,
	}
	if err := store.SaveComputeResult(result); err != nil {
		t.Fatal(err)
	}

	got, err := store.LatestComputeResult("roads")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalAreaSqFt != 1000000 {
		t.Errorf("expected area 1000000, got %f", got.TotalAreaSqFt)
	}
	if got.FeatureCount != 500 {
		t.Errorf("expected 500 features, got %d", got.FeatureCount)
	}
}

func TestStoreStats(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	features := []Feature{
		{ID: "1", Name: "test", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()},
	}
	if err := store.UpsertFeatures("parking", features); err != nil {
		t.Fatal(err)
	}

	info, err := store.Stats("parking")
	if err != nil {
		t.Fatal(err)
	}
	if info.FeatureCount != 1 {
		t.Errorf("expected 1 feature, got %d", info.FeatureCount)
	}
}

func TestStoreResourceTypes(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if err := store.UpsertFeatures("roads", []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFeatures("parking", []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}

	types, err := store.ResourceTypes()
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 {
		t.Errorf("expected 2 types, got %d", len(types))
	}
}
