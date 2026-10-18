package k8score

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilnet "k8s.io/apimachinery/pkg/util/net"
)

// DefaultListFailureAfter is how long an informer's lists may keep failing
// before Radar stops vouching for its store. It is three times the
// reflector's longest jittered backoff, so one bad minute (an apiserver
// rollout, a webhook restart) does not count.
const DefaultListFailureAfter = 3 * time.Minute

// ListFailureCause classifies why an informer's lists keep failing. It never
// carries the API server's text, which can name Radar's identity or the
// namespaces it was refused; that stays in logs and sync diagnostics.
type ListFailureCause string

const (
	// ListFailureForbidden: Kubernetes refuses Radar's own identity (HTTP 403).
	ListFailureForbidden ListFailureCause = "forbidden"
	// ListFailureUnauthorized: Kubernetes rejects Radar's credentials (HTTP 401).
	ListFailureUnauthorized ListFailureCause = "unauthorized"
	// ListFailureUnreachable: Radar cannot reach the API server.
	ListFailureUnreachable ListFailureCause = "unreachable"
	// ListFailureTimeout: lists run out of time.
	ListFailureTimeout ListFailureCause = "timeout"
	// ListFailureServerError: the API server answers with a server error, as it
	// does when a custom resource's conversion webhook fails.
	ListFailureServerError ListFailureCause = "server_error"
	// ListFailureOther: any other failure.
	ListFailureOther ListFailureCause = "other"
)

// ListFailure describes a store Radar no longer vouches for: its informer's
// lists have kept failing, with no sign of progress, for the failure
// threshold.
type ListFailure struct {
	// Since is when the failures began.
	Since time.Time
	Cause ListFailureCause
	// Stale: the store had loaded, so it holds a copy that has stopped
	// updating. Otherwise it never loaded.
	Stale bool
}

func classifyListFailure(err error) ListFailureCause {
	var netErr net.Error
	switch {
	case apierrors.IsForbidden(err):
		return ListFailureForbidden
	case apierrors.IsUnauthorized(err):
		return ListFailureUnauthorized
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err),
		errors.As(err, &netErr) && netErr.Timeout():
		return ListFailureTimeout
	case utilnet.IsConnectionRefused(err), utilnet.IsConnectionReset(err), utilnet.IsProbableEOF(err),
		errors.As(err, &netErr):
		return ListFailureUnreachable
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Code >= http.StatusInternalServerError {
		return ListFailureServerError
	}
	return ListFailureOther
}

// errorForLog is err's text, capped for a log line.
func errorForLog(err error) string {
	const maxLen = 256
	msg := err.Error()
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return msg
}

// isListPhaseError reports an error from the list that opens each of the
// reflector's list-and-watch cycles. Every other error reaching the watch
// error handler (the watch failing to start) ends a cycle whose list
// succeeded.
func isListPhaseError(err error) bool {
	return strings.HasPrefix(err.Error(), "failed to list ")
}

// listProgress follows one informer's lists, as seen by the reflector's watch
// error handler, and decides whether Radar can still vouch for its store.
//
// A failure episode opens with the first failed list. Only evidence that the
// reflector made progress closes it: the store's resource version moved (a
// list with new data, a watch event, a bookmark); the handler reported an
// error from after a successful list; or a verification probe listed the
// kind. Time alone never closes it, since a reflector stuck on a hung request
// is silent too. A quiet kind never fails a list, so it never opens one.
//
// The probe settles the case the resource version cannot: a quiet kind whose
// list failed once and then succeeded with nothing new. When the reflector
// fails again after a passing probe, the probe and the reflector's own list
// disagree (a huge list cut short, say), so the episode keeps its start and
// only the reflector's progress closes it.
type listProgress struct {
	label  string // "ConfigMap" or "ConfigMap in namespace x", for logs
	rv     func() string
	synced func() bool
	probe  func(context.Context) error
	// heartbeat probes the kind every failure threshold even with no failure
	// on record, for a kind whose watch can drop events silently. A failing
	// heartbeat opens an episode only a passing probe closes, and onRecover
	// then makes the informer relist.
	heartbeat bool
	onRecover func()

	mu       sync.Mutex
	since    time.Time // zero when no episode is open
	sinceRV  string
	lastFail time.Time
	cause    ListFailureCause
	lastErr  string // the latest failure's text, for logs only
	probedOK bool
	distrust bool
	// probeOpened: a heartbeat opened the episode. The informer's resource
	// version keeps moving through bookmarks while its watch drops events,
	// so it is no evidence of progress.
	probeOpened bool
	probing     bool
	lastProbe   time.Time
	logged      bool
	// reachEpoch, rvAtReach and reachMovedAt follow the store through the
	// API server's current unreachable episode: the version it had when first
	// looked at, and when that version last moved. A store whose version
	// moves is still updating, through a watch that outlived the failure,
	// until it stops moving.
	reachEpoch   uint64
	rvAtReach    string
	reachMovedAt time.Time
}

