package k8s

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Only already-watched stores are read: diagnosing deletion must not create
// persistent watches for every resource served by the cluster.
func DetectDynamicTerminatingProblems(dynamic *DynamicResourceCache, discovery *ResourceDiscovery, namespaces []string, now time.Time, canList func(group, resource, namespace string) bool) []Detection {
	if dynamic == nil || discovery == nil {
		return nil
	}
	selected := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		selected[ns] = true
	}
	var out []Detection
	for _, gvr := range dynamic.WatchedGVRs() {
		kind := discovery.GetKindForGVR(gvr)
		resource, found := discovery.GetResourceWithGroup(kind, gvr.Group)
		if !found || !resource.IsCRD || !dynamic.IsSynced(gvr) {
			continue
		}
		if len(selected) > 0 && !resource.Namespaced {
			continue
		}
		items, err := dynamic.ListWatchedReadOnly(gvr)
		if err != nil {
			log.Printf("[detect] Failed to list terminating %s: %v", gvr, err)
			continue
		}
		allowedNamespaces := map[string]bool{}
		for _, obj := range items {
			if len(selected) > 0 && !selected[obj.GetNamespace()] {
				continue
			}
			det, ok := terminatingProblem(kind, gvr.Group, obj, now)
			if !ok {
				continue
			}
			if canList != nil {
				allowed, checked := allowedNamespaces[obj.GetNamespace()]
				if !checked {
					allowed = canList(gvr.Group, gvr.Resource, obj.GetNamespace())
					allowedNamespaces[obj.GetNamespace()] = allowed
				}
				if !allowed {
					continue
				}
			}
			det.Cause = fmt.Sprintf("Deletion has been pending for %s. %s.", det.Duration, det.Message)
			det.TerminatingFinalizers = append([]string(nil), obj.GetFinalizers()...)
			for i, finalizer := range obj.GetFinalizers() {
				if finalizer == metav1.FinalizerDeleteDependents || finalizer == metav1.FinalizerOrphanDependents || IsProtectionFinalizer(finalizer) {
					continue
				}
				patch := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": string(obj.GetUID())}}
				path := fmt.Sprintf("/metadata/finalizers/%d", i)
				patch = append(patch, map[string]any{"op": "test", "path": path, "value": finalizer}, map[string]any{"op": "remove", "path": path})
				body, _ := json.Marshal(patch)
				input, _ := json.Marshal(map[string]any{"kind": kind, "group": gvr.Group, "namespace": obj.GetNamespace(), "name": obj.GetName(), "patch_type": "json", "patch": string(body), "dry_run": true})
				namespaceArg := ""
				if obj.GetNamespace() != "" {
					namespaceArg = " -n " + shellQuote(obj.GetNamespace())
				}
				command := fmt.Sprintf("kubectl patch %s %s%s --type=json -p %s", shellQuote(gvr.Resource+"."+gvr.Group), shellQuote(obj.GetName()), namespaceArg, shellQuote(string(body)))
				if det.TerminatingRemovalPreviews == nil {
					det.TerminatingRemovalPreviews = map[string]string{}
				}
				det.TerminatingRemovalPreviews[finalizer] = fmt.Sprintf("For %q, preview with patch_resource %s or %s --dry-run=server. After reviewing, apply the same command without --dry-run=server.", finalizer, input, command)
			}
			out = append(out, det)
		}
	}
	return out
}

// Protection finalizers enforce dependency safety rather than operator cleanup.
// Kubernetes SIG subprojects publish under x-k8s.io alongside controllers that
// tear down infrastructure (Cluster API), so only their in-use and protection
// keys, named like the core guards, count.
func IsProtectionFinalizer(finalizer string) bool {
	domain, name, _ := strings.Cut(finalizer, "/")
	if domain == "k8s.io" || domain == "kubernetes.io" || strings.HasSuffix(domain, ".k8s.io") || strings.HasSuffix(domain, ".kubernetes.io") {
		return true
	}
	if domain == "x-k8s.io" || strings.HasSuffix(domain, ".x-k8s.io") {
		return strings.Contains(name, "in-use") || strings.Contains(name, "protection")
	}
	return false
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
