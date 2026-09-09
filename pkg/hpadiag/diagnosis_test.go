package hpadiag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fixtureCase struct {
	Name            string          `json:"name"`
	HPA             json.RawMessage `json:"hpa"`
	ExpectedState   State           `json:"expectedState"`
	ExpectedReasons []ReasonID      `json:"expectedReasons"`
	ExpectedSummary string          `json:"expectedSummary,omitempty"`
}

func TestAnalyzeFixtures(t *testing.T) {
	for _, tc := range loadFixtureCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			var hpa autoscalingv2.HorizontalPodAutoscaler
			if err := json.Unmarshal(tc.HPA, &hpa); err != nil {
				t.Fatalf("unmarshal HPA: %v", err)
			}

			got := Analyze(&hpa)
			if got == nil {
				t.Fatal("Analyze returned nil")
			}
			if got.State != tc.ExpectedState {
				t.Fatalf("state = %q, want %q; diagnosis=%+v", got.State, tc.ExpectedState, got)
			}
			if gotReasons := reasonIDs(got); !reflect.DeepEqual(gotReasons, tc.ExpectedReasons) {
				t.Fatalf("reasons = %v, want %v; diagnosis=%+v", gotReasons, tc.ExpectedReasons, got)
			}
			if tc.ExpectedSummary != "" && got.Summary != tc.ExpectedSummary {
				t.Fatalf("summary = %q, want %q; diagnosis=%+v", got.Summary, tc.ExpectedSummary, got)
			}
		})
	}
}

func TestAnalyzeFormatsResourceMetric(t *testing.T) {
	tc := loadFixtureByName(t, "stable")
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := json.Unmarshal(tc.HPA, &hpa); err != nil {
		t.Fatalf("unmarshal HPA: %v", err)
	}
	got := Analyze(&hpa)
	if len(got.Metrics) != 1 {
		t.Fatalf("metrics len = %d, want 1", len(got.Metrics))
	}
	metric := got.Metrics[0]
	if metric.Name != "cpu" || metric.Current != "55% utilization" || metric.Target != "70% utilization" || metric.Status != "ok" {
		t.Fatalf("metric = %+v", metric)
	}
}

func TestAnalyzeSkipsEmptyStatusOnlyMetric(t *testing.T) {
	target := int32(80)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MaxReplicas: 10,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "api",
			},
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{
						Type:               autoscalingv2.UtilizationMetricType,
						AverageUtilization: &target,
					},
				},
			}},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 3,
			DesiredReplicas: 3,
			CurrentMetrics:  []autoscalingv2.MetricStatus{{}},
		},
	}

	got := Analyze(hpa)
	if len(got.Metrics) != 1 {
		t.Fatalf("metrics len = %d, want 1; metrics=%+v", len(got.Metrics), got.Metrics)
	}
	metric := got.Metrics[0]
	if metric.Name != "cpu" || metric.Status != "missing" {
		t.Fatalf("metric = %+v, want missing cpu metric only", metric)
	}
}

func TestAnalyzePrefersScalingOverStaleStatus(t *testing.T) {
	observedGeneration := int64(2)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "worker",
			Namespace:  "default",
			Generation: 3,
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MaxReplicas: 10,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "worker",
			},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			ObservedGeneration: &observedGeneration,
			CurrentReplicas:    2,
			DesiredReplicas:    5,
		},
	}

	got := Analyze(hpa)
	if got.State != StateScalingUp {
		t.Fatalf("state = %q, want %q; diagnosis=%+v", got.State, StateScalingUp, got)
	}
	if got.Summary != "Scaling up from 2 to 5 replicas" {
		t.Fatalf("summary = %q", got.Summary)
	}
	wantReasons := []ReasonID{ReasonStaleStatus, ReasonScalingUp}
	if gotReasons := reasonIDs(got); !reflect.DeepEqual(gotReasons, wantReasons) {
		t.Fatalf("reasons = %v, want %v; diagnosis=%+v", gotReasons, wantReasons, got)
	}
}

func TestAnalyzeRequiresConsistentScaledToZeroState(t *testing.T) {
	min := int32(1)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MinReplicas: &min,
			MaxReplicas: 10,
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 0,
			DesiredReplicas: 0,
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{{
				Type:   autoscalingv2.ScaledToZero,
				Status: corev1.ConditionTrue,
			}},
		},
	}

	got := Analyze(hpa)
	if got.State == StateScaledToZero || got.hasReason(ReasonScaledToZero) {
		t.Fatalf("inconsistent ScaledToZero condition must not classify as intentional zero: %+v", got)
	}
}

