package k8s

import (
	"context"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/cronsched"
	"github.com/skyhook-io/radar/pkg/resourceid"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

// CronJobSchedulingEvidence describes retained evidence, not a new health condition.
type CronJobSchedulingEvidence struct {
	UID                    string     `json:"uid"`
	State                  string     `json:"state"`
	Reason                 string     `json:"reason"`
	Message                string     `json:"message"`
	LastReportedAt         time.Time  `json:"last_reported_at"`
	ConfigurationChangedAt *time.Time `json:"configuration_changed_at,omitempty"`
	HistoryIncomplete      bool       `json:"history_incomplete,omitempty"`
}

type cronJobSchedulingHistory struct {
	rows             []timeline.TimelineEvent
	incompleteBefore time.Time
	unavailable      bool
}

func readCronJobSchedulingHistory(namespace string, cronjobs []*batchv1.CronJob, live []*corev1.Event, now time.Time) cronJobSchedulingHistory {
	const limit = 10000
	store := timeline.GetStore()
	if store == nil {
		return cronJobSchedulingHistory{unavailable: true}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opts := timeline.QueryOptions{Kinds: []string{"CronJob"}, APIGroups: []string{"batch"}, RequireAPIVersion: true, ClusterContext: ActiveClusterContext(), IncludeManaged: true, IncludeK8sEvents: true, Limit: limit, Sources: []timeline.EventSource{timeline.SourceK8sEvent}, EventTypes: []timeline.EventType{timeline.EventTypeWarning}}
	if namespace != "" {
		opts.Namespaces = []string{namespace}
	}
	if len(cronjobs) == 1 && cronjobs[0].UID != "" {
		opts.Scope = timeline.ResourceScope{UIDs: []string{string(cronjobs[0].UID)}}
	}
	rows, err := store.Query(ctx, opts)
	if err != nil {
		return cronJobSchedulingHistory{unavailable: true}
	}
	h := cronJobSchedulingHistory{rows: rows, incompleteBefore: timeline.ObservationStart()}
	h.unavailable = h.incompleteBefore.IsZero()
	if boundary, ok := store.(timeline.EvictionBoundary); ok && boundary.EvictedThrough().After(h.incompleteBefore) {
		h.incompleteBefore = boundary.EvictedThrough()
	}
	h.noteTruncation(rows, limit)
	// Normal run Events must not crowd warnings out of the first query. Read
	// edits and recovery only for current resources with unresolved reports.
	reports := append([]*corev1.Event(nil), live...)
	for _, row := range rows {
		reports = append(reports, cronJobHistoryEvent(row))
	}
	unresolved := cronJobScheduleEventDetections(cronjobs, reports, now)
	subjects := make(map[types.NamespacedName]*batchv1.CronJob, len(cronjobs))
	for _, cj := range cronjobs {
		subjects[types.NamespacedName{Namespace: cj.Namespace, Name: cj.Name}] = cj
	}
	seen := make(map[types.UID]bool)
	var uids []string
	for _, d := range unresolved {
		cj := subjects[types.NamespacedName{Namespace: d.Namespace, Name: d.Name}]
		if cj != nil && !seen[cj.UID] {
			seen[cj.UID] = true
			uids = append(uids, string(cj.UID))
		}
	}
	if len(uids) == 0 {
		return h
	}
	opts.Scope = timeline.ResourceScope{UIDs: uids}
	opts.Sources = []timeline.EventSource{timeline.SourceInformer, timeline.SourceK8sEvent}
	opts.EventTypes = []timeline.EventType{timeline.EventTypeUpdate, timeline.EventTypeNormal}
	related, err := store.Query(ctx, opts)
	if err != nil {
		h.unavailable = true
		return h
	}
	h.noteTruncation(related, limit)
	h.rows = append(h.rows, related...)
	return h
}

func (h *cronJobSchedulingHistory) noteTruncation(rows []timeline.TimelineEvent, limit int) {
	if len(rows) < limit {
		return
	}
	oldest := rows[0].Timestamp
	for _, row := range rows {
		if row.Timestamp.Before(oldest) {
			oldest = row.Timestamp
		}
	}
	if oldest.After(h.incompleteBefore) {
		h.incompleteBefore = oldest
	}
}

func cronJobHistoryEvent(row timeline.TimelineEvent) *corev1.Event {
	return &corev1.Event{ObjectMeta: metav1.ObjectMeta{UID: types.UID(row.ID)}, InvolvedObject: corev1.ObjectReference{APIVersion: row.APIVersion, Kind: row.Kind, Namespace: row.Namespace, Name: row.Name, UID: types.UID(row.UID)}, Type: string(row.EventType), Reason: row.Reason, Message: row.Message, LastTimestamp: metav1.NewTime(row.Timestamp), Count: row.Count}
}

type cronJobSchedulingAssessment struct {
	detections []Detection
	evidence   map[types.UID]CronJobSchedulingEvidence
}

func cronJobSchedulingField(path string) bool {
	switch path {
	case "spec.schedule", "spec.timeZone", "spec.startingDeadlineSeconds", "spec.concurrencyPolicy", "spec.suspend":
		return true
	default:
		return false
	}
}

func assessCronJobScheduling(cronjobs []*batchv1.CronJob, live []*corev1.Event, history cronJobSchedulingHistory, now time.Time) cronJobSchedulingAssessment {
	result := cronJobSchedulingAssessment{evidence: make(map[types.UID]CronJobSchedulingEvidence)}
	subjects := make(map[types.UID]*batchv1.CronJob, len(cronjobs))
	for _, cj := range cronjobs {
		if cj.UID != "" {
			subjects[cj.UID] = cj
		}
	}
	events := make(map[types.UID][]*corev1.Event)
	edits := make(map[types.UID]time.Time)
	editSequences := make(map[types.UID]int64)
	reportSequences := make(map[*corev1.Event]int64)
	for _, row := range history.rows {
		cj := subjects[types.UID(row.UID)]
		if cj == nil || row.Kind != "CronJob" || resourceid.GroupFromAPIVersion(row.APIVersion) != "batch" || row.Namespace != cj.Namespace || row.Name != cj.Name || row.Timestamp.After(now) || row.Timestamp.Before(cj.CreationTimestamp.Time) {
			continue
		}
		switch row.Source {
		case timeline.SourceInformer:
			if row.EventType != timeline.EventTypeUpdate || row.Diff == nil {
				continue
			}
			for _, field := range row.Diff.Fields {
				if cronJobSchedulingField(field.Path) && row.Timestamp.After(edits[cj.UID]) {
					edits[cj.UID] = row.Timestamp
					editSequences[cj.UID] = row.Seq
				}
			}
		case timeline.SourceK8sEvent:
			event := cronJobHistoryEvent(row)
			if cronJobEventMatches(event, cj, now) {
				events[cj.UID] = append(events[cj.UID], event)
				reportSequences[event] = row.Seq
			}
		}
	}
	for _, event := range live {
		if cj := subjects[event.InvolvedObject.UID]; cj != nil && cronJobEventMatches(event, cj, now) {
			events[cj.UID] = append(events[cj.UID], event)
		}
	}
	for _, cj := range cronjobs {
		if cj.UID == "" || (cj.Spec.Suspend != nil && *cj.Spec.Suspend) {
			continue
		}
		reports := events[cj.UID]
		// The existing detector selects recognized reasons and verifies later Job creation
		// or lastScheduleTime. A completed older Job does not establish recovery.
		unresolved := cronJobScheduleEventDetections([]*batchv1.CronJob{cj}, reports, now)
		for _, d := range unresolved {
			var latest *corev1.Event
			for _, event := range reports {
				if event.Reason == d.Reason && (latest == nil || newerCronJobEvent(event, latest) || (eventLastTime(event).Equal(eventLastTime(latest)) && reportSequences[event] > reportSequences[latest])) {
					latest = event
				}
			}
			if latest == nil {
				continue
			}
			reported := eventLastTime(latest)
			d.Message = latest.Message
			changed := edits[cj.UID]
			beforeEdit := !changed.IsZero() && !reported.After(changed)
			// Legacy Event timestamps have second precision. The shared store's
			// arrival sequence disambiguates an edit and a new report in that second.
			if !changed.IsZero() && reported.Truncate(time.Second).Equal(changed.Truncate(time.Second)) && reportSequences[latest] > 0 && editSequences[cj.UID] > 0 {
				beforeEdit = reportSequences[latest] <= editSequences[cj.UID]
			}
			evidence := CronJobSchedulingEvidence{UID: string(cj.UID), State: "reported", Reason: d.Reason, Message: d.Message, LastReportedAt: reported, HistoryIncomplete: history.unavailable}
			if beforeEdit {
				if cj.Status.LastScheduleTime != nil && cj.Status.LastScheduleTime.After(changed) {
					continue
				}
				evidence.State = "configuration_changed"
				evidence.ConfigurationChangedAt = &changed
			} else if history.unavailable || (!history.incompleteBefore.IsZero() && !reported.After(history.incompleteBefore)) {
				// An evicted configuration edit may have superseded this old report.
				evidence.State = "history_incomplete"
				evidence.HistoryIncomplete = true
			} else {
				if d.Reason == "MissSchedule" || d.Reason == "TooManyMissedTimes" {
					d.Cause = "Kubernetes reported missed schedules. No later scheduled run is recorded."
					d.Action = "Review the controller report and the CronJob timeline."
				} else {
					d.Cause = "Kubernetes reported a scheduling error. No later scheduled run is recorded."
					d.Action = "Review the schedule and the original report."
				}
				result.detections = append(result.detections, d)
			}
			if previous, ok := result.evidence[cj.UID]; !ok || reported.After(previous.LastReportedAt) {
				result.evidence[cj.UID] = evidence
			}
		}
	}
	return result
}

// GetCronJobSchedulingEvidence uses the same assessment as Issues. It exposes
// superseded reports as neutral detail without inventing another issue severity.
func GetCronJobSchedulingEvidence(namespace, name string) *CronJobSchedulingEvidence {
	cache := GetResourceCache()
	if cache == nil || cache.CronJobs() == nil || cache.Events() == nil {
		return nil
	}
	cj, err := cache.CronJobs().CronJobs(namespace).Get(name)
	if err != nil {
		return nil
	}
	live, err := cache.Events().Events(namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	now := time.Now()
	cronjobs := []*batchv1.CronJob{cj}
	assessment := assessCronJobScheduling(cronjobs, live, readCronJobSchedulingHistory(namespace, cronjobs, live, now), now)
	evidence, ok := assessment.evidence[cj.UID]
	if !ok {
		return nil
	}
	return &evidence
}

// A newly edited schedule gets the same cadence grace as a new CronJob.
// Otherwise an older configuration's inactivity immediately revives a warning.
func cronJobScheduleEditPending(cj *batchv1.CronJob, evidence CronJobSchedulingEvidence, now time.Time) bool {
	return evidence.State == "configuration_changed" && evidence.ConfigurationChangedAt != nil &&
		now.Sub(*evidence.ConfigurationChangedAt) < cronsched.StaleThreshold(cj.Spec.Schedule)
}
