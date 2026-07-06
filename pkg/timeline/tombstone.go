package timeline

import (
	"container/list"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// TombstoneEntry is the last-known enrichment data for a resource, retained
// briefly after the object leaves the informer cache. Delete-time events and
// late K8s events (the "Killing" class, processed once the involved object is
// already gone from cache) consult it so they carry owner/labels/createdAt as
// fact instead of shipping anonymous.
type TombstoneEntry struct {
	Owner     *OwnerInfo
	Labels    map[string]string
	CreatedAt *time.Time
}

type tombstoneNode struct {
	key     string
	entry   TombstoneEntry
	expires time.Time
}

// TombstoneCache is a bounded, TTL-scoped, LRU-evicting cache of resource
// enrichment data keyed by kind/namespace/name. It is fed from informer
// add/update/delete callbacks and consulted during event enrichment, so it is
// safe for concurrent use. Misses (never-seen, expired, or LRU-evicted) return
// ok=false and enrichment silently falls back to other sources — the same
// null-carrying behavior as before the cache existed.
type TombstoneCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	capacity int
	ll       *list.List               // front = most-recently used
	items    map[string]*list.Element // key -> element holding *tombstoneNode
	now      func() time.Time         // overridable in tests for deterministic TTL
}

// NewTombstoneCache returns a cache holding at most capacity entries, each
// valid for ttl after its last write.
func NewTombstoneCache(ttl time.Duration, capacity int) *TombstoneCache {
	if capacity < 1 {
		capacity = 1
	}
	return &TombstoneCache{
		ttl:      ttl,
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		now:      time.Now,
	}
}

func tombstoneKey(kind, namespace, name string) string {
	return kind + "\x00" + namespace + "\x00" + name
}

// Put records (or refreshes) the enrichment for a resource, resetting its TTL
// and marking it most-recently-used. Over-capacity writes evict the LRU entry.
func (c *TombstoneCache) Put(kind, namespace, name string, entry TombstoneEntry) {
	if c == nil {
		return
	}
	key := tombstoneKey(kind, namespace, name)

	c.mu.Lock()
	defer c.mu.Unlock()

	expires := c.now().Add(c.ttl)
	if el, ok := c.items[key]; ok {
		node := el.Value.(*tombstoneNode)
		node.entry = entry
		node.expires = expires
		c.ll.MoveToFront(el)
		return
	}

	node := &tombstoneNode{key: key, entry: entry, expires: expires}
	c.items[key] = c.ll.PushFront(node)

	for c.ll.Len() > c.capacity {
		if back := c.ll.Back(); back != nil {
			c.removeElement(back)
		}
	}
}

// Get returns the enrichment for a resource. A miss, or an entry past its TTL,
// returns ok=false; expired entries are dropped on read.
func (c *TombstoneCache) Get(kind, namespace, name string) (TombstoneEntry, bool) {
	if c == nil {
		return TombstoneEntry{}, false
	}
	key := tombstoneKey(kind, namespace, name)

	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return TombstoneEntry{}, false
	}
	node := el.Value.(*tombstoneNode)
	if c.now().After(node.expires) {
		c.removeElement(el)
		return TombstoneEntry{}, false
	}
	c.ll.MoveToFront(el)
	return node.entry, true
}

// Len reports the number of retained entries, including any not yet lazily
// expired. Introspection/test helper.
func (c *TombstoneCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Clear drops all retained entries in place, keeping the same cache instance so
// concurrent readers holding the pointer never observe a torn reassignment.
func (c *TombstoneCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = make(map[string]*list.Element)
}

func (c *TombstoneCache) removeElement(el *list.Element) {
	c.ll.Remove(el)
	delete(c.items, el.Value.(*tombstoneNode).key)
}

// ExtractTombstoneEntry pulls owner/labels/createdAt off an informer object for
// tombstone storage. It unwraps a cache.DeletedFinalStateUnknown so the delete
// path — whose payload is often that wrapper — still yields the final object's
// data. ok is false for anything that isn't a metav1.Object.
func ExtractTombstoneEntry(obj any) (TombstoneEntry, bool) {
	if tomb, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tomb.Obj
	}
	meta, ok := obj.(metav1.Object)
	if !ok {
		return TombstoneEntry{}, false
	}
	entry := TombstoneEntry{
		Owner:  ExtractOwner(obj),
		Labels: ExtractLabels(obj),
	}
	if ct := meta.GetCreationTimestamp().Time; !ct.IsZero() {
		entry.CreatedAt = &ct
	}
	return entry, true
}