func loadFixtureCases(t *testing.T) []fixtureCase {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "hpa-diagnosis", "cases.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var cases []fixtureCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("unmarshal fixtures: %v", err)
	}
	return cases
}

func loadFixtureByName(t *testing.T, name string) fixtureCase {
	t.Helper()
	for _, tc := range loadFixtureCases(t) {
		if tc.Name == name {
			return tc
		}
	}
	t.Fatalf("fixture %q not found", name)
	return fixtureCase{}
}

func reasonIDs(d *Diagnosis) []ReasonID {
	out := make([]ReasonID, 0, len(d.Reasons))
	for _, reason := range d.Reasons {
		out = append(out, reason.ID)
	}
	return out
}

func TestConditionReasonsLeadWithRadarPhrasing(t *testing.T) {
	minReplicas := int32(2)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MinReplicas:    &minReplicas,
			MaxReplicas:    10,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 2,
			DesiredReplicas: 2,
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
				{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue, Reason: "ReadyForNewScale"},
				{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue, Reason: "ValidMetricFound"},
				{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooFewReplicas",
					Message: "the desired replica count is less than the minimum replica count"},
			},
		},
	}
	got := Analyze(hpa)
	var reason *Reason
	for i := range got.Reasons {
		if got.Reasons[i].ID == ReasonLimitedMin {
			reason = &got.Reasons[i]
		}
	}
	if reason == nil {
		t.Fatalf("no limited_min reason: %+v", got.Reasons)
	}
	if reason.Message != "HPA is held at minReplicas=2" {
		t.Errorf("Message = %q, want Radar's phrasing", reason.Message)
	}
	if reason.Detail != "the desired replica count is less than the minimum replica count" {
		t.Errorf("Detail = %q, want the controller's sentence", reason.Detail)
	}
	if got.Summary != "HPA wants fewer replicas but is held at minReplicas=2" {
		t.Errorf("Summary = %q, want the controller's clamp explained", got.Summary)
	}
}

// An HPA the controller has not reconciled has established nothing. Reporting
// it as OK renders "Stable · 0/0 replicas" over a controller that has not run.
func TestAnalyzeDoesNotCallAnUnreconciledHPAStable(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: "default", Generation: 1},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MaxReplicas:    5,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "fresh"},
		},
	}
	got := Analyze(hpa)
	if got.State != StateStale {
		t.Fatalf("state = %q, want %q; diagnosis=%+v", got.State, StateStale, got)
	}

	// The inverse: some controllers reconcile without ever setting
	// observedGeneration. One that has written conditions has clearly run, and
	// calling every such HPA stale would be noise on a healthy cluster.
	reconciled := hpa.DeepCopy()
	reconciled.Status.Conditions = []autoscalingv2.HorizontalPodAutoscalerCondition{
		{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue, Reason: "ReadyForNewScale"},
	}
	reconciled.Status.CurrentReplicas = 3
	reconciled.Status.DesiredReplicas = 3
	if got := Analyze(reconciled); got.State == StateStale {
		t.Fatalf("an HPA with live conditions must not be stale; diagnosis=%+v", got)
	}
}

// A ScalingLimited condition observed against the previous spec must not be
// read against the bounds in front of us: "capped at maxReplicas=10" from a
// condition recorded when the cap was 5 sends someone to change a limit that
// has already been changed.
func TestAnalyzeDoesNotReadStaleLimitsAgainstNewBounds(t *testing.T) {
	observedGeneration := int64(1)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default", Generation: 2},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MaxReplicas:    10,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			ObservedGeneration: &observedGeneration,
			CurrentReplicas:    5,
			DesiredReplicas:    5,
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
				{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas", Message: "the desired replica count is more than the maximum replica count"},
			},
		},
	}
	got := Analyze(hpa)
	for _, reason := range got.Reasons {
		if strings.Contains(reason.Message, "maxReplicas=") {
			t.Fatalf("a stale limit quoted the current bound: %q; diagnosis=%+v", reason.Message, got)
		}
	}
	if got.State != StateStale {
		t.Fatalf("state = %q, want %q; diagnosis=%+v", got.State, StateStale, got)
	}
}
