package k8s

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/cronsched"
	"github.com/skyhook-io/radar/pkg/k8score"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func schedulingHistoryRow(cj *batchv1.CronJob, reason string, at time.Time) timeline.TimelineEvent {
	return timeline.TimelineEvent{ID: "event-" + reason, UID: string(cj.UID), Kind: "CronJob", APIVersion: "batch/v1", Namespace: cj.Namespace, Name: cj.Name, Source: timeline.SourceK8sEvent, EventType: timeline.EventTypeWarning, Reason: reason, Message: "original Kubernetes report", Timestamp: at, Count: 1, ClusterContext: ActiveClusterContext()}
}
func schedulingEditRow(cj *batchv1.CronJob, path string, at time.Time) timeline.TimelineEvent {
	row := schedulingHistoryRow(cj, "", at)
	row.ID = "edit"
	row.Source = timeline.SourceInformer
	row.EventType = timeline.EventTypeUpdate
	row.Diff = &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: path, OldValue: "old", NewValue: "new"}}}
	return row
}
func TestCronJobTimelineReportLifecycle(t *testing.T) {
	now := time.Now()
	cj := cronJobEventSubject(now)
	report := schedulingHistoryRow(cj, "InvalidSchedule", now.Add(-time.Hour))
	history := cronJobSchedulingHistory{rows: []timeline.TimelineEvent{report}}
	got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now)
	if len(got.detections) != 1 || got.evidence[cj.UID].State != "reported" {
		t.Fatalf("retained Event missing after API deletion: %+v", got)
	}
	got = assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now.Add(48*time.Hour))
	if len(got.detections) != 1 {
		t.Fatal("report age was mistaken for recovery")
	}
	history.rows = append(history.rows, schedulingEditRow(cj, "metadata.annotations.note", now.Add(-30*time.Minute)))
	if got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now); len(got.detections) != 1 {
		t.Fatal("metadata edit suppressed report")
	}
	history.rows = append(history.rows, schedulingEditRow(cj, "spec.schedule", now.Add(-20*time.Minute)))
	got = assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now)
	if len(got.detections) != 0 || got.evidence[cj.UID].State != "configuration_changed" {
		t.Fatalf("edited future schedule should be neutral: %+v", got)
	}
	cj.Status.LastSuccessfulTime = &metav1.Time{Time: now.Add(-10 * time.Minute)}
	if got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now); got.evidence[cj.UID].State != "configuration_changed" {
		t.Fatal("old Job completion established scheduling recovery")
	}
	cj.Status.LastScheduleTime = &metav1.Time{Time: now.Add(-5 * time.Minute)}
	if got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now); len(got.detections) != 0 || len(got.evidence) != 0 {
		t.Fatalf("later scheduling should clear current context: %+v", got)
	}
	fresh := schedulingHistoryRow(cj, "InvalidSchedule", now.Add(-time.Minute))
	history.rows = append(history.rows, fresh)
	got = assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now)
	if len(got.detections) != 1 || got.evidence[cj.UID].State != "reported" {
		t.Fatalf("new report after recovery was lost: %+v", got)
	}
}

func TestCronJobTimelineIdentityAndCoverage(t *testing.T) {
	now := time.Now()
	cj := cronJobEventSubject(now)
	for _, tc := range []struct {
		name   string
		mutate func(*timeline.TimelineEvent)
	}{
		{"previous UID", func(r *timeline.TimelineEvent) { r.UID = "old" }},
		{"other namespace", func(r *timeline.TimelineEvent) { r.Namespace = "other" }},
		{"other group", func(r *timeline.TimelineEvent) { r.APIVersion = "other/v1" }},
		{"other kind", func(r *timeline.TimelineEvent) { r.Kind = "Job" }},
		{"future", func(r *timeline.TimelineEvent) { r.Timestamp = now.Add(time.Hour) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := schedulingHistoryRow(cj, "InvalidSchedule", now.Add(-time.Minute))
			tc.mutate(&r)
			got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, cronJobSchedulingHistory{rows: []timeline.TimelineEvent{r}}, now)
			if len(got.detections) != 0 || len(got.evidence) != 0 {
				t.Fatalf("unrelated report inherited: %+v", got)
			}
		})
	}
	row := schedulingHistoryRow(cj, "InvalidSchedule", now.Add(-time.Hour))
	history := cronJobSchedulingHistory{rows: []timeline.TimelineEvent{row}, incompleteBefore: now.Add(-30 * time.Minute)}
	got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now)
	if len(got.detections) != 0 || got.evidence[cj.UID].State != "history_incomplete" {
		t.Fatalf("evicted edit history allowed a current verdict: %+v", got)
	}
	history.incompleteBefore = now.Add(-2 * time.Hour)
	if got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now); len(got.detections) != 1 {
		t.Fatal("unrelated older eviction suppressed a supported report")
	}
	suspended := true
	cj.Spec.Suspend = &suspended
	if got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now); len(got.detections) != 0 || len(got.evidence) != 0 {
		t.Fatal("paused resource treated as failing to schedule")
	}
}