func newListProgress(label string, rv func() string, synced func() bool, probe func(context.Context) error) *listProgress {
	return &listProgress{label: label, rv: rv, synced: synced, probe: probe}
}

// observe records one error from the informer's watch error handler.
func (p *listProgress) observe(err error, now time.Time) {
	if err == nil {
		return
	}
	rv := p.rv()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !isListPhaseError(err) {
		p.closeLocked()
		return
	}
	switch {
	case p.since.IsZero() || rv != p.sinceRV:
		p.closeLocked()
		p.since, p.sinceRV = now, rv
	case p.probedOK:
		p.probedOK, p.distrust = false, true
	}
	p.lastFail = now
	p.cause, p.lastErr = classifyListFailure(err), errorForLog(err)
}

func (p *listProgress) closeLocked() {
	p.since, p.sinceRV, p.cause = time.Time{}, "", ""
	p.probedOK, p.distrust, p.probeOpened = false, false, false
}

// failure reports the store's list failure, if Radar no longer vouches for it.
func (p *listProgress) failure(now time.Time, after time.Duration, reach *APIReachability) (ListFailure, bool) {
	f, failing, _ := p.failureOf(now, after, reach)
	return f, failing
}

// failureOf is failure that also reports whether the failure is the API
// server being unreachable rather than the store's own lists failing.
func (p *listProgress) failureOf(now time.Time, after time.Duration, reach *APIReachability) (ListFailure, bool, bool) {
	rv := p.rv()
	synced := p.synced()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.since.IsZero() && !p.probeOpened && rv != p.sinceRV {
		p.closeLocked()
	}
	if !p.since.IsZero() && !p.probedOK && now.Sub(p.since) >= after {
		return ListFailure{Since: p.since, Cause: p.cause, Stale: synced}, true, false
	}
	// While the API server is unreachable, a reflector between watches
	// retries inside client-go and never reports, so the store is frozen
	// unless its resource version moves.
	if epoch, since, open := reach.episode(); open {
		switch {
		case p.reachEpoch != epoch:
			p.reachEpoch, p.rvAtReach, p.reachMovedAt = epoch, rv, time.Time{}
		case rv != p.rvAtReach:
			p.rvAtReach, p.reachMovedAt = rv, now
		}
		frozenSince := since
		if p.reachMovedAt.After(frozenSince) {
			frozenSince = p.reachMovedAt
		}
		if now.Sub(frozenSince) >= after {
			return ListFailure{Since: frozenSince, Cause: ListFailureUnreachable, Stale: synced}, true, true
		}
	}
	return ListFailure{}, false, false
}

// startProbe reports whether the watchdog should probe now, and marks the
// probe running.
func (p *listProgress) startProbe(now time.Time, every, after time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.probe == nil || p.probing {
		return false
	}
	verify := !p.since.IsZero() && !p.probedOK && !p.distrust && now.Sub(p.lastFail) >= every && now.Sub(p.lastProbe) >= every
	heartbeat := p.heartbeat && p.since.IsZero() && p.synced() && now.Sub(p.lastProbe) >= after
	if !verify && !heartbeat {
		return false
	}
	p.probing, p.lastProbe = true, now
	return true
}

func (p *listProgress) probeDone(err error, now time.Time) {
	rv := p.rv()
	p.mu.Lock()
	p.probing = false
	recovered := false
	switch {
	case p.since.IsZero() && err != nil && p.heartbeat:
		p.since, p.sinceRV, p.lastFail, p.probeOpened = now, rv, now, true
		p.cause, p.lastErr = classifyListFailure(err), errorForLog(err)
	case p.since.IsZero():
	case err == nil && p.probeOpened:
		p.closeLocked()
		recovered = true
	case err == nil:
		p.probedOK = true
	default:
		p.lastFail = now
		p.cause, p.lastErr = classifyListFailure(err), errorForLog(err)
	}
	onRecover := p.onRecover
	p.mu.Unlock()
	if recovered && onRecover != nil {
		onRecover()
	}
}

// probeSkipped releases a probe the watchdog had no capacity to run.
func (p *listProgress) probeSkipped() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.probing, p.lastProbe = false, time.Time{}
}

