package context

import (
	"fmt"
	"github.com/skyhook-io/radar/pkg/configrefs"
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
	analysis := configrefs.FlaggerAnalysis(u)
	iterations, _, _ := unstructured.NestedInt64(analysis, "iterations")
	s.Strategy = "Canary"
	if analysis != nil {
		mirror, _, _ := unstructured.NestedBool(analysis, "mirror")
		if iterations > 0 {
			s.Strategy = "Blue/green"
			match, _, _ := unstructured.NestedSlice(analysis, "match")
			if len(match) > 0 {
				s.Strategy = "A/B testing"
			}
		}
		if mirror && s.Strategy != "A/B testing" {
			s.Strategy += " with mirroring"
		}
	}
	counters := &CanarySummary{}
	if weight, found, _ := unstructured.NestedInt64(u.Object, "status", "canaryWeight"); found && iterations <= 0 {
		counters.ReportedWeightPercent = &weight
	}
	if checks, found, _ := unstructured.NestedInt64(u.Object, "status", "failedChecks"); found {
		counters.ReportedFailedChecks = &checks
	}
	if iteration, found, _ := unstructured.NestedInt64(u.Object, "status", "iterations"); found && iterations > 0 {
		counters.ReportedIterations = &iteration
	}
	if counters.ReportedWeightPercent != nil || counters.ReportedFailedChecks != nil || counters.ReportedIterations != nil {
		s.Canary = counters
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
