package k8s

import (
	"strconv"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/k8score"
)

func cronJobEventSubject(now time.Time) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "batch", UID: "current-cronjob", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
		Spec:       batchv1.CronJobSpec{Schedule: "0 * * * *"},
	}
}

func cronJobWarning(cj *batchv1.CronJob, reason string, now time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "schedule-warning", Namespace: cj.Namespace},
		InvolvedObject: corev1.ObjectReference{APIVersion: "batch/v1", Kind: "CronJob", Namespace: cj.Namespace, Name: cj.Name, UID: cj.UID},
		Type:           corev1.EventTypeWarning, Reason: reason, Message: "controller scheduling failure details",
		Source:         corev1.EventSource{Component: "cronjob-controller"},
		FirstTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)), LastTimestamp: metav1.NewTime(now.Add(-time.Minute)), Count: 1,
	}
}

func TestCronJobControllerEventsSeries(t *testing.T) {
	now := time.Now()
	cj := cronJobEventSubject(now)
	series := cronJobWarning(cj, "MissSchedule", now)
	series.Name = "series"
	series.FirstTimestamp, series.LastTimestamp = metav1.Time{}, metav1.Time{}
	series.EventTime = metav1.NewMicroTime(now.Add(-2 * time.Hour))
	series.Series = &corev1.EventSeries{Count: 42, LastObservedTime: metav1.NewMicroTime(now.Add(-time.Minute))}
	series.Message = "Missed scheduled time to start a job: controller timestamp"
	older := cronJobWarning(cj, "MissSchedule", now)
	older.LastTimestamp = metav1.NewTime(now.Add(-5 * time.Minute))
	older.Message = "older controller message"
	for _, events := range [][]*corev1.Event{{older, series, series}, {series, older}} {
		got := cronJobScheduleEventDetections([]*batchv1.CronJob{cj}, events, now)
		if len(got) != 1 || !strings.Contains(got[0].Message, series.Message) || strings.Contains(got[0].Message, older.Message) {
			t.Fatalf("expected one freshest controller event, got %+v", got)
		}
		if got[0].Message != series.Message {
			t.Errorf("controller message changed: got %q, want %q", got[0].Message, series.Message)
		}
		if !got[0].OnsetAt.Equal(series.EventTime.Time) {
			t.Errorf("series onset = %v, want %v", got[0].OnsetAt, series.EventTime.Time)
		}
	}

	cache := newOwnershipCache(t, map[string]bool{k8score.CronJobs: true, k8score.Events: true}, cj, older, series)
	if got := DetectProblems(cache, cj.Namespace); len(got) != 1 || got[0].Message != series.Message || !got[0].OnsetAt.Equal(series.EventTime.Time) {
		t.Fatalf("series evidence did not survive the informer path: %+v", got)
	}

	t.Run("equal recency is deterministic", func(t *testing.T) {
		other := series.DeepCopy()
		other.Message = "another controller message"
		other.Series.Count = 7
		for _, events := range [][]*corev1.Event{{other, series}, {series, other}} {
			got := cronJobScheduleEventDetections([]*batchv1.CronJob{cj}, events, now)
			if len(got) != 1 || !strings.Contains(got[0].Message, series.Message) {
				t.Fatalf("tie must retain the larger observed count: %+v", got)
			}
		}
	})
}

