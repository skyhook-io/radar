package loki

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// MaxWindow is the longest time range one read may cover.
const MaxWindow = 7 * 24 * time.Hour

var sinceWindows = map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": MaxWindow}

// Window is the time range of one read.
type Window struct{ Start, End time.Time }

// ParseWindow reads since=1h|6h|24h|7d (default 6h) or start/end (RFC3339).
// Errors are meant for a 400.
func ParseWindow(q url.Values, now time.Time) (Window, error) {
	start, end := q.Get("start"), q.Get("end")
	if start == "" && end == "" {
		since := q.Get("since")
		if since == "" {
			since = "6h"
		}
		d, ok := sinceWindows[since]
		if !ok {
			return Window{}, errors.New("since must be 1h, 6h, 24h or 7d")
		}
		return Window{Start: now.Add(-d), End: now}, nil
	}
	s, err1 := time.Parse(time.RFC3339, start)
	e, err2 := time.Parse(time.RFC3339, end)
	if err1 != nil || err2 != nil {
		return Window{}, errors.New("start and end must both be RFC3339 times")
	}
	if !e.After(s) {
		return Window{}, errors.New("end must be after start")
	}
	if e.Sub(s) > MaxWindow {
		return Window{}, errors.New("the window can be at most 7 days")
	}
	return Window{Start: s, End: e}, nil
}

// quoteLogQL is a LogQL double-quoted string literal.
func quoteLogQL(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// PodSelector is the stream selector for one pod, and one container when
// given and Loki labels containers (otherwise the whole pod is read; its lines
// then carry no container). Only these values reach the query.
func PodSelector(l StreamLabels, namespace, pod, container string) (string, error) {
	if reason := l.HistoryReason(); reason != "" {
		return "", errors.New(reason)
	}
	parts := []string{l.Namespace + "=" + quoteLogQL(namespace), l.Pod + "=" + quoteLogQL(pod)}
	if container != "" && l.Container != "" {
		parts = append(parts, l.Container+"="+quoteLogQL(container))
	}
	return "{" + strings.Join(parts, ", ") + "}", nil
}
