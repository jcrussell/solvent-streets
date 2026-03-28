package geo

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// ParallelMap applies fn to each element of input using a worker pool of
// runtime.NumCPU() goroutines. Each fn call returns zero or more results.
// If counter is non-nil it is incremented after each input item is processed,
// enabling external progress tracking.
func ParallelMap[T any, R any](input []T, fn func(int, T) []R, counter *atomic.Int64) []R {
	n := len(input)
	if n == 0 {
		return nil
	}

	numWorkers := runtime.NumCPU()
	if numWorkers > n {
		numWorkers = n
	}

	// Each worker gets a contiguous chunk and writes to its own slice.
	chunkSize := (n + numWorkers - 1) / numWorkers
	results := make([][]R, numWorkers)

	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for w := range numWorkers {
		go func(wIdx int) {
			defer wg.Done()
			start := wIdx * chunkSize
			end := start + chunkSize
			if end > n {
				end = n
			}
			var local []R
			for i := start; i < end; i++ {
				local = append(local, fn(i, input[i])...)
				if counter != nil {
					counter.Add(1)
				}
			}
			results[wIdx] = local
		}(w)
	}
	wg.Wait()

	// Flatten.
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
