package k8s

import (
	"strings"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/cronsched"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestDetectHPAProblems(t *testing.T) {
	tests := []struct {
		name        string
		hpas        []*autoscalingv2.HorizontalPodAutoscaler
		wantCount   int
		wantProblem string
		wantReason  string
		wantCause   string
		wantAction  string
		wantNoDiag  bool
	}{
		{
			name: "maxed HPA",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 10,
						DesiredReplicas: 10,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas", Message: "the desired replica count is more than the maximum replica count"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "maxed",
			wantReason:  "10/10 replicas (wants 10): TooManyReplicas: the desired replica count is more than the maximum replica count",
			wantNoDiag:  true,
		},
		{
			name: "at max without controller limit condition is not maxed",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 10, DesiredReplicas: 10},
				},
			},
			wantCount: 0,
		},
		{
			name: "not maxed",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 5, DesiredReplicas: 5},
				},
			},
			wantCount: 0,
		},
		{
			name: "zero replicas",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "idle", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 0, DesiredReplicas: 0},
				},
			},
			wantCount: 0,
		},
		{
			name: "maxReplicas zero is not a problem",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 0},
					Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 0, DesiredReplicas: 0},
				},
			},
			wantCount: 0,
		},
		{
			name: "resource metric missing request",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric", Message: "missing request for cpu"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "cannot-scale",
			wantCause:   "Target pods do not declare CPU resource requests",
			wantAction:  "Add CPU resource requests",
		},
		{
			name: "resource metrics API unavailable",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric", Message: "unable to get metrics for resource cpu: unable to fetch metrics from resource metrics API"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "cannot-scale",
			wantCause:   "metrics.k8s.io",
			wantAction:  "resource-metrics provider",
		},
		{
			name: "resource metric missing unknown request",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric", Message: "missing request for required resource"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "cannot-scale",
			wantCause:   "required resource requests",
			wantAction:  "Add the missing resource requests",
		},
		{
			name: "container resource metric missing request",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetContainerResourceMetric", Message: "container cpu-exporter is missing request for memory"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "cannot-scale",
			wantCause:   "memory resource requests",
			wantAction:  "Add memory resource requests",
		},
		{
			name: "custom pods metric unavailable",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "queue", Namespace: "prod"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetPodsMetric", Message: "unable to get pods metric queue_depth"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "cannot-scale",
			wantCause:   "custom metrics adapter",
			wantAction:  "metric name, selector, and namespace",
		},
		{
			name: "custom object metric unavailable",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "queue", Namespace: "prod"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetObjectMetric", Message: "unable to get object metric requests_per_second"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "cannot-scale",
			wantCause:   "custom metrics adapter",
			wantAction:  "metric name, selector, and namespace",
		},
		{
			name: "external metric unavailable",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "queue", Namespace: "prod"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetExternalMetric", Message: "unable to get external metric prod/queue_depth"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "cannot-scale",
			wantCause:   "external metrics adapter",
			wantAction:  "backing query",
		},
		{
			name: "scale target unavailable",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 3,
						DesiredReplicas: 3,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionFalse, Reason: "FailedGetScale", Message: "deployments/scale.apps \"api\" not found"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "cannot-scale",
			wantCause:   "scale subresource",
			wantAction:  "scaleTargetRef exists",
		},
		{
			name: "unknown metric fetch failure uses generic fallback",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "queue", Namespace: "prod"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedComputeMetricsReplicas", Message: "unable to compute replicas from metric"},
						},
					},
				},
			},
			wantCount:   1,
			wantProblem: "cannot-scale",
			wantCause:   "one or more scaling metrics",
			wantAction:  "relevant metrics adapter logs",
		},
		{
			name: "maxed and metrics unavailable emit two distinct issues",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 10,
						DesiredReplicas: 10,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "FailedGetResourceMetric", Message: "missing cpu request"},
							{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas", Message: "the desired replica count is more than the maximum replica count"},
						},
					},
				},
			},
			wantCount: 2,
		},
		{
			name: "scaling disabled is not a metrics outage",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "paused", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 0,
						DesiredReplicas: 0,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionFalse, Reason: "ScalingDisabled", Message: "scaling is disabled since the replica count of the target is zero"},
						},
					},
				},
			},
			wantCount: 0,
		},
		{
			name: "pinned min equals max is not maxed",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "fixed", Namespace: "default"},
					Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
						MinReplicas: ptrInt32(5),
						MaxReplicas: 5,
					},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
					},
				},
			},
			wantCount: 0,
		},
		{
			name: "min limited is drawer context only",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "idle", Namespace: "default"},
					Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
						MinReplicas: ptrInt32(2),
						MaxReplicas: 10,
					},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 2,
						DesiredReplicas: 2,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooFewReplicas", Message: "the desired replica count is less than the minimum replica count"},
						},
					},
				},
			},
			wantCount: 0,
		},
		{
			name: "scale down stabilization is drawer context only",
			hpas: []*autoscalingv2.HorizontalPodAutoscaler{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
					Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
					Status: autoscalingv2.HorizontalPodAutoscalerStatus{
						CurrentReplicas: 5,
						DesiredReplicas: 5,
						Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
							{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "ScaleDownStabilized"},
						},
					},
				},
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := DetectHPAProblems(tt.hpas)
			if len(problems) != tt.wantCount {
				t.Errorf("DetectHPAProblems() returned %d problems, want %d", len(problems), tt.wantCount)
			}
			if tt.wantProblem != "" && len(problems) > 0 {
				if problems[0].Problem != tt.wantProblem {
					t.Errorf("problem = %q, want %q", problems[0].Problem, tt.wantProblem)
				}
			}
			if tt.wantReason != "" && len(problems) > 0 && !strings.Contains(problems[0].Reason, tt.wantReason) {
				t.Errorf("reason = %q, want to contain %q", problems[0].Reason, tt.wantReason)
			}
			if tt.wantCause != "" && len(problems) > 0 && !strings.Contains(problems[0].Cause, tt.wantCause) {
				t.Errorf("cause = %q, want to contain %q", problems[0].Cause, tt.wantCause)
			}
			if tt.wantAction != "" && len(problems) > 0 && !strings.Contains(problems[0].Action, tt.wantAction) {
				t.Errorf("action = %q, want to contain %q", problems[0].Action, tt.wantAction)
			}
			if tt.wantNoDiag && len(problems) > 0 && (problems[0].Cause != "" || problems[0].Action != "") {
				t.Errorf("diagnosis = cause %q action %q, want both empty", problems[0].Cause, problems[0].Action)
			}
		})
	}
}

