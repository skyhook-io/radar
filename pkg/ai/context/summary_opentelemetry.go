package context

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/health"
)

func summarizeOpenTelemetryCollector(u *unstructured.Unstructured) *ResourceSummary {
	v := health.OpenTelemetryCollector(u)
	s := &ResourceSummary{Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace(), Age: age(u.GetCreationTimestamp().Time), Status: v.Reason}
	if v.Level == health.LevelUnhealthy {
		s.Issue = "Collector reconciliation failed; inspect conditions and operator events"
	}
	s.Type, _, _ = unstructured.NestedString(u.Object, "spec", "mode")
	s.Version, _, _ = unstructured.NestedString(u.Object, "status", "version")
	s.Image, _, _ = unstructured.NestedString(u.Object, "status", "image")
	return s
}

func summarizeOpenTelemetryInstrumentation(u *unstructured.Unstructured) *ResourceSummary {
	v := health.OpenTelemetryInstrumentation(u)
	return &ResourceSummary{Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace(), Age: age(u.GetCreationTimestamp().Time), Status: v.Reason}
}
