package vulnerabilities

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// parsedFindings caches each report's parsed findings by UID and
// resourceVersion. Parsing, with every finding's fix target, is most of a
// rollup's cost, and a report changes only when Trivy rescans, so every
// request re-parsing every report repeats the same work. An entry holds one
// report's own findings; which reports a caller reads is decided before the
// lookup, so the cache can't widen anyone's view. Callers share the slices
// and must not modify them.
var parsedFindings = &parseCache{entries: map[types.UID]parsedEntry{}}

// maxCachedFindings bounds the cache: twice what one request may read.
const maxCachedFindings = 2 * MaxFindings

// parsedIdle is how long an entry no read has used survives a sweep: a
// report that was deleted, or one only a past namespace scope read.
const parsedIdle = 30 * time.Minute

// ResetParseCache drops every parsed report, for a context switch: the
// previous cluster's reports would otherwise stay in memory until a read
// misses the cache.
func ResetParseCache() {
	c := parsedFindings
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries, c.total = map[types.UID]parsedEntry{}, 0
}

type parseCache struct {
	mu        sync.Mutex
	entries   map[types.UID]parsedEntry
	total     int
	lastSweep time.Time
}

type parsedEntry struct {
	resourceVersion string
	findings        []Finding
	used            time.Time
}

func (c *parseCache) get(u *unstructured.Unstructured) []Finding {
	uid, rv := u.GetUID(), u.GetResourceVersion()
	if uid == "" || rv == "" {
		return parseFindings(u)
	}
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[uid]; ok && e.resourceVersion == rv {
		e.used = now
		c.entries[uid] = e
		c.mu.Unlock()
		return e.findings
	}
	c.mu.Unlock()

	findings := parseFindings(u)
	if cap(findings) > len(findings) {
		// Deduplication can leave most of the backing array unused; the
		// bound counts what is held.
		findings = append(make([]Finding, 0, len(findings)), findings...)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[uid]; ok {
		c.total -= len(old.findings)
		delete(c.entries, uid)
	}
	if now.Sub(c.lastSweep) > time.Minute {
		c.lastSweep = now
		for id, e := range c.entries {
			if now.Sub(e.used) > parsedIdle {
				c.total -= len(e.findings)
				delete(c.entries, id)
			}
		}
	}
	if c.total+len(findings) > maxCachedFindings {
		clear(c.entries)
		c.total = 0
	}
	c.entries[uid] = parsedEntry{resourceVersion: rv, findings: findings, used: now}
	c.total += len(findings)
	return findings
}
