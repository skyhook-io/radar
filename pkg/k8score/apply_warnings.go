package k8score

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// detectExternalManager inspects labels and annotations for evidence that the
// resource is reconciled by Helm, Argo CD, Flux, or another controller. Returns
// the manager display name and a best-effort instance identifier; both empty
// when nothing was detected. Detection is deliberately conservative — radar and
// kubectl are not considered "external".
func detectExternalManager(obj *unstructured.Unstructured) (string, string) {
	if obj == nil {
		return "", ""
	}
	labels := obj.GetLabels()
	annotations := obj.GetAnnotations()

	if v := annotations["argocd.argoproj.io/instance"]; v != "" {
		return "Argo CD", v
	}
	if v := annotations["argocd.argoproj.io/tracking-id"]; v != "" {
		return "Argo CD", v
	}

	if v := annotations["kustomize.toolkit.fluxcd.io/name"]; v != "" {
		if ns := annotations["kustomize.toolkit.fluxcd.io/namespace"]; ns != "" {
			return "Flux (Kustomization)", ns + "/" + v
		}
		return "Flux (Kustomization)", v
	}
	if v := annotations["helm.toolkit.fluxcd.io/name"]; v != "" {
		if ns := annotations["helm.toolkit.fluxcd.io/namespace"]; ns != "" {
			return "Flux (HelmRelease)", ns + "/" + v
		}
		return "Flux (HelmRelease)", v
	}

	helmRelease := annotations["meta.helm.sh/release-name"]
	helmManaged := strings.EqualFold(labels["app.kubernetes.io/managed-by"], "Helm")
	if helmRelease != "" || helmManaged {
		instance := helmRelease
		if instance == "" {
			instance = labels["app.kubernetes.io/instance"]
		}
		return "Helm", instance
	}

	if mb := labels["app.kubernetes.io/managed-by"]; mb != "" {
		low := strings.ToLower(mb)
		if low != "kubectl" && low != "radar" {
			return mb, labels["app.kubernetes.io/instance"]
		}
	}
	return "", ""
}

func formatExternalManagerWarning(manager, instance string) string {
	if manager == "" {
		return ""
	}
	frag := ""
	if instance != "" {
		frag = fmt.Sprintf(" (instance: %s)", instance)
	}
	return fmt.Sprintf("Resource is reconciled by %s%s. Direct edits may be reverted by the next reconciliation; prefer changing the source of truth (chart values / Application spec / operator CR).", manager, frag)
}

// podSpecPath returns the field path that contains the PodSpec for the given
// workload kind, or nil for kinds without an embedded PodSpec. Used by both
// the field-removal verification and the ConfigMap/Secret consumer lookup.
func podSpecPath(kind string) []string {
	switch kind {
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job":
		return []string{"spec", "template", "spec"}
	case "CronJob":
		return []string{"spec", "jobTemplate", "spec", "template", "spec"}
	case "Pod":
		return []string{"spec"}
	}
	return nil
}

