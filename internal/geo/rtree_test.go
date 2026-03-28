package geo

import (
	"sync"
	"testing"

	"github.com/peterstace/simplefeatures/geom"
)

func TestGeomIndex_SinglePolygon(t *testing.T) {
	rect := makeRect(0, 0, 10, 10)
	idx := NewGeomIndex(rect)
	if idx.Len() != 1 {
		t.Fatalf("expected 1 part, got %d", idx.Len())
	}

	// Search overlapping
	env := makeRect(5, 5, 15, 15).Envelope()
	results := idx.Search(env)
	if len(results) != 1 {
		t.Errorf("expected 1 result for overlapping search, got %d", len(results))
	}

	// Search non-overlapping
	env2 := makeRect(100, 100, 200, 200).Envelope()
	results2 := idx.Search(env2)
	if len(results2) != 0 {
		t.Errorf("expected 0 results for non-overlapping search, got %d", len(results2))
	}
}

func TestGeomIndex_MultiPolygon(t *testing.T) {
	// Create two separated rectangles and union them into a MultiPolygon
	r1 := makeRect(0, 0, 10, 10)
	r2 := makeRect(100, 100, 110, 110)
	union, err := geom.Union(r1, r2)
	if err != nil {
		t.Fatal(err)
	}

	idx := NewGeomIndex(union)
	if idx.Len() != 2 {
		t.Fatalf("expected 2 parts, got %d", idx.Len())
	}

	// Search near first rect only
	env := makeRect(-1, -1, 5, 5).Envelope()
	results := idx.Search(env)
	if len(results) != 1 {
		t.Errorf("expected 1 result near first rect, got %d", len(results))
	}

	// Search near second rect only
	env2 := makeRect(105, 105, 115, 115).Envelope()
	results2 := idx.Search(env2)
	if len(results2) != 1 {
		t.Errorf("expected 1 result near second rect, got %d", len(results2))
	}

	// Search covering both
	env3 := makeRect(-1, -1, 200, 200).Envelope()
	results3 := idx.Search(env3)
	if len(results3) != 2 {
		t.Errorf("expected 2 results covering both, got %d", len(results3))
	}
}

func TestGeomIndex_NoMatch(t *testing.T) {
	rect := makeRect(0, 0, 10, 10)
	idx := NewGeomIndex(rect)

	env := makeRect(50, 50, 60, 60).Envelope()
	results := idx.Search(env)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestGeomIndex_ConcurrentSearch(t *testing.T) {
	// Build index with several parts
	var geoms []geom.Geometry
	for i := range 10 {
		x := float64(i) * 20
		geoms = append(geoms, makeRect(x, 0, x+10, 10))
	}
	union, err := UnionAll(geoms)
	if err != nil {
		t.Fatal(err)
	}
	idx := NewGeomIndex(union)

	// Run concurrent searches
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			env := makeRect(0, 0, 200, 10).Envelope()
			results := idx.Search(env)
			if len(results) == 0 {
				t.Error("expected results from concurrent search")
			}
		})
	}
	wg.Wait()
}
