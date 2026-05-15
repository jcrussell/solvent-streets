package geo

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestParallelMap_Empty(t *testing.T) {
	result := ParallelMap(context.Background(), []int{}, func(_ int, v int) []int {
		return []int{v}
	}, nil)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

func TestParallelMap_Identity(t *testing.T) {
	input := []int{1, 2, 3, 4, 5}
	result := ParallelMap(context.Background(), input, func(_ int, v int) []int {
		return []int{v}
	}, nil)
	if len(result) != len(input) {
		t.Fatalf("expected %d results, got %d", len(input), len(result))
	}
	// errgroup-based ParallelMap preserves input order via index-keyed slots.
	for i, v := range input {
		if result[i] != v {
			t.Errorf("result[%d] = %d, want %d (order must be preserved)", i, result[i], v)
		}
	}
}

func TestParallelMap_Filter(t *testing.T) {
	input := []int{1, 2, 3, 4, 5, 6}
	// Only keep even numbers
	result := ParallelMap(context.Background(), input, func(_ int, v int) []int {
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
	ParallelMap(context.Background(), input, func(_ int, _ int) []int {
		return nil
	}, &counter)
	if counter.Load() != 100 {
		t.Errorf("expected counter=100, got %d", counter.Load())
	}
}

// TestParallelMap_CancelStopsDispatch covers the errgroup.WithContext
// adoption: when ctx is cancelled before/during dispatch, in-flight workers
// return early via gctx.Err() check; remaining items are not processed.
func TestParallelMap_CancelStopsDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	input := make([]int, 1000)
	var counter atomic.Int64
	ParallelMap(ctx, input, func(_ int, _ int) []int {
		return nil
	}, &counter)
	// With ctx pre-cancelled, the gctx.Err() guard inside each goroutine
	// trips immediately. Some goroutines may have raced past the guard
	// (errgroup's SetLimit dispatches before our check), so we don't
	// assert counter == 0; we assert "not all 1000 ran" as a smoke test
	// that the cancellation path is wired.
	if counter.Load() == 1000 {
		t.Errorf("expected at least some workers to short-circuit on pre-cancelled ctx, got %d", counter.Load())
	}
}
