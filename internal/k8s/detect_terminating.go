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
func detectDynamicTerminatingProblems(dynamic *DynamicResourceCache, discovery *ResourceDiscovery, namespace string, now time.Time) []Detection {
	if dynamic == nil || discovery == nil {
		return nil
	}
	var out []Detection
	for _, gvr := range dynamic.WatchedGVRs() {
		kind := discovery.GetKindForGVR(gvr)
		resource, found := discovery.GetResourceWithGroup(kind, gvr.Group)
		if !found || !resource.IsCRD || !dynamic.IsSynced(gvr) {
			continue
		}
		if namespace != "" && !resource.Namespaced {
			continue
		}
		items, err := dynamic.ListWatchedReadOnly(gvr)
		if err != nil {
			log.Printf("[detect] Failed to list terminating %s: %v", gvr, err)
			continue
		}
		for _, obj := range items {
			if namespace != "" && obj.GetNamespace() != namespace {
				continue
			}
			det, ok := terminatingProblem(kind, gvr.Group, obj, now)
			if !ok {
				continue
			}
			det.Cause = fmt.Sprintf("Deletion has been pending for %s. %s.", det.Duration, det.Message)
			det.TerminatingFinalizers = append([]string(nil), obj.GetFinalizers()...)
			var actions []string
			for i, finalizer := range obj.GetFinalizers() {
				if finalizer == metav1.FinalizerDeleteDependents || finalizer == metav1.FinalizerOrphanDependents {
					continue
				}
				patch := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": string(obj.GetUID())}}
				path := fmt.Sprintf("/metadata/finalizers/%d", i)
				patch = append(patch, map[string]any{"op": "test", "path": path, "value": finalizer}, map[string]any{"op": "remove", "path": path})
				body, _ := json.Marshal(patch)
				input, _ := json.Marshal(map[string]any{"kind": kind, "group": gvr.Group, "namespace": obj.GetNamespace(), "name": obj.GetName(), "patch_type": "json", "patch": string(body), "dry_run": true})
				actions = append(actions, fmt.Sprintf("For %q, preview with patch_resource %s.", finalizer, input))
			}
			if len(actions) > 0 {
				det.Action = "Check the controller's logs and permissions first. If the controller is intentionally removed, remove its finalizer explicitly; this skips its cleanup and may leave external resources behind. " + strings.Join(actions, " ") + " Review the preview before applying with dry_run=false. Remove only one finalizer at a time and re-read the object before preparing the next patch; indices can change."
			}
			out = append(out, det)
		}
	}
	return out
}
