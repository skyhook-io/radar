package k8score

import (
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const defaultListOnlyStaleAfter = 3 * time.Minute

// listOnlyTracker follows one informer whose watch may be denied while its
// LIST is allowed (list-but-not-watch RBAC, or a proxy that refuses watches).
// Such an informer never holds a watch: the reflector lists, the watch fails
// with 403, the reflector backs off (up to ~60s with jitter) and lists again.
// Its store is a periodic snapshot, not a live view.
//
// The signal is the reflector's watch error handler. client-go only starts a
// watch after list() returned, and list() returns only after the store was
// replaced with the snapshot, so a Forbidden error from the watch step marks
// the end of one completed list cycle. List failures arrive wrapped as
// "failed to list ...", and leave lastListOK where it was.
//
// When a cycle stops completing (a LIST that never returns, or LISTs that keep
// failing), lastListOK stops advancing and the kind becomes stale.
//
// watchDenied is never cleared: a successful watch reports nothing to the
// error handler, so a kind whose watch starts working mid-session keeps the
// list-only treatment until the cache is rebuilt. That treatment is the
// conservative one (absence is not trusted), so the error is safe.
type listOnlyTracker struct {
	kind        string
	namespace   string // "" for a cluster-wide informer
	watchDenied bool
	lastListOK  time.Time
	staleLogged bool
}

func isListPhaseError(err error) bool {
	return strings.HasPrefix(err.Error(), "failed to list ")
}

// observeListOnlyCycle records one reflector error for the tracker. Callers
// hold rc.listOnlyMu.
func (rc *ResourceCache) observeListOnlyCycle(t *listOnlyTracker, err error, now time.Time) {
	if isListPhaseError(err) || !apierrors.IsForbidden(err) {
		return
	}
	if !t.watchDenied {
		rc.stdlog.Printf("Informer %s%s cannot watch (403), so it re-lists periodically; Radar will not treat a missing %s as confirmed absent",
			t.kind, t.namespaceSuffix(), t.kind)
	}
	t.watchDenied = true
	t.lastListOK = now
	if t.staleLogged {
		t.staleLogged = false
		rc.stdlog.Printf("Informer %s%s completed a list again; cache is current", t.kind, t.namespaceSuffix())
	}
}

func (t *listOnlyTracker) namespaceSuffix() string {
	if t.namespace == "" {
		return ""
	}
	return " (namespace " + t.namespace + ")"
}

func (rc *ResourceCache) listOnlyStaleAfter() time.Duration {
	if rc.config.ListOnlyStaleAfter > 0 {
		return rc.config.ListOnlyStaleAfter
	}
	return defaultListOnlyStaleAfter
}

func (t *listOnlyTracker) stale(now time.Time, after time.Duration) bool {
	return t.watchDenied && now.Sub(t.lastListOK) > after
}

// trackersFor returns the trackers whose informer serves resource in ns. An
// empty ns selects every informer of the kind (cluster-scoped kinds, or a
// question about the whole cluster). Callers hold rc.listOnlyMu.
func (rc *ResourceCache) trackersFor(resource, ns string) []*listOnlyTracker {
	all := rc.listOnlyTrackers[resource]
	if ns == "" {
		return all
	}
	var out []*listOnlyTracker
	for _, t := range all {
		if t.namespace == "" || t.namespace == ns {
			out = append(out, t)
		}
	}
	return out
}

// KindListOnly reports whether the informer serving resource (plural
// lowercase informer key) in ns has its watch denied and polls with LIST
// instead. Its contents lag the cluster by up to one list cycle. An empty ns
// asks about every informer of the kind.
func (rc *ResourceCache) KindListOnly(resource, ns string) bool {
	if rc == nil {
		return false
	}
	rc.listOnlyMu.Lock()
	defer rc.listOnlyMu.Unlock()
	for _, t := range rc.trackersFor(resource, ns) {
		if t.watchDenied {
			return true
		}
	}
	return false
}

// KindStale reports whether a list-only informer serving resource in ns has
// not completed a LIST for longer than CacheConfig.ListOnlyStaleAfter. Its
// store is frozen at the last snapshot. An empty ns asks about every informer
// of the kind.
func (rc *ResourceCache) KindStale(resource, ns string) bool {
	if rc == nil {
		return false
	}
	now := time.Now()
	after := rc.listOnlyStaleAfter()
	rc.listOnlyMu.Lock()
	defer rc.listOnlyMu.Unlock()
	for _, t := range rc.trackersFor(resource, ns) {
		if t.stale(now, after) {
			return true
		}
	}
	return false
}

// KindLastListAt returns when the list-only informers serving resource in ns
// last completed a LIST (the oldest across them), or the zero time when none
// of them is list-only.
func (rc *ResourceCache) KindLastListAt(resource, ns string) time.Time {
	if rc == nil {
		return time.Time{}
	}
	rc.listOnlyMu.Lock()
	defer rc.listOnlyMu.Unlock()
	var oldest time.Time
	for _, t := range rc.trackersFor(resource, ns) {
		if t.watchDenied && (oldest.IsZero() || t.lastListOK.Before(oldest)) {
			oldest = t.lastListOK
		}
	}
	return oldest
}

// KindAbsenceVerifiable reports whether a miss in the cache for resource in ns
// may be reported as "does not exist". It needs KindCoversNamespace (the
// informer watches that namespace) and a live watch: a list-only informer is
// a periodic snapshot, so an object created since its last LIST is missing
// from it, and a stalled one can be hours old. Use it wherever a negative
// conclusion is drawn from the cache (missing references, missing keys or
// ports, "no matching object"); keep KindCoversNamespace for scope questions.
// An empty ns asks for the whole cluster, which needs a cluster-wide informer.
func (rc *ResourceCache) KindAbsenceVerifiable(resource, ns string) bool {
	if rc == nil {
		return false
	}
	if ns == "" {
		if rc.config.ResourceScopes != nil && !rc.IsKindClusterWide(resource) {
			return false
		}
	} else if !rc.KindCoversNamespace(resource, ns) {
		return false
	}
	return !rc.KindListOnly(resource, ns)
}

// logStaleTransitions logs once when a list-only informer becomes stale.
// Recovery is logged by observeListOnlyCycle.
func (rc *ResourceCache) logStaleTransitions(now time.Time) {
	after := rc.listOnlyStaleAfter()
	rc.listOnlyMu.Lock()
	defer rc.listOnlyMu.Unlock()
	for _, trackers := range rc.listOnlyTrackers {
		for _, t := range trackers {
			if t.staleLogged || !t.stale(now, after) {
				continue
			}
			t.staleLogged = true
			rc.stdlog.Printf("WARNING: informer %s%s is list-only and has not completed a list for %s (last at %s); its cache is stale and missing objects will not be reported as absent",
				t.kind, t.namespaceSuffix(), now.Sub(t.lastListOK).Round(time.Second), t.lastListOK.Format(time.RFC3339))
		}
	}
}

func (rc *ResourceCache) runStaleLogger(stopCh <-chan struct{}) {
	interval := rc.listOnlyStaleAfter() / 6
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case now := <-ticker.C:
			rc.logStaleTransitions(now)
		}
	}
}
