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
	// SplitsContainers: Loki labels containers in the run's window, so a
	// container can be read alone.
	SplitsContainers bool
}

// ReadJobRun reads a Job run's lines within one read slot. The Job and
// container labels are taken from Loki's label names in the run's own window,
// not from the connection's, which Loki answers for its recent default window
// only: a shipper that gained or lost a label since then would otherwise be
// missed.
// When Loki has a Job label but none of this run's streams carry it (several
// shippers feeding one Loki), the run is read again by pod name, with the
// same window and limit.
func ReadJobRun(ctx context.Context, l StreamLabels, namespace, job string, indexed bool, container string, w Window, limit int) (JobRun, error) {
	if reason := l.HistoryReason(); reason != "" {
		return JobRun{}, errors.New(reason)
	}
	release, err := acquireRead(readerKey(ctx))
	if err != nil {
		return JobRun{}, err
	}
	defer release()
	names, err := labelNamesIn(ctx, w)
	if err != nil {
		return JobRun{}, err
	}
	inWindow := DetectLabels(names)
	l.Job, l.Container = inWindow.Job, inWindow.Container
	split := l.Container != ""
	if l.Job != "" {
		selector, _, err := JobSelector(l, namespace, job, indexed, container)
		if err != nil {
			return JobRun{}, err
		}
		lines, err := queryRange(ctx, l, selector, w, limit)
		if err != nil || len(lines.Items) > 0 {
			return JobRun{Lines: lines, Notice: fmt.Sprintf("Selected by Loki's %s label.", l.Job), SplitsContainers: split}, err
		}
	}
	byLabel := l.Job
	l.Job = ""
	selector, nameNotice, err := JobSelector(l, namespace, job, indexed, container)
	if err != nil {
		return JobRun{}, err
	}
	lines, err := queryRange(ctx, l, selector, w, limit)
	if err != nil {
		return JobRun{}, err
	}
	how := "Selected by pod name; Loki has no Job label in this run's window."
	if byLabel != "" {
		how = fmt.Sprintf("Selected by pod name: no lines carry Loki's %s label for this Job.", byLabel)
	}
	return JobRun{Lines: lines, Notice: strings.TrimSpace(how + " " + nameNotice), SplitsContainers: split}, nil
}

// labelNamesIn is Loki's label names for streams in w.
func labelNamesIn(ctx context.Context, w Window) ([]string, error) {
	body, err := Get(ctx, "/loki/api/v1/labels", url.Values{
		"start": {strconv.FormatInt(w.Start.UnixNano(), 10)},
		"end":   {strconv.FormatInt(w.End.UnixNano(), 10)},
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Status != "success" {
		return nil, fmt.Errorf("%w: Loki's answer was not a list of labels", ErrUnavailable)
	}
	return resp.Data, nil
}
