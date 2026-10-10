package server

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// Reading Helm release issues means listing and decoding every Helm release
// Secret the caller can read. On a cluster with many or large releases that
// takes longer than an interactive caller waits, so partial=true requests
// read through this cache (stale-while-revalidate) instead of inline:
//
//   - at most one read per identity and namespace scope runs at a time, on its
//     own context capped at helmIssuesReadTimeout, never on a request's, and
//     at most helmIssuesMaxReads run at once across scopes;
//   - a request with an earlier result gets it at once, and starts a refresh
//     when that result is older than helmIssuesRefreshAfter;
//   - only a request with no result yet waits, and only until its budget;
//   - a caller that leaves neither cancels, restarts nor extends a read;
//   - after a failed read, the scope waits helmIssuesRetryAfterFailure
//     before trying again, so a cluster too slow for the cap doesn't rescan
//     on every poll.
//
// A cluster whose read finishes within helmIssuesReadTimeout therefore gets
// checked even when every request gives up first. One that never finishes
// within it reports each attempt as failed (timed out), never as checked.
var (
	helmIssuesReadTimeout       = 60 * time.Second
	helmIssuesRetryAfterFailure = 5 * time.Minute
	// helmIssuesRefreshAfter keeps a burst of requests (several tabs, a page
	// and its refetch) on one result instead of a read each.
	helmIssuesRefreshAfter = 10 * time.Second
	// helmIssuesEntryIdle drops scopes nobody has asked about for a while,
	// and helmIssuesMaxEntries caps how many are kept at all, so namespace
	// picks and departed users can't accumulate.
	helmIssuesEntryIdle  = 10 * time.Minute
	helmIssuesMaxEntries = 256
	// helmIssuesMaxReads caps background reads running at once. They outlive
	// the requests that start them, so without a cap many distinct scopes
	// could pile up full Secret scans on an already slow cluster. A read that
	// is due but can't start is reported as waiting for a slot.
	helmIssuesMaxReads = 8
)

// Why a Helm read failed, as stable codes the client words for people.
const (
	helmReadTimedOut  = issuesapi.HelmIssuesErrorTimeout
	helmReadForbidden = issuesapi.HelmIssuesErrorForbidden
	helmReadError     = issuesapi.HelmIssuesErrorFailed
)

type helmIssuesRead func(ctx context.Context) ([]issues.Issue, error)

type helmIssuesSnapshot struct {
	issues []issues.Issue
	at     time.Time
}

type helmIssuesEntry struct {
	last *helmIssuesSnapshot
	// failure is set when the newest finished read failed: a helmRead* code
	// and when it ended. Cleared by the next successful read.
	failure  string
	failedAt time.Time
	done     chan struct{} // non-nil while a read is running; closed when it ends
	lastUsed time.Time
}

// helmIssuesView is what a request may say about Helm for one scope.
type helmIssuesView struct {
	snapshot *helmIssuesSnapshot // newest successful result, if any
	reading  bool                // a read is running now
	// waiting: a read is due but every read slot is taken.
	waiting  bool
	failure  string // the newest finished read failed (helmRead* code)
	failedAt time.Time
}

type helmIssuesCache struct {
	mu         sync.Mutex
	generation uint64
	stopped    bool
	ctx        context.Context
	cancel     context.CancelFunc // cancels every read of the current generation
	entries    map[string]*helmIssuesEntry
	reads      int // background reads running now, across all entries
	now        func() time.Time
}

func newHelmIssuesCache() *helmIssuesCache {
	c := &helmIssuesCache{entries: map[string]*helmIssuesEntry{}, now: time.Now}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	return c
}

// helmIssues is the server's cache, created on first use. It is per server
// so Stop can retire it for good.
func (s *Server) helmIssues() *helmIssuesCache {
	s.helmIssuesOnce.Do(func() {
		s.helmIssuesCache = newHelmIssuesCache()
		// Clear at switch start and again at completion, so neither a read
		// that straddles the switch nor its result outlives the old cluster.
		s.helmIssuesCache.invalidateOn(k8s.OnBeforeContextSwitch, k8s.OnContextSwitch)
	})
	return s.helmIssuesCache
}

func (c *helmIssuesCache) invalidateOn(hooks ...func(k8s.ContextSwitchCallback)) {
	for _, hook := range hooks {
		hook(func(string) { c.invalidate() })
	}
}

// invalidate drops every result and stops every running read. A read that
// finishes afterwards belongs to an old generation and is discarded.
func (c *helmIssuesCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateLocked()
}