// checkFieldRemoval verifies that fields the agent intended to remove are
// actually gone after apply. Server-side apply does not remove fields owned by
// other managers when those fields are merely omitted from the submission —
// the agent must explicitly set them to null. We check a focused set of
// high-impact paths under the PodSpec (probes, resources, scheduling).
//
// Returns one warning per field that survived the apply despite being omitted.
func checkFieldRemoval(submitted, pre, post *unstructured.Unstructured) []string {
	if submitted == nil || pre == nil || post == nil {
		return nil
	}
	specPath := podSpecPath(pre.GetKind())
	if len(specPath) == 0 {
		return nil
	}

	otherManagers := nonRadarManagers(post)

	var warnings []string

	for _, field := range []string{"nodeSelector", "affinity", "tolerations", "nodeName", "topologySpreadConstraints"} {
		path := append(append([]string{}, specPath...), field)
		if !hasPath(pre, path) || hasPath(submitted, path) {
			continue
		}
		if !hasPath(post, path) {
			continue
		}
		warnings = append(warnings, formatFieldRemovalWarning(strings.Join(path, "."), otherManagers))
	}

	preCs, _, _ := unstructured.NestedSlice(pre.Object, append(specPath, "containers")...)
	submCs, _, _ := unstructured.NestedSlice(submitted.Object, append(specPath, "containers")...)
	postCs, _, _ := unstructured.NestedSlice(post.Object, append(specPath, "containers")...)
	submByName := containersByName(submCs)
	postByName := containersByName(postCs)

	for _, raw := range preCs {
		preC, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := preC["name"].(string)
		if name == "" {
			continue
		}
		submC, hasSubm := submByName[name]
		postC, hasPost := postByName[name]
		if !hasSubm || !hasPost {
			continue
		}
		for _, field := range []string{"readinessProbe", "livenessProbe", "startupProbe", "resources"} {
			if _, in := preC[field]; !in {
				continue
			}
			if _, in := submC[field]; in {
				continue
			}
			if _, in := postC[field]; !in {
				continue
			}
			fieldRef := fmt.Sprintf("%s.containers[name=%s].%s", strings.Join(specPath, "."), name, field)
			warnings = append(warnings, formatFieldRemovalWarning(fieldRef, otherManagers))
		}
	}

	return warnings
}

func formatFieldRemovalWarning(field string, otherManagers []string) string {
	tail := "Server-side apply does not claim fields by omission. To actually remove the field, set its value to null in your YAML."
	if len(otherManagers) > 0 {
		tail = fmt.Sprintf("It is still owned by another field manager (%s). %s", strings.Join(otherManagers, ", "), tail)
	}
	return fmt.Sprintf("Field `%s` was omitted from your submission but is still present after apply. %s", field, tail)
}

func hasPath(obj *unstructured.Unstructured, path []string) bool {
	if obj == nil {
		return false
	}
	_, found, _ := unstructured.NestedFieldNoCopy(obj.Object, path...)
	return found
}

func containersByName(cs []any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(cs))
	for _, c := range cs {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		out[name] = m
	}
	return out
}

// nonRadarManagers returns the names of field managers on the object other
// than radar itself, deduplicated and sorted for stable output. Used to tell
// the agent who else owns fields on the resource.
func nonRadarManagers(obj *unstructured.Unstructured) []string {
	if obj == nil {
		return nil
	}
	mfRaw, found, _ := unstructured.NestedSlice(obj.Object, "metadata", "managedFields")
	if !found {
		return nil
	}
	seen := map[string]struct{}{}
	for _, entry := range mfRaw {
		em, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := em["manager"].(string)
		if name == "" || strings.EqualFold(name, "radar") {
			continue
		}
		seen[name] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// findConfigMapSecretConsumers returns the qualified names (Kind/Name) of
// workloads in the same namespace whose PodSpec references the given
// ConfigMap or Secret via env, envFrom, volumes, or projected volumes.
//
// partial is true when a workload kind could not be listed (e.g. RBAC denied
// the user the apply path impersonates, or the kind isn't in discovery). An
// empty result with partial=true means "scan incomplete", NOT "verified no
// consumers" — the caller must not present it as the latter.
func findConfigMapSecretConsumers(ctx context.Context, dynClient dynamic.Interface, discovery *ResourceDiscovery, namespace, refKind, refName string) (consumers []string, partial bool) {
	if dynClient == nil || discovery == nil || namespace == "" || refName == "" {
		return nil, false
	}
	if refKind != "ConfigMap" && refKind != "Secret" {
		return nil, false
	}

	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "CronJob", "Job"} {
		gvr, ok := discovery.GetGVR(kind)
		if !ok {
			partial = true
			continue
		}
		list, err := dynClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			log.Printf("[k8s] apply_resource: consumer scan list %s in %s failed: %v", kind, namespace, err)
			partial = true
			continue
		}
		for i := range list.Items {
			item := &list.Items[i]
			if podSpecReferences(item, refKind, refName) {
				consumers = append(consumers, fmt.Sprintf("%s/%s", kind, item.GetName()))
			}
		}
	}
	sort.Strings(consumers)
	return consumers, partial
}

