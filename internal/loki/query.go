package loki

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
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
// then carry no container), in this cluster when Loki labels clusters and
// --loki-cluster names this one. Only these values reach the query.
func PodSelector(l StreamLabels, namespace, pod, container string) (string, error) {
	if reason := l.HistoryReason(); reason != "" {
		return "", errors.New(reason)
	}
	parts := []string{l.Namespace + "=" + quoteLogQL(namespace), l.Pod + "=" + quoteLogQL(pod)}
	if container != "" && l.Container != "" {
		parts = append(parts, l.Container+"="+quoteLogQL(container))
	}
	return "{" + strings.Join(append(parts, clusterMatcher(l)...), ", ") + "}", nil
}

// clusterMatcher keeps a read to this cluster in a Loki shared by several;
// HistoryReason refuses the read when it can't.
func clusterMatcher(l StreamLabels) []string {
	if l.Cluster == "" || l.ClusterValue == "" {
		return nil
	}
	return []string{l.Cluster + "=" + quoteLogQL(l.ClusterValue)}
}

// generatedNameLimit: Kubernetes cuts a generateName prefix to 58 characters
// before adding the 5-character random suffix.
const generatedNameLimit = 58

// maxIndexDigits bounds an Indexed Job's completion index (Kubernetes allows
// up to 100000 completions for Indexed Jobs).
const maxIndexDigits = 6

// JobSelector selects a Job's pods: by the Job stream label when the shipper
// adds one, else by the names Kubernetes gives them. A Job's pods are
// "<job>-<random>" with "<job>-" cut to 58 characters; an Indexed Job's
// controller keeps "-<index>-" whole and cuts the Job name instead. notice is
// set when a long name may be cut, so another Job with the same prefix may be
// included, and for an Indexed Job, whose "<job>-<index>-<random>" pods share
// their names with those of a Job named "<job>-<index>". The container (when
// given and Loki labels containers) and this cluster go into the selector, so
// Loki's line limit counts only the lines shown.
func JobSelector(l StreamLabels, namespace, job string, indexed bool, container string) (selector, notice string, err error) {
	return jobSelector(l, namespace, job, indexed, container, nil)
}

// jobSelector is JobSelector that also requires each label in absent to be
// unset (Loki matches label="" on streams without the label).
func jobSelector(l StreamLabels, namespace, job string, indexed bool, container string, absent []string) (selector, notice string, err error) {
	if reason := l.HistoryReason(); reason != "" {
		return "", "", errors.New(reason)
	}
	var scope []string
	if container != "" && l.Container != "" {
		scope = append(scope, l.Container+"="+quoteLogQL(container))
	}
	scope = append(scope, clusterMatcher(l)...)
	for _, name := range absent {
		scope = append(scope, name+`=""`)
	}
	build := func(match string) string {
		return "{" + strings.Join(append([]string{l.Namespace + "=" + quoteLogQL(namespace), match}, scope...), ", ") + "}"
	}
	if l.Job != "" {
		return build(l.Job + "=" + quoteLogQL(job)), "", nil
	}
	var alternatives []string
	cut := false
	if !indexed {
		prefix := job + "-"
		if len(prefix) > generatedNameLimit {
			prefix, cut = prefix[:generatedNameLimit], true
		}
		alternatives = append(alternatives, regexp.QuoteMeta(prefix))
	} else {
		seen := map[string]bool{}
		for d := 1; d <= maxIndexDigits; d++ {
			name := job
			if keep := generatedNameLimit - 2 - d; len(name) > keep {
				name, cut = name[:keep], true
			}
			alt := regexp.QuoteMeta(name) + fmt.Sprintf("-[0-9]{%d}-", d)
			if !seen[alt] {
				seen[alt] = true
				alternatives = append(alternatives, alt)
			}
		}
	}
	var notices []string
	if indexed {
		notices = append(notices, fmt.Sprintf("A Job named %s-<index> has pods with the same names, and its lines would be included.", job))
	}
	if cut {
		notices = append(notices, "Job names this long are shortened in pod names; lines from another Job with the same prefix may be included.")
	}
	notice = strings.Join(notices, " ")
	body := alternatives[0]
	if len(alternatives) > 1 {
		body = "(?:" + strings.Join(alternatives, "|") + ")"
	}
	re := "^" + body + "[a-z0-9]{5}$"
	return build(l.Pod + "=~" + quoteLogQL(re)), notice, nil
}
