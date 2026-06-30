package k8s

import (
	"strings"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	tests := []struct {
		name        string
		cronjobs    []*batchv1.CronJob
		wantCount   int
		wantProblem string
	}{
		{
			name: "stale cronjob",
			cronjobs: []*batchv1.CronJob{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "backup", Namespace: "default", CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
					Spec:       batchv1.CronJobSpec{Schedule: "0 2 * * *", Suspend: &notSuspended},
					Status:     batchv1.CronJobStatus{LastScheduleTime: &oldTime},
				},
			},
			wantCount:   1,
			wantProblem: "stale",
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
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := DetectCronJobProblems(tt.cronjobs)
			if len(problems) != tt.wantCount {
				t.Errorf("DetectCronJobProblems() returned %d problems, want %d", len(problems), tt.wantCount)
			}
			if tt.wantCount > 0 && len(problems) > 0 {
				if problems[0].Problem != tt.wantProblem {
					t.Errorf("problem = %q, want %q", problems[0].Problem, tt.wantProblem)
				}
			}
		})
	}
}
