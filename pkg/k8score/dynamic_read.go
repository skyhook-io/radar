package k8score

import (
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ReadWithSourceCheck runs one read and reports whether the same synced
// informers served its scope before and after it. The value and error are
// returned unchanged even when the read is not authoritative: a caller may
// accept a found object before sync, but a miss or list needs complete stores.
// With a nil cache the read still runs, but is never authoritative.
// It does not warm, wait, retry or validate the caller's access or connection.
func ReadWithSourceCheck[T any](cache *DynamicResourceCache, gvr schema.GroupVersionResource, namespace string, read func() (T, error)) (value T, authoritative bool, err error) {
	before, syncedBefore := cache.ReadSource(gvr, namespace)
	value, err = read()
	after, syncedAfter := cache.ReadSource(gvr, namespace)
	return value, syncedBefore && syncedAfter && before.Same(after), err
}

// DynamicReadSource identifies the informers serving reads of one GVR and
// namespace at a moment: their identity, not a snapshot of their objects.
// Starting a cluster-wide informer replaces a GVR's namespace informers
// inside the same cache, so two readiness checks that both pass can describe
// different informers; a read bracketed by two equal, synced sources was
// answered by stores that were complete throughout.
type DynamicReadSource struct {
	entries []*informerEntry
}

// Same reports whether s and other are the same informers, in any order.
func (s DynamicReadSource) Same(other DynamicReadSource) bool {
	if len(s.entries) != len(other.entries) {
		return false
	}
	for _, e := range s.entries {
		if !slices.Contains(other.entries, e) {
			return false
		}
	}
	return true
}

// ReadSource returns the informers serving reads of gvr in namespace now and
// whether they answer authoritatively, by IsNamespaceSynced's rules: namespace
// "" is a cluster-wide read, which only a cluster-wide informer answers. Both
// come from the same informers, so a replacement between them cannot mix them.
// A nil cache returns an empty source and false: never authoritative.
func (d *DynamicResourceCache) ReadSource(gvr schema.GroupVersionResource, namespace string) (DynamicReadSource, bool) {
	entries, ok := d.servedEntries(gvr, namespace)
	return DynamicReadSource{entries: entries}, ok
}
