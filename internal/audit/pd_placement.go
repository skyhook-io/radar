package audit

import (
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/gkedisk"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// pdPlacementClusterResources are the cluster-scoped kinds the Persistent
// Disk placement check reads. The evidence names nodes and their machine
// types, so a reader who can't list all three gets no evidence at all.
var pdPlacementClusterResources = []schema.GroupVersionResource{
	{Version: "v1", Resource: "nodes"},
	{Version: "v1", Resource: "persistentvolumes"},
	{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"},
}

// collectPDPlacement gathers the Hyperdisk-only nodes and the disk type of
// every claim in the audited namespaces. It returns nil, so the check skips,
// when the reader lacks access, an inventory isn't a synced cluster-wide
// cache, or the cluster has no Hyperdisk-only nodes.
func collectPDPlacement(cache *k8s.ResourceCache, namespaces []string, scope *ReadScope) *bp.PDPlacementEvidence {
	for _, gvr := range pdPlacementClusterResources {
		if !scope.allows(gvr, "") {
			return nil
		}
	}
	for _, kind := range []string{k8score.Nodes, k8score.PersistentVolumes, k8score.StorageClasses} {
		if !cache.IsKindReady(kind) || !cache.IsKindClusterWide(kind) {
			return nil
		}
	}
	if cache.Nodes() == nil || cache.PersistentVolumes() == nil || cache.StorageClasses() == nil || cache.PersistentVolumeClaims() == nil {
		return nil
	}
	nodes, err := cache.Nodes().List(labels.Everything())
	if err != nil {
		return nil
	}
	ev := &bp.PDPlacementEvidence{ClaimDiskTypes: map[string]string{}, ClaimVolumes: map[string]*corev1.PersistentVolume{}}
	for _, n := range nodes {
		if gkedisk.HyperdiskOnly(n.Labels[gkedisk.InstanceTypeLabel]) {
			ev.HyperdiskOnlyNodes = append(ev.HyperdiskOnlyNodes, n)
		}
	}
	if len(ev.HyperdiskOnlyNodes) == 0 {
		return nil
	}
	storageClass := func(name string) *storagev1.StorageClass {
		sc, err := cache.StorageClasses().Get(name)
		if err != nil {
			return nil
		}
		return sc
	}
	for _, pvc := range ListNamespaced(cache.PersistentVolumeClaims(), namespaces) {
		if pvc.Spec.VolumeName == "" {
			continue
		}
		pv, err := cache.PersistentVolumes().Get(pvc.Spec.VolumeName)
		if err != nil {
			continue
		}
		key := pvc.Namespace + "/" + pvc.Name
		ev.ClaimVolumes[key] = pv
		if diskType, ok := gkedisk.DiskType(pv, storageClass); ok {
			ev.ClaimDiskTypes[key] = diskType
		}
	}
	return ev
}
