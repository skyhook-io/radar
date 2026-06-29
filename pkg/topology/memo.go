package topology

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Memoizer is a short-TTL cache for Topology builds. The Topology graph is a
// deterministic projection of the Kubernetes informer cache, so a few-second
// TTL absorbs typical request bursts (page load fetching tree+insights,
// in-flight polls, dashboard widgets) without user-visible staleness.
// Concurrent cold reads for the same key are deduped via singleflight so
// the underlying walk runs once per burst.
//
// The Memoizer does NOT own the underlying Builder. Callers pass a build
// closure each Get(); on a hit the closure is never invoked.
type Memoizer struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]*memoEntry
	group   singleflight.Group
}

type memoEntry struct {
	topo      *Topology
	builtAt   time.Time
	indexOnce sync.Once          // guards lazy build of index
	index     *RelationshipsIndex // populated by Memoizer.GetIndex on first call
}

// NewMemoizer returns a Memoizer with the given TTL. A zero or negative TTL
// disables caching (every Get rebuilds), useful for tests.
func NewMemoizer(ttl time.Duration) *Memoizer {
	return &Memoizer{ttl: ttl, entries: make(map[string]*memoEntry)}
}

// Get returns a cached Topology if a fresh entry exists for opts, otherwise
// invokes build, stores the result, and returns it. Errors from build are
// not cached. Concurrent callers with the same key share a single build call.
func (m *Memoizer) Get(opts BuildOptions, build func() (*Topology, error)) (*Topology, error) {
	if m == nil || m.ttl <= 0 {
		return build()
	}
	key := memoKey(opts)
	if topo, ok := m.lookup(key); ok {
		return topo, nil
	}

	v, err, _ := m.group.Do(key, func() (any, error) {
		// Re-check inside the singleflight critical section: a previous
		// caller may have populated the entry while we were waiting.
		if topo, ok := m.lookup(key); ok {
			return topo, nil
		}
		topo, err := build()
		if err != nil {
			return nil, err
		}
		m.store(key, topo)
		return topo, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*Topology), nil
}

func (m *Memoizer) lookup(key string) (*Topology, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok && time.Since(e.builtAt) < m.ttl {
		return e.topo, true
	}
	return nil, false
}

// GetIndex returns the RelationshipsIndex for the topology produced by build
// under opts. The index is computed lazily on first call and cached alongside
// the topology entry, so subsequent GetIndex calls for the same entry are O(1)
// map lookups. Falls through to an inline build when the underlying entry has
// been evicted between the topology fetch and the index fetch, or when the
// Memoizer is disabled (ttl ≤ 0).
func (m *Memoizer) GetIndex(opts BuildOptions, build func() (*Topology, error)) (*RelationshipsIndex, error) {
	topo, err := m.Get(opts, build)
	if err != nil {
		return nil, err
	}
	if m == nil || m.ttl <= 0 {
		return IndexByResource(topo), nil
	}
	key := memoKey(opts)
	m.mu.Lock()
	entry := m.entries[key]
	m.mu.Unlock()
	// Entry evicted between Get() and GetIndex() — build inline and return.
	// Costs one extra walk but keeps the API correct under aggressive eviction.
	if entry == nil || entry.topo != topo {
		return IndexByResource(topo), nil
	}
	entry.indexOnce.Do(func() {
		entry.index = IndexByResource(entry.topo)
	})
	return entry.index, nil
}

func (m *Memoizer) store(key string, topo *Topology) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = &memoEntry{topo: topo, builtAt: time.Now()}
	// Bound the map: drop entries older than 2× the TTL on every write.
	// Entry count is dominated by the number of distinct (namespace-set,
	// view-mode, flags) tuples in flight at once — small in practice.
	cutoff := 2 * m.ttl
	for k, e := range m.entries {
		if time.Since(e.builtAt) > cutoff {
			delete(m.entries, k)
		}
	}
}

// memoKey is the cache key. Includes every BuildOptions field that changes
// the resulting graph; if a new field is added to BuildOptions that affects
// output, it must be added here too or callers will get stale-shape data.
func memoKey(opts BuildOptions) string {
	ns := append([]string(nil), opts.Namespaces...)
	sort.Strings(ns)
	var b strings.Builder
	b.WriteString(string(opts.ViewMode))
	b.WriteByte('|')
	b.WriteString(strings.Join(ns, ","))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(opts.MaxIndividualPods))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(opts.IncludeSecrets))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(opts.IncludeConfigMaps))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(opts.IncludePVCs))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(opts.IncludeReplicaSets))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(opts.IncludeGenericCRDs))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(opts.ForRelationshipCache))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(opts.ShowPolicyEffect))
	return b.String()
}
