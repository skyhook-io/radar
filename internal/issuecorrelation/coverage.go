package issuecorrelation

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// checkCollected records why Radar holds no changes for a source's kind in
// namespace ("" for a cluster-scoped kind): it doesn't collect the kind, or
// not in that namespace, or it is still loading it. An empty answer about
// such a source means nothing.
func (c *run) checkCollected(kind, group, namespace string) {
	c.collected(kind, group, namespace)
}

// collected is checkCollected that also reports whether the kind is ready:
// collected in namespace and synced.
func (c *run) collected(kind, group, namespace string) bool {
	if reason, ok := collectionGap(kind, group, namespace); ok {
		c.addUnknown(reason, group, kind)
		return false
	}
	return true
}

func collectionGap(kind, group, namespace string) (issuesapi.CorrelationUnknownReason, bool) {
	if b, builtin := resourceid.BuiltinForKind(kind); builtin && (group == "" || b.Group == group) {
		if cache := k8s.GetResourceCache(); cache != nil {
			if g, resource, _, ok := k8s.ResolveChangeGVR(kind, b.Group); ok && g == b.Group {
				if synced, known := cache.InformerSynced(resource); known {
					switch {
					case !cache.KindCoversNamespace(resource, namespace):
						return issuesapi.CorrelationUnknownNamespaceNotCollected, true
					case !synced:
						return issuesapi.CorrelationUnknownKindSyncPending, true
					}
					return "", false
				}
			}
		}
	}
	disc := k8s.GetResourceDiscovery()
	dyn := k8s.GetDynamicResourceCache()
	if disc == nil || dyn == nil {
		return issuesapi.CorrelationUnknownKindNotCollected, true
	}
	gvr, ok := disc.GetGVRWithGroup(kind, group)
	if !ok {
		return issuesapi.CorrelationUnknownKindNotCollected, true
	}
	if dyn.IsNamespaceSynced(gvr, namespace) {
		return "", false
	}
	if dyn.HasCoveringInformer(gvr, namespace) {
		return issuesapi.CorrelationUnknownKindSyncPending, true
	}
	return issuesapi.CorrelationUnknownKindNotCollected, true
}
