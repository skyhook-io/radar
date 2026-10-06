package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/pkg/cnpg"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	cnpgSchedulePreviewRuns = 3
	cnpgScheduleMaxLen      = 256
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

func cnpgSchedulePreview(spec string, lastCheck *time.Time, suspended bool, now time.Time) CNPGSchedulePreview {
	p := CNPGSchedulePreview{Schedule: spec, Basis: "now", Suspended: suspended}
	if lastCheck != nil {
		p.Basis, p.LastCheckTime = "lastCheckTime", lastCheck.UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(spec) == "" {
		p.Error = "the schedule is empty"
		return p
	}
	if len(spec) > cnpgScheduleMaxLen {
		p.Error = fmt.Sprintf("the schedule is longer than %d characters", cnpgScheduleMaxLen)
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

func cnpgScheduleLastCheck(sched *unstructured.Unstructured) *time.Time {
	raw, _, _ := unstructured.NestedString(sched.Object, "status", "lastCheckTime")
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &t
}

// GET /api/cnpg/scheduledbackups/{ns}/{name}/schedule-preview?schedule=
// Pure computation over the schedule read as the caller; writes nothing.
func (s *Server) handleCNPGSchedulePreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	dyn, _ := s.getDynamicClientSnapshotForRequest(r)
	if dyn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	sched, err := dyn.Resource(cnpgScheduleGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		s.writeCNPGActionError(w, err, "schedule-preview", namespace, name)
		return
	}
	spec := r.URL.Query().Get("schedule")
	suspended, _, _ := unstructured.NestedBool(sched.Object, "spec", "suspend")
	s.writeJSON(w, cnpgSchedulePreview(spec, cnpgScheduleLastCheck(sched), suspended, time.Now()))
}

// describeCNPGSchedule words an already-validated schedule; see
// cnpg.DescribeSchedule, shared with the Issues engine's own messages.
func describeCNPGSchedule(spec string) string { return cnpg.DescribeSchedule(spec) }
