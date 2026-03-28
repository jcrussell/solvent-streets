package geo

import (
	"sync/atomic"
	"testing"
)

func TestParallelMap_Empty(t *testing.T) {
	result := ParallelMap([]int{}, func(_ int, v int) []int {
		return []int{v}
	}, nil)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

func TestParallelMap_Identity(t *testing.T) {
	input := []int{1, 2, 3, 4, 5}
	result := ParallelMap(input, func(_ int, v int) []int {
		return []int{v}
	}, nil)
	if len(result) != len(input) {
		t.Fatalf("expected %d results, got %d", len(input), len(result))
	}
	// Results are in chunk order — values present but not necessarily in original order
	seen := make(map[int]bool)
	for _, v := range result {
		seen[v] = true
	}
	for _, v := range input {
		if !seen[v] {
			t.Errorf("missing value %d in results", v)
		}
	}
}

func TestParallelMap_Filter(t *testing.T) {
	input := []int{1, 2, 3, 4, 5, 6}
	// Only keep even numbers
	result := ParallelMap(input, func(_ int, v int) []int {
		if v%2 == 0 {
			return []int{v}
		}
		return nil
	}, nil)
	if len(result) != 3 {
		t.Fatalf("expected 3 even numbers, got %d", len(result))
	}
}

func TestParallelMap_Counter(t *testing.T) {
	input := make([]int, 100)
	var counter atomic.Int64
	ParallelMap(input, func(_ int, _ int) []int {
		return nil
	}, &counter)
	if counter.Load() != 100 {
		t.Errorf("expected counter=100, got %d", counter.Load())
	}
}