func TestCronJobTimelineReadsSharedStore(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatal(err)
	}
	timeline.SetObservationStartForTest(time.Now().Add(-24 * time.Hour))
	cj := cronJobEventSubject(time.Now())
	row := schedulingHistoryRow(cj, "InvalidSchedule", time.Now().Add(-time.Minute))
	other := row
	other.ID = "other-cluster"
	other.ClusterContext = "another-cluster"
	if err := timeline.GetStore().AppendBatch(t.Context(), []timeline.TimelineEvent{row, other}); err != nil {
		t.Fatal(err)
	}
	h := readCronJobSchedulingHistory(cj.Namespace, []*batchv1.CronJob{cj}, nil, time.Now())
	if len(h.rows) != 1 || h.rows[0].ID != row.ID || h.unavailable {
		t.Fatalf("shared query was not namespace/cluster scoped: %+v", h)
	}
	if got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, h, time.Now()); len(got.detections) != 1 {
		t.Fatal("timeline record did not power diagnosis")
	}
}

func TestCronJobSchedulingIdentityDoesNotRequireReporter(t *testing.T) {
	now := time.Now()
	cj := cronJobEventSubject(now)
	event := cronJobWarning(cj, "InvalidSchedule", now)
	event.Source = corev1.EventSource{}
	event.ReportingController = ""
	got := assessCronJobScheduling([]*batchv1.CronJob{cj}, []*corev1.Event{event}, cronJobSchedulingHistory{}, now)
	if len(got.detections) != 1 {
		t.Fatal("valid subject UID and scheduling reason were not enough")
	}
}

func TestCronJobTimelineNewReportInSameSecondAsEdit(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cj := cronJobEventSubject(now)
	edit := schedulingEditRow(cj, "spec.schedule", now.Add(-time.Minute+200*time.Millisecond))
	edit.Seq = 10
	report := schedulingHistoryRow(cj, "InvalidSchedule", now.Add(-time.Minute))
	report.Seq = 11
	history := cronJobSchedulingHistory{rows: []timeline.TimelineEvent{report, edit}}
	got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now)
	if len(got.detections) != 1 || got.evidence[cj.UID].State != "reported" {
		t.Fatalf("new controller report lost to second-precision timestamp: %+v", got)
	}
	report.Seq = 9
	history.rows = []timeline.TimelineEvent{report, edit}
	got = assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now)
	if len(got.detections) != 0 || got.evidence[cj.UID].State != "configuration_changed" {
		t.Fatalf("observed old report not superseded by edit: %+v", got)
	}
}

