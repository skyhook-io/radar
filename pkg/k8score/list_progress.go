package k8score

import (
	"log"
	"strings"
	"sync"
	"time"
)

// ListStaleAfter is how long a loaded informer's failed lists must span
// before Radar stops treating its store as current, so one bad minute (an API
// server rollout, a webhook restart) does not count.
const ListStaleAfter = 3 * time.Minute

// listStaleFailures is how many failed lists an episode needs before it can
// go stale. Lists that keep failing produce several within ListStaleAfter;
// a few separate failed relists, each followed by a successful one, don't
// make a copy suspect.
const listStaleFailures = 3

// listQuietAfter is how long without a failed list ends a failure episode:
// twice the slowest gap between two failed lists that keep failing. The
// reflector waits under a minute between attempts (backoff doubling from
// 800ms, capped at 30s, then jittered up to 60s); a 403 fails at once, and a
// 5xx within the API server's one-minute request timeout plus about ten
// seconds of the client's Retry-After retries, so that gap stays under two
// and a half minutes. Silence this long means a list got through, including
// one that came back at the same resource version, which a quiet kind's
// often does. A black-holed API server, whose watch-list requests run to
// their 6-11 minute deadline, reports less often and so never goes stale.
const listQuietAfter = 5 * time.Minute

// listProgress follows one informer's lists through its watch error handler,
// because client-go's HasSynced never reverts: an informer that loaded once
// reads as synced however long its lists then fail.
//
// A failure episode opens with a failed list and stays open while failed
// lists keep arriving. It goes stale once it holds listStaleFailures failures
// spanning ListStaleAfter. Evidence of progress closes it: the store's
// resource version moved (a list with new data, a watch event, a bookmark),
// the handler reported an error from the watch, which the reflector starts
// only after a successful list, or no list failed for listQuietAfter. The
// handler never reports a successful list, so that silence is the only sign
// of one that came back at an unchanged resource version.
type listProgress struct {
	label  string // "configmaps" or "configmaps in namespace x", for logs
	rv     func() string
	logger *log.Logger

	mu       sync.Mutex
	since    time.Time // the episode's first failed list; zero when none is open
	last     time.Time // the episode's latest failed list
	failures int
	sinceRV  string
	lastErr  string
	logged   bool
}

func newListProgress(label string, rv func() string, logger *log.Logger) *listProgress {
	if logger == nil {
		logger = log.Default()
	}
	return &listProgress{label: label, rv: rv, logger: logger}
}

// isListPhaseError reports an error from the list that opens each of the
// reflector's list-and-watch cycles, as client-go's reflector words it. Every
// other error reaching the watch error handler ends a cycle whose list
// succeeded.
func isListPhaseError(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "failed to list ") ||
		strings.HasPrefix(msg, "unable to understand list result") ||
		strings.HasPrefix(msg, "unable to sync list result")
}

// observe records one error from the informer's watch error handler.
func (p *listProgress) observe(err error, now time.Time) {
	if p == nil || err == nil {
		return
	}
	rv := p.rv()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !isListPhaseError(err) {
		p.closeLocked()
		return
	}
	if p.since.IsZero() || rv != p.sinceRV || now.Sub(p.last) >= listQuietAfter {
		p.closeLocked()
		p.since, p.sinceRV = now, rv
	}
	p.last = now
	p.failures++
	p.lastErr = errorForLog(err)
	if !p.logged && p.failingLocked() {
		p.logged = true
		p.logger.Printf("WARNING: informer %s: its lists have failed since %s (%s); until one succeeds, Radar doesn't report its cache as complete",
			p.label, p.since.Format(time.RFC3339), p.lastErr)
	}
}

// stale reports whether the informer's lists have kept failing, with no
// progress, for ListStaleAfter.
func (p *listProgress) stale(now time.Time) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.since.IsZero() {
		return false
	}
	if p.rv() != p.sinceRV || now.Sub(p.last) >= listQuietAfter {
		p.closeLocked()
		return false
	}
	return p.failingLocked()
}

func (p *listProgress) failingLocked() bool {
	return p.failures >= listStaleFailures && p.last.Sub(p.since) >= ListStaleAfter
}

func (p *listProgress) closeLocked() {
	if p.logged {
		p.logger.Printf("Informer %s: its lists stopped failing; Radar reports its cache as complete again", p.label)
	}
	p.since, p.last, p.failures, p.sinceRV, p.lastErr, p.logged = time.Time{}, time.Time{}, 0, "", "", false
}

func errorForLog(err error) string {
	const maxLen = 256
	msg := err.Error()
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return msg
}
