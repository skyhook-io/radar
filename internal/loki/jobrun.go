package loki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// JobRun is one Job run's lines, with a notice that says how its pods were
// selected and what that selection may include.
type JobRun struct {
	Lines
	Notice string
	// PodSplits names the pods whose streams all carry a container label, so
	// each of their containers can be read alone.
	PodSplits map[string]bool
}

// ReadJobRun reads a Job run's lines within one read slot. It first asks Loki
// which streams belong to the run in its window, without a container, and
// decides from those streams alone, not from labels other workloads carry or
// from the connection's recent label discovery:
//   - the run is selected by a Job label when any of its streams carry it, and
//     by pod name only when none do, never because a container read was empty;
//   - by pod name, streams whose Job label names another Job are left out;
//   - a chosen container is matched only when every stream of the run carries
//     a container label; otherwise the run is read whole and the caller keeps
//     the chosen container's lines and the whole-pod ones.
func ReadJobRun(ctx context.Context, l StreamLabels, namespace, job string, indexed bool, container string, w Window, limit int) (JobRun, error) {
	if reason := l.HistoryReason(); reason != "" {
		return JobRun{}, errors.New(reason)
	}
	release, err := acquireRead(readerKey(ctx))
	if err != nil {
		return JobRun{}, err
	}
	defer release()

	base := l
	base.Job, base.Container = "", ""
	byName, nameNotice, err := jobSelector(base, namespace, job, indexed, "", nil)
	if err != nil {
		return JobRun{}, err
	}
	matches := []string{byName}
	for _, label := range jobLabels {
		withLabel := base
		withLabel.Job = label
		sel, _, _ := jobSelector(withLabel, namespace, job, indexed, "", nil)
		matches = append(matches, sel)
	}
	series, err := seriesIn(ctx, matches, w)
	if err != nil {
		return JobRun{}, err
	}

	byLabel := ""
	for _, label := range jobLabels {
		for _, s := range series {
			if s[label] == job {
				byLabel = label
				break
			}
		}
		if byLabel != "" {
			break
		}
	}
	var run []map[string]string
	otherJob := map[string]bool{}
	for _, s := range series {
		if byLabel != "" {
			if s[byLabel] == job {
				run = append(run, s)
			}
			continue
		}
		other := false
		for _, label := range jobLabels {
			if v, ok := s[label]; ok && v != job {
				otherJob[label], other = true, true
			}
		}
		if !other {
			run = append(run, s)
		}
	}

	containerLabel := ""
	for _, c := range containerLabels {
		for _, s := range run {
			if s[c] != "" {
				containerLabel = c
				break
			}
		}
		if containerLabel != "" {
			break
		}
	}
	split := map[string]bool{}
	whole := map[string]bool{}
	for _, s := range run {
		if containerLabel != "" && s[containerLabel] != "" {
			split[s[l.Pod]] = true
		} else {
			whole[s[l.Pod]] = true
		}
	}
	for pod := range whole {
		delete(split, pod)
	}

	var absent []string
	for _, label := range jobLabels {
		if otherJob[label] {
			absent = append(absent, label)
		}
	}
	notice := fmt.Sprintf("Selected by Loki's %s label.", byLabel)
	if byLabel == "" {
		how := "Selected by pod name; this run's streams carry no Job label."
		if len(absent) > 0 {
			how = "Selected by pod name, leaving out streams labelled with another Job."
		}
		notice = strings.TrimSpace(how + " " + nameNotice)
	}
	if len(run) == 0 {
		return JobRun{Notice: notice, PodSplits: split}, nil
	}

	sel := base
	sel.Job = byLabel
	if len(whole) == 0 {
		sel.Container = containerLabel
	}
	selector, _, err := jobSelector(sel, namespace, job, indexed, container, absent)
	if err != nil {
		return JobRun{}, err
	}
	mapping := l
	mapping.Container = containerLabel
	lines, err := queryRange(ctx, mapping, selector, w, limit)
	if err != nil {
		return JobRun{}, err
	}
	return JobRun{Lines: lines, Notice: notice, PodSplits: split}, nil
}

// seriesIn is the label sets of the streams matching any of matches in w.
func seriesIn(ctx context.Context, matches []string, w Window) ([]map[string]string, error) {
	body, err := Get(ctx, "/loki/api/v1/series", url.Values{
		"match[]": matches,
		"start":   {strconv.FormatInt(w.Start.UnixNano(), 10)},
		"end":     {strconv.FormatInt(w.End.UnixNano(), 10)},
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Status string              `json:"status"`
		Data   []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Status != "success" {
		return nil, fmt.Errorf("%w: Loki's answer was not a list of series", ErrUnavailable)
	}
	return resp.Data, nil
}
