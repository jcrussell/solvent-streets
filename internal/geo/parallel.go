package geo

import (
	"context"
	"runtime"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
)

// ParallelMap applies fn to each element of input in parallel, capped at
// runtime.NumCPU() concurrent workers via errgroup.SetLimit (byob-lifecycle.3).
// Each fn call returns zero or more results; output preserves input order.
// If counter is non-nil it is incremented after each input item is processed,
// enabling external progress tracking.
//
// Cancellation: when ctx is cancelled, in-flight workers complete the item
// they have already started but no further items dispatch. The function then
// returns the partial result. fn itself is not passed ctx and is not
// expected to fail; the cancellation seam exists so a long compute run can
// abort cleanly on SIGINT instead of running to completion.
func ParallelMap[T any, R any](ctx context.Context, input []T, fn func(int, T) []R, counter *atomic.Int64) []R {
	n := len(input)
	if n == 0 {
		return nil
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())

	results := make([][]R, n)
	for i, v := range input {
		g.Go(func() error {
			// Return the ctx error (not nil) when cancelled so errgroup
			// propagates the signal to peers via gctx; g.Wait below
			// discards it because cancellation is not an "error" in the
			// errorless ParallelMap contract.
			if err := gctx.Err(); err != nil {
				return err
			}
			results[i] = fn(i, v)
			if counter != nil {
				counter.Add(1)
			}
			return nil
		})
	}
	_ = g.Wait()

	total := 0
	for _, r := range results {
		total += len(r)
	}
	out := make([]R, 0, total)
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}