func TestCronJobControllerEventsFallback(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*corev1.Event)
	}{
		{name: "absent"},
		{name: "RBAC disabled"},
		{name: "expired", mutate: func(e *corev1.Event) { e.LastTimestamp = metav1.NewTime(time.Now().Add(-time.Hour)) }},
		{name: "expired series", mutate: func(e *corev1.Event) {
			e.Series = &corev1.EventSeries{Count: 90, LastObservedTime: metav1.NewMicroTime(time.Now().Add(-time.Hour))}
		}},
		{name: "predecessor UID", mutate: func(e *corev1.Event) { e.InvolvedObject.UID = "deleted-predecessor" }},
		{name: "other namespace", mutate: func(e *corev1.Event) { e.InvolvedObject.Namespace = "other" }},
		{name: "other name", mutate: func(e *corev1.Event) { e.InvolvedObject.Name = "other" }},
		{name: "other kind", mutate: func(e *corev1.Event) { e.InvolvedObject.Kind = "Job" }},
		{name: "other group", mutate: func(e *corev1.Event) { e.InvolvedObject.APIVersion = "other.example/v1" }},
		{name: "other emitter", mutate: func(e *corev1.Event) { e.Source.Component = "other-controller" }},
		{name: "other reporting controller", mutate: func(e *corev1.Event) { e.ReportingController = "other-controller" }},
		{name: "unattributed", mutate: func(e *corev1.Event) { e.Source.Component = "" }},
		{name: "normal", mutate: func(e *corev1.Event) { e.Type = corev1.EventTypeNormal }},
		{name: "unsupported", mutate: func(e *corev1.Event) { e.Reason = "UnsupportedSchedule" }},
		{name: "unrecognized", mutate: func(e *corev1.Event) { e.Reason = "FailedCreate" }},
		{name: "no recency", mutate: func(e *corev1.Event) { e.LastTimestamp = metav1.Time{} }},
		{name: "future", mutate: func(e *corev1.Event) { e.LastTimestamp = metav1.NewTime(time.Now().Add(time.Hour)) }},
		{name: "UID-less predecessor series", mutate: func(e *corev1.Event) {
			e.InvolvedObject.UID = ""
			e.FirstTimestamp = metav1.NewTime(time.Now().Add(-96 * time.Hour))
		}},
	}
	for _, tt := range cases {
		for _, fallback := range []string{"stale", "never-scheduled", "repeated-without-success"} {
			t.Run(tt.name+"/"+fallback, func(t *testing.T) {
				now := time.Now()
				cj := cronJobEventSubject(now)
				tracker := newCronJobScheduleObservationTracker()
				switch fallback {
				case "stale":
					last := metav1.NewTime(now.Add(-48 * time.Hour))
					cj.Status.LastScheduleTime = &last
				case "repeated-without-success":
					observeCronJobEventTestSchedules(cj, tracker, now)
				}
				// Radar's inability to resolve this timezone is not controller evidence.
				tz := "Not/A_Timezone"
				cj.Spec.TimeZone = &tz
				want := DetectCronJobProblems([]*batchv1.CronJob{cj}, nil, tracker, now)
				if len(want) != 1 || want[0].Problem != fallback {
					t.Fatalf("invalid baseline: %+v", want)
				}
				objects := []runtime.Object{cj}
				if tt.mutate != nil {
					e := cronJobWarning(cj, "MissSchedule", now)
					tt.mutate(e)
					objects = append(objects, e)
				}
				cache := newOwnershipCache(t, map[string]bool{k8score.CronJobs: true, k8score.Events: tt.name != "RBAC disabled"}, objects...)
				cache.cronJobScheduleObservations = tracker
				if tt.name == "RBAC disabled" && cache.Events() != nil {
					t.Fatal("RBAC fixture must have no Event lister")
				}
				for _, namespace := range []string{"", cj.Namespace} {
					got := DetectProblems(cache, namespace)
					if len(got) != 1 || got[0].Reason != want[0].Problem || got[0].Message != want[0].Reason || !got[0].OnsetAt.Equal(want[0].OnsetAt) || got[0].Cause != "" || got[0].Action != "" {
						t.Fatalf("namespace %q: fallback changed: %+v, want %+v", namespace, got, want)
					}
				}
			})
		}
	}
}

func observeCronJobEventTestSchedules(cj *batchv1.CronJob, tracker *cronJobScheduleObservationTracker, now time.Time) {
	cj.Spec.ConcurrencyPolicy = batchv1.ReplaceConcurrent
	cj.Status.Active = []corev1.ObjectReference{{Name: "current-job"}}
	initial := metav1.NewTime(now.Add(-4 * time.Hour))
	cj.Status.LastScheduleTime = &initial
	tracker.observe(k8score.OpAdd, cj)
	for n := 3; n > 0; n-- {
		last := metav1.NewTime(now.Add(-time.Duration(n) * time.Hour))
		cj.Status.LastScheduleTime = &last
		tracker.observe(k8score.OpUpdate, cj)
	}
}

func TestCronJobControllerEventsChildJobs(t *testing.T) {
	for _, childState := range []string{"none", "failed", "stuck"} {
		for _, withEvent := range []bool{false, true} {
			t.Run(childState+"/event="+strconv.FormatBool(withEvent), func(t *testing.T) {
				now := time.Now()
				cj := cronJobEventSubject(now)
				tracker := newCronJobScheduleObservationTracker()
				observeCronJobEventTestSchedules(cj, tracker, now)
				objects := []runtime.Object{cj}
				if withEvent {
					objects = append(objects, cronJobWarning(cj, "MissSchedule", now))
				}
				if childState != "none" {
					controller := true
					job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
						Name: "current-job", Namespace: cj.Namespace, UID: "child-job", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
						OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID, Controller: &controller}},
					}}
					if childState == "failed" {
						job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit", LastTransitionTime: metav1.NewTime(now.Add(-time.Hour))}}
					} else {
						job.Status.Active = 1
					}
					objects = append(objects, job)
				}
				cache := newOwnershipCache(t, map[string]bool{k8score.CronJobs: true, k8score.Jobs: true, k8score.Events: true}, objects...)
				cache.cronJobScheduleObservations = tracker
				got := DetectProblems(cache, cj.Namespace)
				wantCount := 1
				if withEvent {
					wantCount++
				}
				if len(got) != wantCount {
					t.Fatalf("lost/duplicated child or schedule diagnosis: %+v", got)
				}
				for _, d := range got {
					switch d.Reason {
					case "MissSchedule":
						if !withEvent || d.Kind != "CronJob" {
							t.Errorf("unexpected schedule event: %+v", d)
						}
					case "repeated-without-success":
						if childState != "none" {
							t.Errorf("child diagnosis duplicated on parent: %+v", d)
						}
					default:
						if d.Kind != "Job" || d.Name != "current-job" || d.OwnerKind != "" || (childState == "failed" && d.Reason != "BackoffLimitExceeded") || (childState == "stuck" && !strings.HasPrefix(d.Reason, "Running for ")) {
							t.Errorf("child diagnosis changed: %+v", d)
						}
					}
				}
			})
		}
	}
}

