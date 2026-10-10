package audit

import (
	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var storageClusterResources = []schema.GroupVersionResource{
	{Version: "v1", Resource: "persistentvolumes"},
	{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"},
}

var typedPVCConsumers = []string{k8score.Pods, k8score.Deployments, k8score.ReplicaSets, k8score.StatefulSets, k8score.DaemonSets, k8score.Jobs, k8score.CronJobs}

func collectStorageInput(cache *k8s.ResourceCache, namespaces []string, scope *ReadScope) *bp.CheckInput {
	input := &bp.CheckInput{}
	if cache.KindReadinessFor(k8score.PersistentVolumeClaims) == k8score.KindReady {
		input.PersistentVolumeClaims = ListNamespaced(cache.PersistentVolumeClaims(), namespaces)
	}
	seen := map[string]bool{}
	for _, pvc := range input.PersistentVolumeClaims {
		if seen[pvc.Namespace] {
			continue
		}
		seen[pvc.Namespace] = true
		complete := true
		for _, resource := range typedPVCConsumers {
			complete = complete && typedConfigCoverage(cache, resource, pvc.Namespace)
		}
		if complete {
			input.PVCConsumerNamespaces = append(input.PVCConsumerNamespaces, pvc.Namespace)
		}
	}
	if scope.allows(storageClusterResources[0], "") && typedConfigCoverage(cache, k8score.PersistentVolumes, "") {
		input.PersistentVolumes = ListNamespaced(cache.PersistentVolumes(), nil)
	}
	if scope.allows(storageClusterResources[1], "") && typedConfigCoverage(cache, k8score.StorageClasses, "") {
		input.StorageClasses = ListNamespaced(cache.StorageClasses(), nil)
	}
	needsEvents := false
	for _, pv := range input.PersistentVolumes {
		if pv.Status.Phase == corev1.VolumeReleased && pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete {
			needsEvents = true
			break
		}
	}
	if !needsEvents {
		return input
	}
	var eventNamespaces []string
	if scope != nil {
		eventNamespaces = scope.Namespaces
	}
	// The view selects PVC subjects; PV events may live in a different
	// namespace, but must still be within the caller's namespace grants.
	if cache.KindReadinessFor(k8score.Events) == k8score.KindReady && (scope == nil || scope.Namespaces == nil || len(eventNamespaces) > 0) {
		input.Events = ListNamespaced(cache.Events(), eventNamespaces)
	}
	input.PVDeletionEventsComplete = typedConfigCoverage(cache, k8score.Events, "") && (scope == nil || scope.Namespaces == nil)
	return input
}
