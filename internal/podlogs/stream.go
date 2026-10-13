package podlogs

import (
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// StreamCursor remembers where one container's follow left off, so a stream
// that ends while its Pod is still running resumes instead of replaying lines
// the client already has. Only the stream loop touches it.
type StreamCursor struct {
	// uid is the Pod the cursor belongs to; a replacement Pod with the same
	// name starts a new cursor.
	uid types.UID
	// firstFollow is the first follow opened for this cursor. Lines read by an
	// earlier follow belong to the previous Pod and are passed through.
	firstFollow int
	last        time.Time
	// delivered counts the lines sent at timestamp last, per content. The pod
	// log API's sinceTime is second-granular, so a resumed follow replays that
	// second, and some runtimes stamp several lines written together with one
	// timestamp. So the n-th time a follow reads a (timestamp, content) is new
	// only when fewer than n were sent.
	delivered map[string]int
	// seen counts the lines at timestamp last that follow seenIn has read.
	seen   map[string]int
	seenIn int
}

// CursorFor returns the cursor for a container, starting a new one when the
// Pod behind the name has changed.
func CursorFor(cursors map[string]*StreamCursor, key string, uid types.UID, follow int) *StreamCursor {
	if c := cursors[key]; c != nil && c.uid == uid {
		return c
	}
	c := &StreamCursor{uid: uid, firstFollow: follow}
	cursors[key] = c
	return c
}

// RestartOptions returns the follow request for the next (re)start: the
// caller's window the first time, and from the last delivered second after.
func (c *StreamCursor) RestartOptions(container string, tailLines int64, sinceSeconds *int64) corev1.PodLogOptions {
	opts := corev1.PodLogOptions{Container: container, Timestamps: true, Follow: true}
	if c.last.IsZero() {
		opts.TailLines = &tailLines
		opts.SinceSeconds = sinceSeconds
		return opts
	}
	since := metav1.NewTime(c.last.Truncate(time.Second))
	opts.SinceTime = &since
	return opts
}

// Queued lines from a replaced Pod are retained, but cannot establish that
// its replacement is readable.
func (c *StreamCursor) FromCurrentPod(entry Entry) bool {
	return entry.Follow >= c.firstFollow
}

// Admit reports whether an entry is new, recording it when it is. Lines
// arrive in order per container, so anything before the last delivered
// timestamp was already sent.
func (c *StreamCursor) Admit(entry Entry) bool {
	if !c.FromCurrentPod(entry) {
		return true
	}
	ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return true
	}
	switch {
	case ts.Before(c.last):
		return false
	case ts.After(c.last):
		c.last = ts
		c.delivered = map[string]int{}
		c.seen = map[string]int{}
		c.seenIn = entry.Follow
	case entry.Follow != c.seenIn:
		c.seen = map[string]int{}
		c.seenIn = entry.Follow
	}
	c.seen[entry.Content]++
	if c.seen[entry.Content] <= c.delivered[entry.Content] {
		return false
	}
	c.delivered[entry.Content] = c.seen[entry.Content]
	return true
}

// UnavailableSources tracks the stream sources, keyed pod/container, whose node
// answered with its notice instead of logs. A live view reopens each source
// every discovery pass, so the same answer repeats; the methods report whether
// the set changed so the notice is only sent when it does. Only the stream's
// main loop may touch it.
type UnavailableSources map[string]bool

func (u UnavailableSources) Add(key string) bool {
	if u[key] {
		return false
	}
	u[key] = true
	return true
}

func (u UnavailableSources) Remove(key string) bool {
	if !u[key] {
		return false
	}
	delete(u, key)
	return true
}

func (u UnavailableSources) RemovePod(podName string) bool {
	changed := false
	for key := range u {
		if strings.HasPrefix(key, podName+"/") {
			delete(u, key)
			changed = true
		}
	}
	return changed
}

// Notice is the current set as a notice; an empty notice clears it.
func (u UnavailableSources) Notice() string {
	failures := make([]string, 0, len(u))
	for key := range u {
		failures = append(failures, key+": "+k8score.ErrLogsUnavailable.Error())
	}
	return UnreadableSourcesNotice(failures)
}

// UnreadableSourcesNotice words failures, each "pod/container: reason".
func UnreadableSourcesNotice(failures []string) string {
	if len(failures) == 0 {
		return ""
	}
	sort.Strings(failures)
	return countNoun(len(failures), "source", "sources") + " could not be read: " + strings.Join(failures[:min(3, len(failures))], "; ")
}
