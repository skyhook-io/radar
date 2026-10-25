package readscope

import (
	"context"
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
func BudgetedNamespaceAccess(ctx context.Context, namespaces []string, budget *atomic.Int32, cached func(ns string) (allowed, ok bool), check func(ns string) (allowed, answered bool)) (allowed, unchecked []string) {
	var pending []string
	for _, ns := range namespaces {
		if v, ok := cached(ns); ok {
			if v {
				allowed = append(allowed, ns)
			}
			continue
		}
		if ctx.Err() != nil || budget.Add(-1) < 0 {
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
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			mu.Lock()
			unchecked = append(unchecked, ns)
			mu.Unlock()
			continue
		}
		if ctx.Err() != nil {
			<-sem
			mu.Lock()
			unchecked = append(unchecked, ns)
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(ns string) {
			defer wg.Done()
			defer func() { <-sem }()
			v, answered := false, false
			if ctx.Err() == nil {
				v, answered = check(ns)
			}
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

// CheckNamespacedAccess checks exact-kind access with one shared fresh-check
// budget. Cached cluster grants avoid namespace checks altogether. An empty
// namespace denotes a whole-scope decision when the scope cannot be enumerated.
func CheckNamespacedAccess(ctx context.Context, namespaces []string, budget *atomic.Int32, cached func(string) (bool, bool), check func(string) (bool, bool)) (allowed, denied, unchecked []string) {
	all, unknown := BudgetedNamespaceAccess(ctx, []string{""}, budget, cached, check)
	if len(all) > 0 {
		return namespaces, nil, nil
	}
	if len(unknown) > 0 && namespaces == nil {
		return []string{}, nil, []string{""}
	}
	if namespaces == nil {
		return []string{}, []string{""}, nil
	}
	if len(unknown) > 0 {
		// A failed cluster check cannot invalidate known namespace decisions.
		// Leave the rest unchecked without starting more reviews.
		budget = new(atomic.Int32)
	}
	allowed, unchecked = BudgetedNamespaceAccess(ctx, namespaces, budget, cached, check)
	decided := make(map[string]bool, len(allowed)+len(unchecked))
	for _, ns := range allowed {
		decided[ns] = true
	}
	for _, ns := range unchecked {
		decided[ns] = true
	}
	for _, ns := range namespaces {
		if !decided[ns] {
			denied = append(denied, ns)
		}
	}
	sort.Strings(denied)
	return allowed, denied, unchecked
}
