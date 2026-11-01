package context

import (
	"fmt"
	"github.com/skyhook-io/radar/pkg/health"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func summarizeFlaggerCanary(u *unstructured.Unstructured) *ResourceSummary {
	v := health.FlaggerCanary(u)
	s := &ResourceSummary{Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace(), Age: age(u.GetCreationTimestamp().Time), Status: v.Reason}
	kind, _, _ := unstructured.NestedString(u.Object, "spec", "targetRef", "kind")
	name, _, _ := unstructured.NestedString(u.Object, "spec", "targetRef", "name")
	if name != "" {
		s.Target = fmt.Sprintf("%s/%s", kind, name)
	}
	if suspended, found, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); found {
		s.Suspended = &suspended
	}
	if v.Reason == "Failed" {
		s.Issue = "Last reported rollout failed; inspect Canary events for the cause"
	}
	return s
}

func summarizeFlaggerMetricTemplate(u *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{Kind: u.GetKind(), Name: u.GetName(), Namespace: u.GetNamespace(), Age: age(u.GetCreationTimestamp().Time), Status: "Configured"}
	s.Type, _, _ = unstructured.NestedString(u.Object, "spec", "provider", "type")
	return s
}