func ptrInt32(v int32) *int32 {
	return &v
}

func TestDetectCronJobProblems(t *testing.T) {
	now := time.Now()
	suspended := true
	notSuspended := false
	oldTime := metav1.NewTime(now.Add(-48 * time.Hour))
	freshTime := metav1.NewTime(now.Add(-1 * time.Hour))
	veryFreshTime := metav1.NewTime(now.Add(-5 * time.Minute))
	active := []corev1.ObjectReference{{Name: "current"}}

	tests := []struct {
		name        string
		cronjobs    []*batchv1.CronJob
		wantCount   int
		wantProblem string
		wantOnset   time.Time
	}{
		{
			name: "stale Replace cronjob",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
					Spec: batchv1.CronJobSpec{
						Schedule:          "0 2 * * *",
						ConcurrencyPolicy: batchv1.ReplaceConcurrent,
						Suspend:           &notSuspended,
					},
					Status: batchv1.CronJobStatus{Active: active, LastScheduleTime: &oldTime},
				},
			},
			wantCount:   1,
			wantProblem: "stale",
			wantOnset:   oldTime.Add(cronsched.StaleThreshold("0 2 * * *")),
		},
		{
			name: "suspended old cronjob is ok",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &suspended},
					Status:     batchv1.CronJobStatus{LastScheduleTime: &oldTime},
				},
			},
			wantCount: 0,
		},
		{
			name: "fresh cronjob is ok",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &notSuspended},
					Status:     batchv1.CronJobStatus{LastScheduleTime: &freshTime},
				},
			},
			wantCount: 0,
		},
		{
			name: "never-scheduled cronjob",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "new-cron", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-48 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 * * * *", Suspend: &notSuspended},
					Status:     batchv1.CronJobStatus{},
				},
			},
			wantCount:   1,
			wantProblem: "never-scheduled",
			wantOnset:   now.Add(-48 * time.Hour).Add(cronsched.StaleThreshold("0 * * * *")),
		},
		{
			name: "active Allow cronjob relies on its retained Jobs",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "allow", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-24 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 * * * *", Suspend: &notSuspended},
					Status: batchv1.CronJobStatus{
						Active:           active,
						LastScheduleTime: &veryFreshTime,
					},
				},
			},
			wantCount: 0,
		},
		{
			name: "new cronjob gets time to succeed",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 * * * *", Suspend: &notSuspended},
					Status:     batchv1.CronJobStatus{LastScheduleTime: &veryFreshTime},
				},
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := DetectCronJobProblems(tt.cronjobs, nil, nil, now)
			if len(problems) != tt.wantCount {
				t.Errorf("DetectCronJobProblems() returned %d problems, want %d", len(problems), tt.wantCount)
			}
			if tt.wantCount > 0 && len(problems) > 0 {
				if problems[0].Problem != tt.wantProblem {
					t.Errorf("problem = %q, want %q", problems[0].Problem, tt.wantProblem)
				}
				if !problems[0].OnsetAt.Equal(tt.wantOnset) {
					t.Errorf("onset = %v, want threshold crossing %v", problems[0].OnsetAt, tt.wantOnset)
				}
			}
		})
	}
}

