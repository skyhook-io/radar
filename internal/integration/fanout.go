package integration

import (
	"context"
	"sync"
)

// fanOut calls run(i) for every i in [0, n), at most limit at a time, and
// returns the results in index order once every call has finished. A call
// still waiting for a slot when ctx is done does not run; its result is the
// zero value. Caps on n, ordering and what a skipped index means stay with
// the caller.
func FanOut[T any](ctx context.Context, n, limit int, run func(i int) T) []T {
	results := make([]T, n)
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			results[i] = run(i)
		}()
	}
	wg.Wait()
	return results
}
