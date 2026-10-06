package cnpg

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/pkg/cnpg"
)

const (
	cnpgSchedulePreviewRuns = 3
	scheduleMaxLen          = 256
)

// CNPGSchedulePreview explains a ScheduledBackup schedule and when the
// operator would run it. Runs are computed as the operator does: from
// status.lastCheckTime when it has one (so a schedule whose next time since
// that check has passed runs as soon as the operator sees it), otherwise from
// now. Times are UTC: the operator evaluates the schedule on its own clock,
// which is UTC unless its Pod sets TZ.
type CNPGSchedulePreview struct {
	Schedule    string   `json:"schedule"`
	Valid       bool     `json:"valid"`
	Error       string   `json:"error,omitempty"`
	Description string   `json:"description,omitempty"`
	NextRuns    []string `json:"nextRuns,omitempty"`
	// RunsImmediately: the first run is due already, so the operator creates
	// a backup as soon as it reconciles this schedule.
	RunsImmediately bool `json:"runsImmediately,omitempty"`
	// Basis is what the first run is counted from: lastCheckTime | now.
	Basis         string `json:"basis"`
	LastCheckTime string `json:"lastCheckTime,omitempty"`
	Suspended     bool   `json:"suspended,omitempty"`
}

func schedulePreview(spec string, lastCheck *time.Time, suspended bool, now time.Time) CNPGSchedulePreview {
	p := CNPGSchedulePreview{Schedule: spec, Basis: "now", Suspended: suspended}
	if lastCheck != nil {
		p.Basis, p.LastCheckTime = "lastCheckTime", lastCheck.UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(spec) == "" {
		p.Error = "the schedule is empty"
		return p
	}
	if len(spec) > scheduleMaxLen {
		p.Error = fmt.Sprintf("the schedule is longer than %d characters", scheduleMaxLen)
		return p
	}
	sched, err := cnpg.ParseSchedule(spec)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	now = now.UTC()
	from := now
	if lastCheck != nil {
		from = lastCheck.UTC()
	}
	next := sched.Next(from)
	if next.IsZero() || sched.Next(now).IsZero() {
		p.Error = "no time satisfies this schedule: the operator would never run it"
		return p
	}
	p.Valid = true
	p.Description = describeCNPGSchedule(spec)
	if !next.After(now) {
		p.RunsImmediately = !suspended
		next = now
		p.NextRuns = append(p.NextRuns, next.Format(time.RFC3339))
		next = sched.Next(now)
	}
	for len(p.NextRuns) < cnpgSchedulePreviewRuns && !next.IsZero() {
		p.NextRuns = append(p.NextRuns, next.Format(time.RFC3339))
		next = sched.Next(next)
	}
	return p
}

func scheduleLastCheck(sched *unstructured.Unstructured) *time.Time {
	raw, _, _ := unstructured.NestedString(sched.Object, "status", "lastCheckTime")
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &t
}

// describeCNPGSchedule words an already-validated schedule; see
// cnpg.DescribeSchedule, shared with the Issues engine's own messages.
func describeCNPGSchedule(spec string) string { return cnpg.DescribeSchedule(spec) }

func (s *Reader) PreviewSchedule(ctx context.Context, dyn dynamic.Interface, namespace, name, spec string, now time.Time) (CNPGSchedulePreview, error) {
	sched, err := dyn.Resource(ScheduleGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return CNPGSchedulePreview{}, err
	}
	suspended, _, _ := unstructured.NestedBool(sched.Object, "spec", "suspend")
	return schedulePreview(spec, scheduleLastCheck(sched), suspended, now), nil
}
