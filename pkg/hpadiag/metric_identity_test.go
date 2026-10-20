package hpadiag

import (
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestObjectMetricValuesKeepGroupAndSelectorIdentity(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}}
	refs := []autoscalingv2.CrossVersionObjectReference{
		{APIVersion: "v1", Kind: "Endpoints", Name: "source"},
		{APIVersion: "custom.example.io/v1", Kind: "Endpoints", Name: "source"},
		{APIVersion: "custom.example.io/v1", Kind: "Endpoints", Name: "source"},
	}
	identifiers := []autoscalingv2.MetricIdentifier{
		{Name: "requests"}, {Name: "requests"},
		{Name: "requests", Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"lane": "canary"}}},
	}
	values := []string{"10", "20", "30"}
	for i, ref := range refs {
		value := resource.MustParse(values[i])
		hpa.Spec.Metrics = append(hpa.Spec.Metrics, autoscalingv2.MetricSpec{Type: autoscalingv2.ObjectMetricSourceType,
			Object: &autoscalingv2.ObjectMetricSource{DescribedObject: ref, Metric: identifiers[i]}})
		hpa.Status.CurrentMetrics = append(hpa.Status.CurrentMetrics, autoscalingv2.MetricStatus{Type: autoscalingv2.ObjectMetricSourceType,
			Object: &autoscalingv2.ObjectMetricStatus{DescribedObject: ref, Metric: identifiers[i], Current: autoscalingv2.MetricValueStatus{Value: &value}}})
	}
	hpa.Status.CurrentMetrics[0], hpa.Status.CurrentMetrics[2] = hpa.Status.CurrentMetrics[2], hpa.Status.CurrentMetrics[0]
	got := Analyze(hpa).Metrics
	wantNames := []string{"Endpoints/source/requests", "Endpoints (custom.example.io)/source/requests", "Endpoints (custom.example.io)/source/requests"}
	if len(got) != len(values) {
		t.Fatalf("metrics = %+v", got)
	}
	for i := range values {
		if got[i].Name != wantNames[i] || got[i].Current != values[i] {
			t.Errorf("metric %d = %+v, want %s value %s", i, got[i], wantNames[i], values[i])
		}
	}
}

func TestNamespaceMetricUsesEffectiveScopeWithAnOmittedVersion(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}}
	ref := autoscalingv2.CrossVersionObjectReference{Kind: "Namespace", Name: "ignored"}
	metric := autoscalingv2.MetricIdentifier{Name: "queue_depth"}
	hpa.Spec.Metrics = []autoscalingv2.MetricSpec{{Type: autoscalingv2.ObjectMetricSourceType,
		Object: &autoscalingv2.ObjectMetricSource{DescribedObject: ref, Metric: metric}}}
	ref.APIVersion = "v1"
	value := resource.MustParse("7")
	hpa.Status.CurrentMetrics = []autoscalingv2.MetricStatus{{Type: autoscalingv2.ObjectMetricSourceType,
		Object: &autoscalingv2.ObjectMetricStatus{DescribedObject: ref, Metric: metric, Current: autoscalingv2.MetricValueStatus{Value: &value}}}}
	got := Analyze(hpa).Metrics
	if len(got) != 1 || got[0].Name != "Namespace/team/queue_depth" || got[0].Current != "7" {
		t.Fatalf("effective Namespace metric = %+v", got)
	}
}