func TestCronJobTimelineEditDoesNotReviveOldInactivity(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	timeline.SetObservationStartForTest(now.Add(-24 * time.Hour))
	cj := cronJobEventSubject(now)
	cj.CreationTimestamp = metav1.NewTime(now.Add(-365 * 24 * time.Hour))
	cj.Spec.Schedule = "0 0 1 1 *"
	changed := now.Add(-time.Minute)
	rows := []timeline.TimelineEvent{schedulingHistoryRow(cj, "InvalidSchedule", now.Add(-time.Hour)), schedulingEditRow(cj, "spec.schedule", changed)}
	if err := timeline.GetStore().AppendBatch(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	cache := newOwnershipCache(t, map[string]bool{k8score.CronJobs: true, k8score.Events: true}, cj)
	for _, d := range DetectProblems(cache, cj.Namespace) {
		if d.Kind == "CronJob" {
			t.Fatalf("old configuration's inactivity revived: %+v", d)
		}
	}
	evidence := CronJobSchedulingEvidence{State: "configuration_changed", ConfigurationChangedAt: &changed}
	if cronJobScheduleEditPending(cj, evidence, changed.Add(cronsched.StaleThreshold(cj.Spec.Schedule))) {
		t.Fatal("edit grace must end at current cadence threshold")
	}
}

func TestCronJobUnavailableHistoryIsUncertain(t *testing.T) {
	now := time.Now()
	cj := cronJobEventSubject(now)
	warning := cronJobWarning(cj, "InvalidSchedule", now)
	got := assessCronJobScheduling([]*batchv1.CronJob{cj}, []*corev1.Event{warning}, cronJobSchedulingHistory{unavailable: true}, now)
	if len(got.detections) != 0 || got.evidence[cj.UID].State != "history_incomplete" {
		t.Fatalf("unknown edits asserted a current failure: %+v", got)
	}
}

func TestCronJobRestartGapDoesNotAssertEarlierError(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	timeline.SetObservationStartForTest(now.Add(-10 * time.Second))
	cj := cronJobEventSubject(now)
	old := schedulingHistoryRow(cj, "InvalidSchedule", now.Add(-time.Hour))
	if err := timeline.GetStore().AppendBatch(t.Context(), []timeline.TimelineEvent{old}); err != nil {
		t.Fatal(err)
	}
	history := readCronJobSchedulingHistory(cj.Namespace, []*batchv1.CronJob{cj}, nil, now)
	got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now)
	if len(got.detections) != 0 || got.evidence[cj.UID].State != "history_incomplete" {
		t.Fatalf("unobserved offline edit treated as impossible: %+v", got)
	}
	fresh := old
	fresh.ID = "fresh"
	fresh.Timestamp = now.Add(-5 * time.Second)
	if err := timeline.GetStore().AppendBatch(t.Context(), []timeline.TimelineEvent{fresh}); err != nil {
		t.Fatal(err)
	}
	history = readCronJobSchedulingHistory(cj.Namespace, []*batchv1.CronJob{cj}, nil, now)
	if got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now); len(got.detections) != 1 {
		t.Fatalf("report observed after restart was lost: %+v", got)
	}
}

func TestCronJobRoutineRunsDoNotCrowdOutUnresolvedWarning(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20000}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	timeline.SetObservationStartForTest(now.Add(-24 * time.Hour))
	cj := cronJobEventSubject(now)
	rows := []timeline.TimelineEvent{schedulingHistoryRow(cj, "InvalidSchedule", now.Add(-time.Hour))}
	for i := 0; i < 10001; i++ {
		row := schedulingHistoryRow(cj, "SuccessfulCreate", now.Add(-time.Minute))
		row.ID = fmt.Sprintf("normal-%d", i)
		row.UID = "busy-other-cronjob"
		row.EventType = timeline.EventTypeNormal
		rows = append(rows, row)
	}
	if err := timeline.GetStore().AppendBatch(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	busy := cj.DeepCopy()
	busy.UID = "busy-other-cronjob"
	busy.Name = "busy"
	busy.Status.LastScheduleTime = &metav1.Time{Time: now.Add(-30 * time.Second)}
	history := readCronJobSchedulingHistory(cj.Namespace, []*batchv1.CronJob{cj, busy}, nil, now)
	if len(history.rows) != 1 {
		t.Fatalf("decoded unrelated routine runs: %d rows", len(history.rows))
	}
	if got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, history, now); len(got.detections) != 1 {
		t.Fatalf("busy neighbor hid error: %+v", got)
	}
}

func TestCronJobMissedScheduleActionDoesNotPrescribeDeadline(t *testing.T) {
	now := time.Now()
	cj := cronJobEventSubject(now)
	for _, reason := range []string{"MissSchedule", "TooManyMissedTimes"} {
		got := assessCronJobScheduling([]*batchv1.CronJob{cj}, nil, cronJobSchedulingHistory{rows: []timeline.TimelineEvent{schedulingHistoryRow(cj, reason, now.Add(-time.Minute))}}, now)
		if len(got.detections) != 1 {
			t.Fatalf("missing %s report", reason)
		}
		if strings.Contains(got.detections[0].Action, "startingDeadlineSeconds") {
			t.Fatalf("%s recommends a setting without a stated cause", reason)
		}
	}
}