func TestCronJobControllerEventsIdentityAndFreshness(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name   string
		mutate func(*corev1.Event)
		want   bool
	}{
		{name: "no UID uses namespace and name", want: true, mutate: func(e *corev1.Event) { e.InvolvedObject.UID = "" }},
		{name: "no API version", want: true, mutate: func(e *corev1.Event) { e.InvolvedObject.APIVersion = "" }},
		{name: "freshness boundary", want: true, mutate: func(e *corev1.Event) { e.LastTimestamp = metav1.NewTime(now.Add(-cronJobScheduleFailureWindow)) }},
		{name: "outside boundary", mutate: func(e *corev1.Event) {
			e.LastTimestamp = metav1.NewTime(now.Add(-cronJobScheduleFailureWindow - time.Nanosecond))
		}},
		{name: "before creation", mutate: func(e *corev1.Event) { e.LastTimestamp = metav1.NewTime(now.Add(-96 * time.Hour)) }},
		{name: "legacy count", want: true, mutate: func(e *corev1.Event) { e.Count = 12 }},
		{name: "empty series uses legacy", want: true, mutate: func(e *corev1.Event) { e.Series = &corev1.EventSeries{}; e.Count = 12 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cj := cronJobEventSubject(now)
			e := cronJobWarning(cj, "MissSchedule", now)
			tt.mutate(e)
			got := cronJobScheduleEventDetections([]*batchv1.CronJob{cj}, []*corev1.Event{e}, now)
			if len(got) > 1 || (len(got) == 1) != tt.want {
				t.Fatalf("got %+v, want evidence=%v", got, tt.want)
			}
			if got := cronJobScheduleEventDetections(nil, []*corev1.Event{e}, now); len(got) != 0 {
				t.Fatalf("event attached to nonexistent CronJob: %+v", got)
			}
		})
	}
}

func TestCronJobControllerEventsReasons(t *testing.T) {
	for _, reason := range []string{"UnknownTimeZone", "UnParseableCronJobSchedule", "UnparseableSchedule", "InvalidSchedule", "MissSchedule", "TooManyMissedTimes"} {
		for _, fallback := range []string{"stale", "never-scheduled"} {
			t.Run(reason+"/"+fallback, func(t *testing.T) {
				now := time.Now()
				cj := cronJobEventSubject(now)
				if fallback == "stale" {
					last := metav1.NewTime(now.Add(-48 * time.Hour))
					cj.Status.LastScheduleTime = &last
				}
				event := cronJobWarning(cj, reason, now)
				cache := newOwnershipCache(t, map[string]bool{k8score.CronJobs: true, k8score.Events: true}, cj, event)
				for _, namespace := range []string{"", cj.Namespace} {
					got := DetectProblems(cache, namespace)
					if len(got) != 1 || got[0].Reason != reason {
						t.Fatalf("namespace %q: got %+v, want only %s controller evidence", namespace, got, reason)
					}
					d := got[0]
					if d.Kind != "CronJob" || d.Group != "batch" || d.Namespace != cj.Namespace || d.Name != cj.Name || d.Severity != "medium" {
						t.Errorf("wrong subject/severity: %+v", d)
					}
					if !strings.Contains(d.Message, event.Message) || d.Cause != "" || d.Action != "" {
						t.Errorf("must retain controller message without inferred cause/remediation: %+v", d)
					}
					if d.Fingerprint != "cronjob-event:"+reason || !d.OnsetAt.Equal(event.FirstTimestamp.Time) || !d.ResourceCreatedAt.Equal(cj.CreationTimestamp.Time) {
						t.Errorf("missing stable reason identity or evidence timing: %+v", d)
					}
				}
			})
		}
	}
}
