package topology

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Memoizer is a short-TTL cache for Topology builds. The Topology graph is a
// deterministic projection of the Kubernetes informer cache: same provider
// state + same options → identical output. Building it requires walking every
// resource of every kind, so a TTL of even a few seconds is enough to absorb
// the typical request bursts (page load fetching tree+insights, the in-flight
// polling tick, dashboard widgets refreshing) without any user-visible
// staleness — controllers reconcile much more slowly than the TTL.
//
// The Memoizer does NOT own the underlying Builder. Callers pass a build
// closure each Get(); on a hit the closure is never invoked. This keeps the
// cache decoupled from how callers construct providers/builders.
type Memoizer struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]*memoEntry
}

type memoEntry struct {
	topo    *Topology
	builtAt time.Time
}

// NewMemoizer returns a Memoizer with the given TTL. A zero or negative TTL
// disables caching (every Get rebuilds), useful for tests.
func NewMemoizer(ttl time.Duration) *Memoizer {
	return &Memoizer{ttl: ttl, entries: make(map[string]*memoEntry)}
}

// Get returns a cached Topology if a fresh entry exists for opts, otherwise
// invokes build, stores the result, and returns it. Errors from build are
// not cached.
func (m *Memoizer) Get(opts BuildOptions, build func() (*Topology, error)) (*Topology, error) {
	if m == nil || m.ttl <= 0 {
		return build()
	}
	key := memoKey(opts)
	m.mu.Lock()
	if e, ok := m.entries[key]; ok && time.Since(e.builtAt) < m.ttl {
		topo := e.topo
		m.mu.Unlock()
		return topo, nil
	}
	m.mu.Unlock()

	topo, err := build()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.entries[key] = &memoEntry{topo: topo, builtAt: time.Now()}
	// Bound the map: drop entries older than 2× the TTL on every write.
	// A naive sweep is fine here — entry count is dominated by the number
	// of distinct (namespace-set, view-mode, flags) tuples in flight at
	// once, which is small (handful of namespace filters per active session).
	cutoff := 2 * m.ttl
	for k, e := range m.entries {
		if time.Since(e.builtAt) > cutoff {
			delete(m.entries, k)
		}
	}
	m.mu.Unlock()
	return topo, nil
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
