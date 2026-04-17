package db

import (
	"context"
	"testing"
	"time"
)

// openTestStore opens an in-memory DB and returns a Store scoped to a test city.
func openTestStore(t *testing.T) Store {
	t.Helper()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := root.EnsureCity(context.Background(), "test-city", "Test City")
	if err != nil {
		t.Fatal(err)
	}
	return root.ForCity(id)
}

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	features := []Feature{
		{ID: "osm:way:1", ResourceType: "roads", Name: "Main St", Tags: map[string]string{"highway": "primary"}, GeometryJSON: `{"type":"LineString","coordinates":[[-121.76,37.68],[-121.75,37.68]]}`, SourceAPI: "overpass", FetchedAt: time.Now()},
		{ID: "osm:way:2", ResourceType: "roads", Name: "Oak Ave", Tags: map[string]string{"highway": "residential"}, GeometryJSON: `{"type":"LineString","coordinates":[[-121.76,37.69],[-121.75,37.69]]}`, SourceAPI: "overpass", FetchedAt: time.Now()},
	}

	if err := store.UpsertFeatures(ctx, "roads", features); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListFeatures(ctx, "roads")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 features, got %d", len(got))
	}

	// Upsert same features — should update, not duplicate
	if err := store.UpsertFeatures(ctx, "roads", features); err != nil {
		t.Fatal(err)
	}
	got, err = store.ListFeatures(ctx, "roads")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 features after upsert, got %d", len(got))
	}
}

func TestStoreComputeResult(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	result := ComputeResult{
		ResourceType: "roads",
		TotalAreaSqM: 92903,
		FeatureCount: 500,
		GeometryJSON: `{"type":"Polygon","coordinates":[]}`,
	}
	if err := store.SaveComputeResult(ctx, result); err != nil {
		t.Fatal(err)
	}

	got, err := store.LatestComputeResult(ctx, "roads")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalAreaSqM != 92903 {
		t.Errorf("expected area 92903, got %f", got.TotalAreaSqM)
	}
	if got.FeatureCount != 500 {
		t.Errorf("expected 500 features, got %d", got.FeatureCount)
	}
}

func TestStoreStats(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	features := []Feature{
		{ID: "1", Name: "test", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()},
	}
	if err := store.UpsertFeatures(ctx, "parking", features); err != nil {
		t.Fatal(err)
	}

	info, err := store.Stats(ctx, "parking")
	if err != nil {
		t.Fatal(err)
	}
	if info.FeatureCount != 1 {
		t.Errorf("expected 1 feature, got %d", info.FeatureCount)
	}
}

func TestStoreResourceTypes(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	if err := store.UpsertFeatures(ctx, "roads", []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFeatures(ctx, "parking", []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}

	types, err := store.ResourceTypes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 {
		t.Errorf("expected 2 types, got %d", len(types))
	}
}

func TestBoundaryRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	// No boundary initially
	got, err := store.GetBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty boundary, got %q", got)
	}

	// Save boundary
	gj := `{"type":"Polygon","coordinates":[[[-121.9,37.6],[-121.8,37.6],[-121.8,37.7],[-121.9,37.7],[-121.9,37.6]]]}`
	if err := store.SaveBoundary(ctx, gj, "nominatim"); err != nil {
		t.Fatal(err)
	}

	// Read back
	got, err = store.GetBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != gj {
		t.Errorf("boundary mismatch:\n got: %s\nwant: %s", got, gj)
	}

	// Upsert replaces
	gj2 := `{"type":"Polygon","coordinates":[[[-122.0,37.5],[-121.7,37.5],[-121.7,37.8],[-122.0,37.8],[-122.0,37.5]]]}`
	if err := store.SaveBoundary(ctx, gj2, "nominatim"); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != gj2 {
		t.Errorf("expected updated boundary")
	}
}

func TestCityIsolation(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	id1, err := root.EnsureCity(ctx, "city-a", "City A")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := root.EnsureCity(ctx, "city-b", "City B")
	if err != nil {
		t.Fatal(err)
	}

	storeA := root.ForCity(id1)
	storeB := root.ForCity(id2)

	// Insert features into city A
	if err := storeA.UpsertFeatures(ctx, "roads", []Feature{{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}

	// City B should see nothing
	got, err := storeB.ListFeatures(ctx, "roads")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("city B should have 0 features, got %d", len(got))
	}

	// City A should see its feature
	got, err = storeA.ListFeatures(ctx, "roads")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("city A should have 1 feature, got %d", len(got))
	}
}

func TestEnsureCityIdempotent(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	id1, err := root.EnsureCity(ctx, "livermore-ca", "Livermore, CA")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := root.EnsureCity(ctx, "livermore-ca", "Livermore, CA")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("expected same id, got %d and %d", id1, id2)
	}
}

func TestForeignKeyEnforcement(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	// Use a city_id that doesn't exist in the cities table.
	bogus := root.ForCity(9999)
	err = bogus.UpsertFeatures(ctx, "roads", []Feature{
		{ID: "1", Tags: map[string]string{}, GeometryJSON: `{}`, FetchedAt: time.Now()},
	})
	if err == nil {
		t.Fatal("expected FK violation error when inserting feature with nonexistent city_id")
	}
}

func TestListCities(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	// Migration seeds a "default" city, so start by counting baseline.
	baseline, err := root.ListCities(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := root.EnsureCity(ctx, "a", "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := root.EnsureCity(ctx, "b", "B"); err != nil {
		t.Fatal(err)
	}

	cities, err := root.ListCities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cities) != len(baseline)+2 {
		t.Errorf("expected %d cities, got %d", len(baseline)+2, len(cities))
	}
}
