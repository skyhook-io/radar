package server

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/auth"
)

// gitopsIssuesMemo holds the cluster-wide issue composition the GitOps
// detail page's resolvers ask for. One page load fires the tree and
// insights requests independently, and both may need the same
// composition; while a sync runs they poll every 2s. Composing once per
// short window instead of once per request keeps that off the hot path.
//
// The composed set is per user: Compose redacts related refs the caller
// can't read and is scoped to their namespaces, so the key carries the
// identity and the namespace set. Cleared on context switch like the other
// post-switch caches.
type gitopsIssuesMemo struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]*gitopsIssuesEntry
}

type gitopsIssuesEntry struct {
	mu        sync.Mutex
	expiresAt time.Time
	flat      []issues.Issue
	grouped   []issues.Issue
}

func newGitopsIssuesMemo(ttl time.Duration) *gitopsIssuesMemo {
	return &gitopsIssuesMemo{ttl: ttl, entries: map[string]*gitopsIssuesEntry{}}
}

func gitopsIssuesMemoKey(user *auth.User, namespaces []string) string {
	var b strings.Builder
	if user != nil {
		b.WriteString(user.Username)
		groups := append([]string(nil), user.Groups...)
		sort.Strings(groups)
		b.WriteString("\x00")
		b.WriteString(strings.Join(groups, ","))
	}
	ns := append([]string(nil), namespaces...)
	sort.Strings(ns)
	b.WriteString("\x00")
	b.WriteString(strings.Join(ns, ","))
	return b.String()
}

// load returns the memoized composition for key, composing (once, even under
// concurrent callers) when it is missing or stale.
func (m *gitopsIssuesMemo) load(key string, compose func() ([]issues.Issue, []issues.Issue)) ([]issues.Issue, []issues.Issue) {
	if m == nil || m.ttl <= 0 {
		return compose()
	}
	m.mu.Lock()
	e, ok := m.entries[key]
	if !ok {
		e = &gitopsIssuesEntry{}
		m.entries[key] = e
	}
	m.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Now().Before(e.expiresAt) {
		return e.flat, e.grouped
	}
	e.flat, e.grouped = compose()
	e.expiresAt = time.Now().Add(m.ttl)
	return e.flat, e.grouped
}

func (m *gitopsIssuesMemo) clear() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.entries = map[string]*gitopsIssuesEntry{}
	m.mu.Unlock()
}
