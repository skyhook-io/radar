package cnpg

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	_ "time/tzdata"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/integration"
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
// now. Times serialize as UTC. Clock records a readable TZ declaration or
// explicitly marks the UTC estimate when the operator clock is unknown.
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
	Basis         string             `json:"basis"`
	LastCheckTime string             `json:"lastCheckTime,omitempty"`
	Suspended     bool               `json:"suspended,omitempty"`
	Clock         *CNPGScheduleClock `json:"clock,omitempty"`
}

func schedulePreview(spec string, lastCheck *time.Time, suspended bool, now time.Time) CNPGSchedulePreview {
	return schedulePreviewIn(spec, lastCheck, suspended, now, time.UTC)
}

type CNPGScheduleClock struct {
	Zone     string `json:"zone"`
	Declared bool   `json:"declared"`
	Source   string `json:"source"`
}

func schedulePreviewIn(spec string, lastCheck *time.Time, suspended bool, now time.Time, location *time.Location) CNPGSchedulePreview {
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
	now = now.In(location)
	from := now
	if lastCheck != nil {
		from = lastCheck.In(location)
	}
	next := sched.Next(from)
	if next.IsZero() || sched.Next(now).IsZero() {
		p.Error = "no time satisfies this schedule: the operator would never run it"
		return p
	}
	p.Valid = true
	p.Description = strings.ReplaceAll(describeCNPGSchedule(spec), "operator clock", location.String())
	if !next.After(now) {
		p.RunsImmediately = !suspended
		next = now
		p.NextRuns = append(p.NextRuns, next.UTC().Format(time.RFC3339))
		next = sched.Next(now)
	}
	for len(p.NextRuns) < cnpgSchedulePreviewRuns && !next.IsZero() {
		p.NextRuns = append(p.NextRuns, next.UTC().Format(time.RFC3339))
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
	return s.previewOnOperatorClock(ctx, namespace, spec, scheduleLastCheck(sched), suspended, now), nil
}

func (s *Reader) PreviewDraftSchedule(ctx context.Context, dyn dynamic.Interface, namespace, name, spec string, now time.Time) (CNPGSchedulePreview, error) {
	if _, err := dyn.Resource(ClusterGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
		return CNPGSchedulePreview{}, err
	}
	return s.previewOnOperatorClock(ctx, namespace, spec, nil, false, now), nil
}

func scheduleClockOf(deployments []*appsv1.Deployment, full bool, namespace string) (CNPGScheduleClock, *time.Location) {
	unknown := CNPGScheduleClock{Zone: "UTC", Source: "Operator clock is not established; upcoming times assume UTC."}
	if !full {
		return unknown, time.UTC
	}
	var watching []*appsv1.Deployment
	for _, d := range deployments {
		if d.Labels[cnpgOperatorNameLabel] != cnpgOperatorNameValue {
			continue
		}
		c := cnpgOperatorContainerOf(d)
		if c == nil || len(c.EnvFrom) > 0 {
			return unknown, time.UTC
		}
		watch := cnpgOperatorWatchOf(c, d.Namespace)
		if watch.Unresolved != "" {
			return unknown, time.UTC
		}
		if watch.All || slices.Contains(watch.Namespaces, namespace) {
			watching = append(watching, d)
		}
	}
	if len(watching) != 1 {
		return unknown, time.UTC
	}
	d := watching[0]
	c := cnpgOperatorContainerOf(d)
	tzCount := 0
	for _, env := range c.Env {
		if env.Name == "TZ" {
			tzCount++
		}
	}
	if tzCount > 1 {
		return unknown, time.UTC
	}
	for _, env := range c.Env {
		if env.Name != "TZ" {
			continue
		}
		if env.ValueFrom != nil || env.Value == "" {
			return unknown, time.UTC
		}
		zone, err := time.LoadLocation(env.Value)
		if err != nil {
			return unknown, time.UTC
		}
		return CNPGScheduleClock{Zone: env.Value, Declared: true, Source: "TZ declared on Deployment " + d.Namespace + "/" + d.Name + "; times use that operator clock."}, zone
	}
	unknown.Source = "Deployment " + d.Namespace + "/" + d.Name + " declares no TZ. Upcoming times assume UTC; the running operator clock is not verified."
	return unknown, time.UTC
}

func (s *Reader) previewOnOperatorClock(ctx context.Context, namespace, spec string, last *time.Time, suspended bool, now time.Time) CNPGSchedulePreview {
	clock, zone := scheduleClockOf(nil, false, namespace)
	if s.Observations.Cache != nil {
		access, deployments := s.operatorDeployments(ctx, s.Observations.Cache, s.Observations.OperatorScope(ctx))
		clock, zone = scheduleClockOf(deployments, access.State == integration.KindCoverageFull, namespace)
	}
	out := schedulePreviewIn(spec, last, suspended, now, zone)
	out.Clock = &clock
	return out
}