func (c *helmIssuesCache) invalidateLocked() {
	c.generation++
	c.reads = 0 // the old generation's reads no longer count; they are cancelled
	c.cancel()
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.entries = map[string]*helmIssuesEntry{}
}

// stop cancels every running read and refuses new ones, for server shutdown.
func (c *helmIssuesCache) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	c.invalidateLocked()
}

// get returns what is known about key's Helm issues. It starts a read when
// none is running and one is due: there is no result yet, or the result is
// older than helmIssuesRefreshAfter, or the newest read failed more than
// helmIssuesRetryAfterFailure ago. It waits for a running read only when
// there is no result yet, and only until wait is done (the request's budget,
// or its caller leaving).
func (c *helmIssuesCache) get(wait context.Context, key string, read helmIssuesRead) helmIssuesView {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return helmIssuesView{}
	}
	now := c.now()
	_, known := c.entries[key]
	c.evictLocked(now, !known)
	entry := c.entries[key] // idle eviction may just have dropped it
	if entry == nil {
		entry = &helmIssuesEntry{}
		c.entries[key] = entry
	}
	entry.lastUsed = now
	waiting := false
	if entry.done == nil && c.readDueLocked(entry, now) {
		if c.reads < helmIssuesMaxReads {
			c.startLocked(entry, read)
		} else {
			waiting = true
		}
	}
	done := entry.done
	hasResult := entry.last != nil
	generation := c.generation
	c.mu.Unlock()

	if !hasResult && done != nil {
		select {
		case <-done:
		case <-wait.Done():
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		// Cleared (context switch or shutdown) while waiting: nothing here is
		// about the cluster now connected.
		return helmIssuesView{}
	}
	return helmIssuesView{
		snapshot: entry.last,
		reading:  entry.done != nil,
		waiting:  waiting && entry.done == nil,
		failure:  entry.failure,
		failedAt: entry.failedAt,
	}
}

func (c *helmIssuesCache) readDueLocked(entry *helmIssuesEntry, now time.Time) bool {
	if entry.failure != "" {
		return now.Sub(entry.failedAt) >= helmIssuesRetryAfterFailure
	}
	return entry.last == nil || now.Sub(entry.last.at) >= helmIssuesRefreshAfter
}

// evictLocked drops idle entries and, when makeRoom is set (a new scope is
// about to be added), the least recently used ones beyond
// helmIssuesMaxEntries. Entries with a running read are kept; at most
// helmIssuesMaxReads of them exist, far below the entry cap, so there is
// always room. Callers hold c.mu.
func (c *helmIssuesCache) evictLocked(now time.Time, makeRoom bool) {
	idle := make([]string, 0, len(c.entries))
	for k, e := range c.entries {
		if e.done != nil {
			continue
		}
		if now.Sub(e.lastUsed) > helmIssuesEntryIdle {
			delete(c.entries, k)
			continue
		}
		idle = append(idle, k)
	}
	if over := len(c.entries) - (helmIssuesMaxEntries - 1); makeRoom && over > 0 {
		sort.Slice(idle, func(i, j int) bool { return c.entries[idle[i]].lastUsed.Before(c.entries[idle[j]].lastUsed) })
		for _, k := range idle[:min(over, len(idle))] {
			delete(c.entries, k)
		}
	}
}

// startLocked runs one read for entry. Callers hold c.mu.
func (c *helmIssuesCache) startLocked(entry *helmIssuesEntry, read helmIssuesRead) {
	generation := c.generation
	ctx, cancel := context.WithTimeout(c.ctx, helmIssuesReadTimeout)
	done := make(chan struct{})
	entry.done = done
	c.reads++
	started := c.now()
	go func() {
		defer close(done)
		defer cancel()
		result, err := read(ctx)
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.generation != generation {
			return
		}
		c.reads--
		entry.done = nil
		if err != nil {
			entry.failure = helmReadFailure(ctx, err)
			entry.failedAt = c.now()
			if helm.IsForbiddenError(err) {
				// Access was revoked: the earlier result must not keep
				// showing data Kubernetes now refuses this caller.
				entry.last = nil
			}
			log.Printf("[issues] Reading Helm release issues failed after %s: %v", c.now().Sub(started).Round(time.Millisecond), err)
			return
		}
		entry.failure = ""
		entry.failedAt = time.Time{}
		entry.last = &helmIssuesSnapshot{issues: result, at: c.now()}
	}()
}

// helmReadFailure classifies why a read failed.
func helmReadFailure(ctx context.Context, err error) string {
	switch {
	case helm.IsForbiddenError(err):
		return helmReadForbidden
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return helmReadTimedOut
	default:
		return helmReadError
	}
}
