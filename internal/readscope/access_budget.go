package readscope

import (
	"sort"
	"sync"
	"sync/atomic"
)

// MaxAccessChecks bounds uncached SubjectAccessReviews shared by one request.
// They share Radar's client rate limit with every other screen, and a caller
// can see hundreds of namespaces.
const MaxAccessChecks = 64

// BudgetedNamespaceAccess partitions a finite namespace set using cached
// decisions and fresh checks. Nil and empty inputs both mean no members; empty
// outputs may be nil. Denied members are omitted from both outputs. Results
// contain namespace names, never the "" whole-scope sentinel.
// budget is a per-request counter seeded with MaxAccessChecks and shared across
// kinds. Cached decisions cost nothing; exhausted or unanswered checks return
// unchecked. Both callbacks return (false, false) for no answer. Fresh callbacks
// run concurrently (at most 16) and must honor the request context they capture.
func BudgetedNamespaceAccess(namespaces []string, budget *atomic.Int32, cached func(ns string) (allowed, ok bool), check func(ns string) (allowed, answered bool)) (allowed, unchecked []string) {
	var pending []string
	for _, ns := range namespaces {
		if v, ok := cached(ns); ok {
			if v {
				allowed = append(allowed, ns)
			}
			continue
		}
		if budget.Add(-1) < 0 {
			unchecked = append(unchecked, ns)
			continue
		}
		pending = append(pending, ns)
	}
	const maxConcurrent = 16
	sem := make(chan struct{}, maxConcurrent)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ns := range pending {
		wg.Add(1)
		sem <- struct{}{}
		go func(ns string) {
			defer wg.Done()
			defer func() { <-sem }()
			v, answered := check(ns)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case !answered:
				unchecked = append(unchecked, ns)
			case v:
				allowed = append(allowed, ns)
			}
		}(ns)
	}
	wg.Wait()
	sort.Strings(allowed)
	sort.Strings(unchecked)
	return allowed, unchecked
}
