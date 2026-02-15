package context

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// summarizeUnstructured produces a Summary for CRDs and dynamic resources.
// Uses known CRD extractors where available, falls back to generic extraction.
func summarizeUnstructured(obj *unstructured.Unstructured) *ResourceSummary {
	kind := obj.GetKind()
	group := obj.GroupVersionKind().Group

	// Known CRD extractors
	switch {
	case group == "argoproj.io" && kind == "Application":
		return summarizeArgoApp(obj)
	case group == "argoproj.io" && kind == "Rollout":
		return summarizeArgoRollout(obj)
	case group == "kustomize.toolkit.fluxcd.io" && kind == "Kustomization":
		return summarizeFluxKustomization(obj)
	case group == "helm.toolkit.fluxcd.io" && kind == "HelmRelease":
		return summarizeFluxHelmRelease(obj)
	case group == "gateway.networking.k8s.io" && kind == "Gateway":
		return summarizeGateway(obj)
	}

	// Generic fallback
	return summarizeGenericCRD(obj)
}

func summarizeArgoApp(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Application",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	// Sync status
	syncStatus, _, _ := unstructured.NestedString(obj.Object, "status", "sync", "status")
	s.Status = syncStatus

	// Health status stored as issue if not Healthy
	healthStatus, _, _ := unstructured.NestedString(obj.Object, "status", "health", "status")
	if healthStatus != "" && healthStatus != "Healthy" {
		s.Issue = healthStatus
	}

	// Repo URL as extra context
	repo, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "repoURL")
	if repo != "" {
		s.Image = repo // Reuse Image field for repo
	}

	return s
}

func summarizeArgoRollout(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Rollout",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	s.Status = phase

	// Strategy type
	if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "strategy", "canary"); found {
		s.Strategy = "canary"
	} else if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "strategy", "blueGreen"); found {
		s.Strategy = "blueGreen"
	}

	readyReplicas, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
	replicas, _, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if replicas > 0 {
		s.Ready = formatInt64Pair(readyReplicas, replicas)
	}

	return s
}

func summarizeFluxKustomization(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Kustomization",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	s.Status = extractReadyCondition(obj)

	revision, _, _ := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
	if revision != "" {
		s.Version = revision
	}

	return s
}

func summarizeFluxHelmRelease(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "HelmRelease",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	s.Status = extractReadyCondition(obj)

	chart, _, _ := unstructured.NestedString(obj.Object, "spec", "chart", "spec", "chart")
	if chart != "" {
		s.Image = chart // Reuse Image field for chart name
	}

	version, _, _ := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
	if version != "" {
		s.Version = version
	}

	return s
}

func summarizeGateway(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "Gateway",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	className, _, _ := unstructured.NestedString(obj.Object, "spec", "gatewayClassName")
	s.Type = className

	s.Status = extractReadyCondition(obj)

	return s
}

func summarizeGenericCRD(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      obj.GetKind(),
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}

	// Try status.conditions — most CRDs follow this convention
	s.Status = extractReadyCondition(obj)

	// Try status.phase as fallback
	if s.Status == "" {
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		s.Status = phase
	}

	return s
}

// extractReadyCondition extracts the most informative condition from status.conditions.
// Priority: Ready > Available > Synced > first condition.
func extractReadyCondition(obj *unstructured.Unstructured) string {
	conditions, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found || len(conditions) == 0 {
		return ""
	}

	priority := map[string]int{"Ready": 0, "Available": 1, "Synced": 2}
	bestPriority := 999
	bestStatus := ""

	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok {
			continue
		}
		condType, _ := cond["type"].(string)
		condStatus, _ := cond["status"].(string)

		p, known := priority[condType]
		if known && p < bestPriority {
			bestPriority = p
			if condStatus == "True" {
				bestStatus = condType
			} else {
				reason, _ := cond["reason"].(string)
				if reason != "" {
					bestStatus = reason
				} else {
					bestStatus = "Not" + condType
				}
			}
		}
	}

	// If no priority condition found, use first condition
	if bestStatus == "" {
		cond, ok := conditions[0].(map[string]any)
		if ok {
			condType, _ := cond["type"].(string)
			condStatus, _ := cond["status"].(string)
			if condStatus == "True" {
				bestStatus = condType
			} else {
				bestStatus = "Not" + condType
			}
		}
	}

	return bestStatus
}

func formatInt64Pair(a, b int64) string {
	return fmt.Sprintf("%d/%d", a, b)
}
