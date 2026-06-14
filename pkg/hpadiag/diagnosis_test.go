package hpadiag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