func podSpecReferences(obj *unstructured.Unstructured, refKind, refName string) bool {
	specPath := podSpecPath(obj.GetKind())
	if len(specPath) == 0 {
		return false
	}

	for _, group := range []string{"containers", "initContainers"} {
		cs, _, _ := unstructured.NestedSlice(obj.Object, append(specPath, group)...)
		for _, raw := range cs {
			c, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if containerReferences(c, refKind, refName) {
				return true
			}
		}
	}

	volumes, _, _ := unstructured.NestedSlice(obj.Object, append(specPath, "volumes")...)
	for _, raw := range volumes {
		v, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if volumeReferences(v, refKind, refName) {
			return true
		}
	}
	return false
}

func containerReferences(c map[string]any, refKind, refName string) bool {
	envFromKey := "configMapRef"
	envKey := "configMapKeyRef"
	if refKind == "Secret" {
		envFromKey = "secretRef"
		envKey = "secretKeyRef"
	}

	if envFrom, ok := c["envFrom"].([]any); ok {
		for _, raw := range envFrom {
			ef, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if ref, ok := ef[envFromKey].(map[string]any); ok {
				if n, _ := ref["name"].(string); n == refName {
					return true
				}
			}
		}
	}

	if env, ok := c["env"].([]any); ok {
		for _, raw := range env {
			ev, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			vf, ok := ev["valueFrom"].(map[string]any)
			if !ok {
				continue
			}
			if ref, ok := vf[envKey].(map[string]any); ok {
				if n, _ := ref["name"].(string); n == refName {
					return true
				}
			}
		}
	}
	return false
}

func volumeReferences(v map[string]any, refKind, refName string) bool {
	if refKind == "ConfigMap" {
		if src, ok := v["configMap"].(map[string]any); ok {
			if n, _ := src["name"].(string); n == refName {
				return true
			}
		}
	}
	if refKind == "Secret" {
		if src, ok := v["secret"].(map[string]any); ok {
			if n, _ := src["secretName"].(string); n == refName {
				return true
			}
		}
	}

	if proj, ok := v["projected"].(map[string]any); ok {
		if sources, ok := proj["sources"].([]any); ok {
			for _, raw := range sources {
				sm, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if refKind == "ConfigMap" {
					if src, ok := sm["configMap"].(map[string]any); ok {
						if n, _ := src["name"].(string); n == refName {
							return true
						}
					}
				}
				if refKind == "Secret" {
					if src, ok := sm["secret"].(map[string]any); ok {
						if n, _ := src["name"].(string); n == refName {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func formatConsumerWarning(refKind, refName string, consumers []string, partial bool) string {
	if len(consumers) == 0 {
		if partial {
			return fmt.Sprintf("Could not fully enumerate workloads referencing `%s` (some list calls failed — likely RBAC). If you changed its contents, manually verify which workloads mount it and restart any that don't watch for changes.", refName)
		}
		return ""
	}
	preview := consumers
	tail := ""
	if len(preview) > 5 {
		preview = preview[:5]
		tail = fmt.Sprintf(" (+%d more)", len(consumers)-5)
	}
	incomplete := ""
	if partial {
		incomplete = " (consumer list may be incomplete — some list calls failed)"
	}
	return fmt.Sprintf("%s changes typically reach pod filesystems within ~60s, but most applications must detect and reload the new contents. %d workload(s) reference `%s`: %s%s%s. If those apps don't watch for changes, restart them (e.g., `manage_workload restart`) so they pick up the edit.", refKind, len(consumers), refName, strings.Join(preview, ", "), tail, incomplete)
}
