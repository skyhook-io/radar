package readscope

import (
	"slices"
	"sync"
)

// PartitionAccess splits namespaces into sorted, non-nil allowed, denied and
// unchecked sets. At most sixteen checks run concurrently; a failed check is
// unknown rather than a denial. Nil and empty inputs mean no members. There is
// no budget; cancellation is the callback's job.
func PartitionAccess(namespaces []string, check func(namespace string) (allowed, checked bool)) (allowed, denied, unchecked []string) {
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed, denied, unchecked = []string{}, []string{}, []string{}
	for _, ns := range namespaces {
		wg.Add(1)
		sem <- struct{}{}
		go func(ns string) {
			defer wg.Done()
			defer func() { <-sem }()
			ok, checked := check(ns)
			mu.Lock()
			switch {
			case ok:
				allowed = append(allowed, ns)
			case checked:
				denied = append(denied, ns)
			default:
				unchecked = append(unchecked, ns)
			}
			mu.Unlock()
		}(ns)
	}
	wg.Wait()
	slices.Sort(allowed)
	slices.Sort(denied)
	slices.Sort(unchecked)
	return allowed, denied, unchecked
}