func TestCronJobScheduleDetectionUsesObservedHistory(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	notSuspended := false
	newSubject := func() *batchv1.CronJob {
		lastSchedule := metav1.NewTime(now.Add(-4 * time.Hour))
		return &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "replace-broken",
				Namespace:         "default",
				UID:               types.UID("replace-broken-uid"),
				CreationTimestamp: metav1.NewTime(now.Add(-30 * 24 * time.Hour)),
			},
			Spec: batchv1.CronJobSpec{
				Schedule:          "0 * * * *",
				ConcurrencyPolicy: batchv1.ReplaceConcurrent,
				Suspend:           &notSuspended,
			},
			Status: batchv1.CronJobStatus{
				Active:           []corev1.ObjectReference{{Name: "current"}},
				LastScheduleTime: &lastSchedule,
			},
		}
	}
	advance := func(tracker *cronJobScheduleObservationTracker, cj *batchv1.CronJob, at time.Time) {
		lastSchedule := metav1.NewTime(at)
		cj.Status.LastScheduleTime = &lastSchedule
		tracker.observe(k8score.OpUpdate, cj)
	}
	observeMisses := func(tracker *cronJobScheduleObservationTracker, cj *batchv1.CronJob, count int) {
		tracker.observe(k8score.OpAdd, cj)
		for n := 1; n <= count; n++ {
			advance(tracker, cj, now.Add(time.Duration(n-4)*time.Hour))
		}
	}
	ownedJob := func(cj *batchv1.CronJob) *batchv1.Job {
		controller := true
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name: "replace-broken-run", Namespace: cj.Namespace, UID: types.UID("job-uid"),
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID, Controller: &controller,
			}},
		}}
	}

	t.Run("an old status snapshot does not invent prior schedules", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		tracker.observe(k8score.OpAdd, cj)
		if got := DetectCronJobProblems([]*batchv1.CronJob{cj}, nil, tracker, now); len(got) != 0 {
			t.Fatalf("initial observation produced a problem: %+v", got)
		}
	})

	t.Run("three observed schedules cross the threshold", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		observeMisses(tracker, cj, cronJobScheduleObservationThreshold-1)
		if got := DetectCronJobProblems([]*batchv1.CronJob{cj}, nil, tracker, now); len(got) != 0 {
			t.Fatalf("reported before the threshold: %+v", got)
		}
		advance(tracker, cj, now.Add(-time.Hour))
		got := DetectCronJobProblems([]*batchv1.CronJob{cj}, nil, tracker, now)
		if len(got) != 1 || got[0].Problem != "repeated-without-success" {
			t.Fatalf("threshold result = %+v", got)
		}
		if got[0].Duration != 3*time.Hour {
			t.Fatalf("duration = %s, want observed 3h failure window", got[0].Duration)
		}
		if !got[0].OnsetAt.Equal(now.Add(-3 * time.Hour)) {
			t.Fatalf("onset = %v, want exact first failed schedule %v", got[0].OnsetAt, now.Add(-3*time.Hour))
		}
		if !strings.Contains(got[0].Reason, "3 consecutive schedules") {
			t.Fatalf("reason = %q", got[0].Reason)
		}
	})

	t.Run("live success wins over tracker state", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		observeMisses(tracker, cj, cronJobScheduleObservationThreshold)
		success := metav1.NewTime(now.Add(-30 * time.Minute))
		cj.Status.LastSuccessfulTime = &success
		if got := DetectCronJobProblems([]*batchv1.CronJob{cj}, nil, tracker, now); len(got) != 0 {
			t.Fatalf("live success raced with stale tracker state: %+v", got)
		}
	})

	t.Run("a failed child Job suppresses the aggregate", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		observeMisses(tracker, cj, cronJobScheduleObservationThreshold)
		job := ownedJob(cj)
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
		if got := DetectCronJobProblems([]*batchv1.CronJob{cj}, []*batchv1.Job{job}, tracker, now); len(got) != 0 {
			t.Fatalf("failed child Job left a duplicate aggregate: %+v", got)
		}
	})

	t.Run("a directly detected stuck child Job suppresses the aggregate", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		observeMisses(tracker, cj, cronJobScheduleObservationThreshold)
		job := ownedJob(cj)
		job.Status.Active = 1
		if got := DetectCronJobProblems([]*batchv1.CronJob{cj}, []*batchv1.Job{job}, tracker, now); len(got) != 0 {
			t.Fatalf("stuck child Job left a duplicate aggregate: %+v", got)
		}
	})

	t.Run("a directly detected terminating child Job suppresses the aggregate", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		observeMisses(tracker, cj, cronJobScheduleObservationThreshold)
		job := ownedJob(cj)
		deleting := metav1.NewTime(now.Add(-20 * time.Minute))
		job.DeletionTimestamp = &deleting
		if got := DetectCronJobProblems([]*batchv1.CronJob{cj}, []*batchv1.Job{job}, tracker, now); len(got) != 0 {
			t.Fatalf("terminating child Job left a duplicate aggregate: %+v", got)
		}
	})

	t.Run("a failed Job from another CronJob does not suppress", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		observeMisses(tracker, cj, cronJobScheduleObservationThreshold)
		job := ownedJob(cj)
		job.OwnerReferences[0].UID = types.UID("previous-cronjob-uid")
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
		got := DetectCronJobProblems([]*batchv1.CronJob{cj}, []*batchv1.Job{job}, tracker, now)
		if len(got) != 1 || got[0].Problem != "repeated-without-success" {
			t.Fatalf("unrelated Job suppressed the aggregate: %+v", got)
		}
	})

	t.Run("unchanged schedule time never accumulates evidence", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		tracker.observe(k8score.OpAdd, cj)
		for range 5 {
			tracker.observe(k8score.OpUpdate, cj)
		}
		if count, _ := tracker.withoutRecordedSuccess(cj.UID); count != 0 {
			t.Fatalf("unchanged status accumulated %d schedules", count)
		}
	})

	resetCases := []struct {
		name   string
		mutate func(*batchv1.CronJob, *cronJobScheduleObservationTracker)
	}{
		{
			name: "recorded success",
			mutate: func(cj *batchv1.CronJob, tracker *cronJobScheduleObservationTracker) {
				success := metav1.NewTime(now.Add(-2 * time.Hour))
				cj.Status.LastSuccessfulTime = &success
				tracker.observe(k8score.OpUpdate, cj)
			},
		},
		{
			name: "suspend and resume",
			mutate: func(cj *batchv1.CronJob, tracker *cronJobScheduleObservationTracker) {
				suspended := true
				cj.Spec.Suspend = &suspended
				tracker.observe(k8score.OpUpdate, cj)
				resumed := false
				cj.Spec.Suspend = &resumed
				tracker.observe(k8score.OpUpdate, cj)
			},
		},
		{
			name: "schedule edit",
			mutate: func(cj *batchv1.CronJob, tracker *cronJobScheduleObservationTracker) {
				cj.Spec.Schedule = "30 * * * *"
				tracker.observe(k8score.OpUpdate, cj)
			},
		},
		{
			name: "policy edit",
			mutate: func(cj *batchv1.CronJob, tracker *cronJobScheduleObservationTracker) {
				cj.Spec.ConcurrencyPolicy = batchv1.AllowConcurrent
				tracker.observe(k8score.OpUpdate, cj)
				cj.Spec.ConcurrencyPolicy = batchv1.ReplaceConcurrent
				tracker.observe(k8score.OpUpdate, cj)
			},
		},
		{
			name: "status regression",
			mutate: func(cj *batchv1.CronJob, tracker *cronJobScheduleObservationTracker) {
				lastSchedule := metav1.NewTime(now.Add(-5 * time.Hour))
				cj.Status.LastScheduleTime = &lastSchedule
				tracker.observe(k8score.OpUpdate, cj)
			},
		},
	}
	for _, tt := range resetCases {
		t.Run(tt.name+" resets evidence", func(t *testing.T) {
			cj := newSubject()
			tracker := newCronJobScheduleObservationTracker()
			observeMisses(tracker, cj, cronJobScheduleObservationThreshold-1)
			tt.mutate(cj, tracker)
			for n := 1; n < cronJobScheduleObservationThreshold; n++ {
				advance(tracker, cj, now.Add(time.Duration(n)*time.Hour))
			}
			if got := DetectCronJobProblems([]*batchv1.CronJob{cj}, nil, tracker, now.Add(3*time.Hour)); len(got) != 0 {
				t.Fatalf("reset evidence still produced a problem: %+v", got)
			}
		})
	}

	t.Run("delete removes evidence", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		observeMisses(tracker, cj, cronJobScheduleObservationThreshold)
		tracker.observe(k8score.OpDelete, cj)
		if count, since := tracker.withoutRecordedSuccess(cj.UID); count != 0 || !since.IsZero() {
			t.Fatalf("deleted state survived: count=%d since=%s", count, since)
		}
	})

	t.Run("an active run is required", func(t *testing.T) {
		cj := newSubject()
		tracker := newCronJobScheduleObservationTracker()
		observeMisses(tracker, cj, cronJobScheduleObservationThreshold)
		cj.Status.Active = nil
		if got := DetectCronJobProblems([]*batchv1.CronJob{cj}, nil, tracker, now); len(got) != 0 {
			t.Fatalf("inactive CronJob produced a problem: %+v", got)
		}
	})
}
