package k8score

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// EnrichRuntimeObjectWarnings converts a typed K8s runtime.Object to
// *unstructured.Unstructured (if needed) and runs EnrichObjectWarnings.
// Returns nil on conversion error — best-effort, never blocks the caller.
func EnrichRuntimeObjectWarnings(obj runtime.Object) []string {
	if obj == nil {
		return nil
	}
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return EnrichObjectWarnings(u)
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil
	}
	return EnrichObjectWarnings(&unstructured.Unstructured{Object: raw})
}

// EnrichObjectWarnings derives advisory warnings purely from an object's own
// state — no API calls. Safe to invoke per-row on list responses. Surfaces
// state that an agent commonly skims past (deletionTimestamp, terminating
// namespace, never-healthy workloads, external reconciliation by Helm/Argo/Flux).
//
// All entries are self-contained factual sentences. Empty result means
// "nothing notable about this object's state."
func EnrichObjectWarnings(obj *unstructured.Unstructured) []string {
	if obj == nil {
		return nil
	}
	var out []string

	if mgr, instance := detectExternalManager(obj); mgr != "" {
		out = append(out, formatExternalManagerWarning(mgr, instance))
	}

	if w := deletionTimestampWarning(obj); w != "" {
		out = append(out, w)
	}

	if w := namespaceTerminatingWarning(obj); w != "" {
		out = append(out, w)
	}

	if w := workloadHealthWarning(obj); w != "" {
		out = append(out, w)
	}

	if w := pvcPendingWarning(obj); w != "" {
		out = append(out, w)
	}

	return out
}

func deletionTimestampWarning(obj *unstructured.Unstructured) string {
	dt := obj.GetDeletionTimestamp()
	if dt == nil || dt.IsZero() {
		return ""
	}
	finalizers := obj.GetFinalizers()
	if len(finalizers) == 0 {
		return fmt.Sprintf("Resource is being deleted (deletionTimestamp: %s, no finalizers remaining); it may disappear shortly. Any edits will not persist.", dt.Format(time.RFC3339))
	}
	return fmt.Sprintf("Resource is being deleted (deletionTimestamp: %s) with %d finalizer(s) still active: %s. The resource is in cleanup; edits and reads can behave unexpectedly until finalizers are removed.", dt.Format(time.RFC3339), len(finalizers), strings.Join(finalizers, ", "))
}

func namespaceTerminatingWarning(obj *unstructured.Unstructured) string {
	if obj.GetKind() != "Namespace" {
		return ""
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	if phase != "Terminating" {
		return ""
	}
	return "Namespace is in `Terminating` phase. The apiserver will reject new resource creates in this namespace until termination completes (or until a stuck finalizer is removed)."
}

// workloadHealthWarning surfaces (1) how long the workload's primary health
// condition has been False and (2) whether that failure has been continuous
// since the workload was created — a strong "this state is baseline, not new"
// signal. We don't recommend an interpretation: agents reading current state
// will treat "broken since deploy" as the problem to fix; agents debugging an
// incident will treat it as a baseline distractor.
func workloadHealthWarning(obj *unstructured.Unstructured) string {
	kind := obj.GetKind()
	var conditionType string
	switch kind {
	case "Deployment", "StatefulSet":
		conditionType = "Available"
	case "Pod":
		conditionType = "Ready"
	default:
		return ""
	}

	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if len(conds) == 0 {
		return ""
	}

	var failingFor time.Duration
	var foundFailing bool
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		cType, _ := cm["type"].(string)
		if cType != conditionType {
			continue
		}
		cStatus, _ := cm["status"].(string)
		if cStatus != "False" {
			return ""
		}
		foundFailing = true
		if ltt, _ := cm["lastTransitionTime"].(string); ltt != "" {
			if t, err := time.Parse(time.RFC3339, ltt); err == nil {
				failingFor = time.Since(t)
			}
		}
		break
	}
	if !foundFailing {
		return ""
	}

	createdAt := obj.GetCreationTimestamp().Time
	if createdAt.IsZero() || failingFor <= 0 {
		return ""
	}
	age := time.Since(createdAt)

	// "never healthy" = condition has been False for essentially the entire
	// resource lifetime. The 30s slop accommodates the brief moment after
	// creation when conditions haven't been written yet.
	if age-failingFor < 30*time.Second {
		return fmt.Sprintf("Condition `%s=False` for ~%s — continuously since this resource was created. The failure has been present from the moment it was first applied; check whether this matches what you were asked to investigate (a recently-introduced fault vs. a long-standing misconfiguration).", conditionType, durationShort(failingFor))
	}
	return fmt.Sprintf("Condition `%s=False` for ~%s (resource age: %s).", conditionType, durationShort(failingFor), durationShort(age))
}

func pvcPendingWarning(obj *unstructured.Unstructured) string {
	if obj.GetKind() != "PersistentVolumeClaim" {
		return ""
	}
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	if phase != "Pending" {
		return ""
	}
	createdAt := obj.GetCreationTimestamp().Time
	if createdAt.IsZero() {
		return ""
	}
	age := time.Since(createdAt)
	if age < 30*time.Second {
		return ""
	}
	return fmt.Sprintf("PVC has been `Pending` for %s. Common causes: no matching StorageClass, `volumeBindingMode: WaitForFirstConsumer` with no consumer pod scheduled yet, or capacity exhaustion in the underlying provisioner. Check `status.conditions` and recent Warning events on this PVC.", durationShort(age))
}

func durationShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd", days)
}
