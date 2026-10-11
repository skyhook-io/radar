package helm

import (
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/helmhistory"
	"helm.sh/helm/v3/pkg/release"
)

const (
	// uninstallStartRetain drops anchors for releases Radar has stopped reading,
	// such as one whose uninstall finished and purged its history. It is far
	// longer than any read cadence so a live anchor is never dropped between
	// reads; only releases that were uninstalling are ever held.
	uninstallStartRetain = 24 * time.Hour
	uninstallStartSweep  = 10 * time.Minute
)

type uninstallStartKey struct {
	context          string
	storageNamespace string
	name             string
}

// A revision number is reused when a purged release is installed again, so the
// deployment time is part of the revision's identity.
type uninstallStartEntry struct {
	revision     int
	deployed     time.Time
	started      time.Time
	lastObserved time.Time
}

// uninstallStartMemo keeps the earliest uninstall start Radar has read for a
// release revision. Helm rewrites Info.Deleted at the start of every uninstall
// attempt, before the pre-delete hook runs, so a retry that fails on the same
// blocker would otherwise restart the stuck clock and hide the issue for
// another threshold. It lives in the Radar process: a restart re-anchors to
// Helm's latest timestamp.
type uninstallStartMemo struct {
	mu        sync.Mutex
	entries   map[uninstallStartKey]uninstallStartEntry
	lastSweep time.Time
}

func newUninstallStartMemo() *uninstallStartMemo {
	return &uninstallStartMemo{entries: map[uninstallStartKey]uninstallStartEntry{}}
}

var uninstallStarts = newUninstallStartMemo()

// observe records the current revision of a release and returns the uninstall
// start to time it from: the earliest Info.Deleted read for this revision while
// it stayed uninstalling. Any other status clears the release's anchor.
func (m *uninstallStartMemo) observe(key uninstallStartKey, current helmhistory.Revision, now time.Time) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(now)
	if current.Status != release.StatusUninstalling.String() {
		delete(m.entries, key)
		return current.Deleted
	}
	entry, ok := m.entries[key]
	if !ok || entry.revision != current.Revision || !entry.deployed.Equal(current.Updated) {
		entry = uninstallStartEntry{revision: current.Revision, deployed: current.Updated}
	}
	if !current.Deleted.IsZero() && (entry.started.IsZero() || current.Deleted.Before(entry.started)) {
		entry.started = current.Deleted
	}
	if entry.started.IsZero() {
		delete(m.entries, key)
		return time.Time{}
	}
	entry.lastObserved = now
	m.entries[key] = entry
	return entry.started
}

func (m *uninstallStartMemo) sweepLocked(now time.Time) {
	if now.Sub(m.lastSweep) < uninstallStartSweep {
		return
	}
	m.lastSweep = now
	for key, entry := range m.entries {
		if now.Sub(entry.lastObserved) > uninstallStartRetain {
			delete(m.entries, key)
		}
	}
}

// analyzeReleaseHistory is helmhistory.Analyze with the current revision's
// uninstall start anchored by uninstallStarts. contextName is the context the
// read started under: a read that straddled a context switch may hold another
// cluster's release, so it neither reads nor updates the anchors.
func analyzeReleaseHistory(contextName, storageNamespace, name string, currentRevision int, revisions []HelmRevision, opts helmhistory.Options) helmhistory.Analysis {
	history := toHelmHistoryRevisions(revisions)
	if i := currentRevisionIndex(history, currentRevision); i >= 0 && contextName == k8s.GetContextName() {
		key := uninstallStartKey{context: contextName, storageNamespace: storageNamespace, name: name}
		history[i].Deleted = uninstallStarts.observe(key, history[i], time.Now())
	}
	return helmhistory.Analyze(name, currentRevision, history, opts)
}

// currentRevisionIndex picks the revision helmhistory.Analyze treats as
// current: the exact revision, or the newest when none is given.
func currentRevisionIndex(history []helmhistory.Revision, currentRevision int) int {
	found := -1
	for i, rev := range history {
		if currentRevision > 0 {
			if rev.Revision == currentRevision {
				return i
			}
			continue
		}
		if found < 0 || rev.Revision > history[found].Revision {
			found = i
		}
	}
	return found
}