// logTransition logs once when Radar stops vouching for the store, and once
// when it vouches again.
func (p *listProgress) logTransition(f ListFailure, failing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !failing {
		if p.logged {
			p.logged = false
			log.Printf("Informer %s is current again; Radar treats its cache as current again", p.label)
		}
		return
	}
	if p.logged {
		return
	}
	p.logged = true
	state := "has not loaded"
	if f.Stale {
		state = "is stale"
	}
	log.Printf("WARNING: informer %s %s: its lists have failed since %s (%s: %s); Radar no longer treats its cache as current",
		p.label, state, f.Since.Format(time.RFC3339), f.Cause, p.lastErr)
}

// APIReachability follows whether Radar's requests reach the API server at
// all, from the transport every shared client uses. client-go retries a
// refused watch inside the reflector without reporting it, so when the whole
// API server is unreachable this is the only signal informers give.
//
// A failure episode opens with a request that got no response and closes
// with any response, whatever its status.
type APIReachability struct {
	mu    sync.Mutex
	since time.Time
	// epoch counts episodes, so a store can tell this one from the last.
	epoch uint64
	// loggedEpoch is the last episode logged; recoveryLogged whether its end
	// was.
	loggedEpoch    uint64
	recoveryLogged bool
}

// ObserveRoundTrip records one HTTP exchange. A request its caller cancelled
// says nothing about the API server.
func (a *APIReachability) ObserveRoundTrip(req *http.Request, resp *http.Response, err error) {
	if a == nil {
		return
	}
	if err == nil && resp == nil {
		return
	}
	if err != nil && req != nil && req.Context().Err() != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case err == nil:
		a.since = time.Time{}
	case a.since.IsZero():
		a.since = time.Now()
		a.epoch++
		a.recoveryLogged = false
	}
}

// logTransition logs once when the API server has been unreachable for
// after, and once when it answers again. Every watchdog sharing a's clients
// calls it; only the first to see a transition logs it.
func (a *APIReachability) logTransition(now time.Time, after time.Duration) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch unreachable := !a.since.IsZero() && now.Sub(a.since) >= after; {
	case unreachable && a.loggedEpoch != a.epoch:
		a.loggedEpoch = a.epoch
		log.Printf("WARNING: Radar has not reached the Kubernetes API server since %s; it no longer treats caches that have not updated since as current",
			a.since.Format(time.RFC3339))
	case a.since.IsZero() && a.loggedEpoch == a.epoch && a.epoch != 0 && !a.recoveryLogged:
		a.recoveryLogged = true
		log.Printf("Radar reaches the Kubernetes API server again")
	}
}

// episode reports the open episode, if any.
func (a *APIReachability) episode() (epoch uint64, since time.Time, open bool) {
	if a == nil {
		return 0, time.Time{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.epoch, a.since, !a.since.IsZero()
}

func (a *APIReachability) unreachableSince(now time.Time, after time.Duration) (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.since.IsZero() || now.Sub(a.since) < after {
		return time.Time{}, false
	}
	return a.since, true
}

// watchdogInterval is how often the watchdog looks at each store.
func watchdogInterval(after time.Duration) time.Duration {
	return min(max(after/6, 10*time.Millisecond), 30*time.Second)
}

// maxConcurrentListProbes bounds the verification probes in flight at once.
const maxConcurrentListProbes = 4

// runListWatchdog keeps each tracker's resource version current, logs when
// Radar stops vouching for a store, and runs verification probes, until stop
// closes.
func runListWatchdog(stop <-chan struct{}, after time.Duration, reach *APIReachability, trackers func() []*listProgress) {
	every := watchdogInterval(after)
	probeTimeout := min(10*time.Second, after)
	sem := make(chan struct{}, maxConcurrentListProbes)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			_, unreachable := reach.unreachableSince(now, 0)
			reach.logTransition(now, after)
			for _, p := range trackers() {
				// An unreachable API server is logged once, not per store.
				f, failing, unreachableOnly := p.failureOf(now, after, reach)
				p.logTransition(f, failing && !unreachableOnly)
				// Probes would only fail while the API server is unreachable.
				if unreachable || !p.startProbe(now, every, after) {
					continue
				}
				select {
				case sem <- struct{}{}:
				default:
					p.probeSkipped()
					continue
				}
				go func(p *listProgress) {
					defer func() { <-sem }()
					ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
					err := p.probe(ctx)
					cancel()
					p.probeDone(err, time.Now())
				}(p)
			}
		}
	}
}
