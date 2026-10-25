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

// CheckNamespaceAccess partitions namespace access over a caller's cached
// decisions and fresh checks. cached returns a remembered decision; check
// runs one and reports whether the apiserver answered. budget is shared by
// one request's calls. A namespace past the budget, or whose check failed,
// comes back unchecked rather than denied.
func CheckNamespaceAccess(namespaces []string, budget *atomic.Int32, cached func(ns string) (allowed, ok bool), check func(ns string) (allowed, answered bool)) (allowed, unchecked []string) {
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
