package k8score

import (
	"log"
	"strings"
	"sync"
	"time"
)

// ListStaleAfter is how long a loaded informer's lists may keep failing, with
// no sign of progress, before Radar stops treating its store as current. It is
// three times the reflector's longest jittered backoff, so one bad minute (an
// API server rollout, a webhook restart) does not count.
const ListStaleAfter = 3 * time.Minute

// listProgress follows one informer's lists through its watch error handler,
// because client-go's HasSynced never reverts: an informer that loaded once
// reads as synced however long its lists then fail.
//
// A failure episode opens with a failed list. Only evidence of progress
// closes it: the store's resource version moved (a list with new data, a
// watch event, a bookmark), or the handler reported an error from the watch,
// which the reflector starts only after a successful list. Time alone never
// closes it.
type listProgress struct {
	label  string // "configmaps" or "configmaps in namespace x", for logs
	rv     func() string
	logger *log.Logger

	mu      sync.Mutex
	since   time.Time // zero when no episode is open
	sinceRV string
	lastErr string
	logged  bool
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
	if p.since.IsZero() || rv != p.sinceRV {
		p.closeLocked()
		p.since, p.sinceRV = now, rv
	}
	p.lastErr = errorForLog(err)
	if !p.logged && now.Sub(p.since) >= ListStaleAfter {
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
	if p.rv() != p.sinceRV {
		p.closeLocked()
		return false
	}
	return now.Sub(p.since) >= ListStaleAfter
}

func (p *listProgress) closeLocked() {
	if p.logged {
		p.logger.Printf("Informer %s is listing again; Radar reports its cache as complete again", p.label)
	}
	p.since, p.sinceRV, p.lastErr, p.logged = time.Time{}, "", "", false
}

func errorForLog(err error) string {
	const maxLen = 256
	msg := err.Error()
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return msg
}
