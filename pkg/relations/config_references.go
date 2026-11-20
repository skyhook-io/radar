package relations

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ConfigReferences is the family of the ConfigMaps, Secrets,
// PersistentVolumeClaims and ServiceAccount a Pod, a pod-template workload or
// an Ingress's TLS declares, as pkg/configrefs reads them. A retired revision
// (RetiredRevision) declares nothing, as the Topology screen draws none, so it
// is never counted among what uses a ConfigMap or Secret; OwnConfigReferences
// still reads its template. A Deployment scaled to zero still declares
// through its own template.
var ConfigReferences = &Family{
	Name:       "configReferences",
	Projection: project("configures", true, "ConfigRefs", "Consumers").whenTarget(kindPVC, "uses", true, "StorageRefs", "Consumers").whenTarget(resourceid.GroupKind{Kind: "ServiceAccount"}, "configures", true, "ServiceAccount", "Consumers"),
	Sources: []Source{
		{Kind: resourceid.GroupKind{Group: "apps", Kind: "Deployment"}},
		{Kind: resourceid.GroupKind{Group: configrefs.ArgoGroup, Kind: "Rollout"}},
		{Kind: resourceid.GroupKind{Group: "apps", Kind: "StatefulSet"}},
		{Kind: resourceid.GroupKind{Group: "apps", Kind: "DaemonSet"}},
		{Kind: resourceid.GroupKind{Group: "batch", Kind: "CronJob"}},
		{Kind: resourceid.GroupKind{Group: "batch", Kind: "Job"}},
		{Kind: resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}},
		{Kind: resourceid.GroupKind{Group: "networking.k8s.io", Kind: "Ingress"}, Targets: []resourceid.GroupKind{{Kind: "Secret"}}},
		{Kind: resourceid.GroupKind{Kind: "Pod"}},
		{Kind: resourceid.GroupKind{Kind: "ServiceAccount"}, Targets: []resourceid.GroupKind{{Kind: "Secret"}}},
	},
	Targets: []resourceid.GroupKind{
		{Kind: "ConfigMap"}, {Kind: "Secret"}, {Kind: "PersistentVolumeClaim"}, {Kind: "ServiceAccount"},
	},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if RetiredRevision(obj) {
			return nil
		}
		return declaredConfigReferences(obj, visit)
	},
}

// OwnConfigReferences is ConfigReferences as a subject's own answer reads it:
// a retired revision declares through its template, what it would use if
// scaled up again. It is never searched for referrers.
var OwnConfigReferences = func() *Family {
	f := *ConfigReferences
	f.Declared = declaredConfigReferences
	return &f
}()

// RetiredRevision reports a ReplicaSet scaled explicitly to zero: usually a
// Deployment's old revision, kept for rollback.
func RetiredRevision(obj runtime.Object) bool {
	rs, ok := obj.(*appsv1.ReplicaSet)
	return ok && rs.Spec.Replicas != nil && *rs.Spec.Replicas == 0
}

func declaredConfigReferences(obj runtime.Object, visit func(Declaration)) error {
	if sa, ok := obj.(*corev1.ServiceAccount); ok {
		for _, r := range configrefs.ServiceAccountSecretReferences(sa) {
			visit(Declaration{Target: resourceid.Ref{Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}, Path: r.Path})
		}
		return nil
	}
	_, err := configrefs.VisitObjectReferences(obj, func(r configrefs.PodSpecReference) {
		visit(Declaration{Target: resourceid.Ref{Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}, Path: r.Path})
	})
	return err
}
