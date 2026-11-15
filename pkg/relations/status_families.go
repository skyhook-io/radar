package relations

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var (
	kindPod          = resourceid.GroupKind{Kind: "Pod"}
	kindNode         = resourceid.GroupKind{Kind: "Node"}
	kindConfigMap    = resourceid.GroupKind{Kind: "ConfigMap"}
	kindPVC          = resourceid.GroupKind{Kind: "PersistentVolumeClaim"}
	kindPV           = resourceid.GroupKind{Kind: "PersistentVolume"}
	kindStorageClass = resourceid.GroupKind{Group: "storage.k8s.io", Kind: "StorageClass"}
	kindNodeClaim    = resourceid.GroupKind{Group: karpenter.Group, Kind: "NodeClaim"}
	kindMachine      = resourceid.GroupKind{Group: configrefs.CAPIGroup, Kind: "Machine"}
)

// PodPlacement is the family of the Node a Pod runs on. A Pod that has
// finished runs nowhere. A Node's Pods are found by reading every Pod the
// caller may read: an index of Pods by Node would cost each Pod relist more
// than the rare question costs (docs/loadtest.md).
var PodPlacement = &Family{
	Name:    "podPlacement",
	Sources: []Source{{Kind: kindPod}},
	Targets: []resourceid.GroupKind{kindNode},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		pod, ok := obj.(*corev1.Pod)
		if !ok || pod.Spec.NodeName == "" || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			return nil
		}
		visit(Declaration{Target: resourceid.NewRef("", "Node", "", pod.Spec.NodeName), Path: "spec.nodeName"})
		return nil
	},
}

// Reflections is the family of the source a Reflector mirror copies: a
// Secret or ConfigMap whose reflects annotation names another of its kind,
// which may be in another namespace. A source's mirrors are found through the
// stores' index of that annotation.
var Reflections = &Family{
	Name: "reflections",
	Sources: []Source{
		{Kind: kindSecret, Targets: []resourceid.GroupKind{kindSecret}},
		{Kind: kindConfigMap, Targets: []resourceid.GroupKind{kindConfigMap}},
	},
	Targets: []resourceid.GroupKind{kindSecret, kindConfigMap},
	ReferrersIndex: &ReferrersIndex{
		Name: k8score.ReflectsIndex,
		Key:  func(source resourceid.Ref) string { return k8score.ReflectsKey(source.Namespace, source.Name) },
	},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		var kind string
		var m metav1.Object
		switch o := obj.(type) {
		case *corev1.Secret:
			kind, m = "Secret", o
		case *corev1.ConfigMap:
			kind, m = "ConfigMap", o
		default:
			return nil
		}
		if source, ok := configrefs.ReflectedSource(kind, m.GetNamespace(), m.GetName(), m.GetAnnotations()); ok {
			visit(Declaration{Target: resourceid.NewRef("", kind, source.Namespace, source.Name), Path: "metadata.annotations[" + configrefs.ReflectsAnnotation + "]"})
		}
		return nil
	},
}

// Storage is the family of a claim's volume and class, and a volume's claim
// and class: a PersistentVolumeClaim's bound PersistentVolume and
// StorageClass, and a PersistentVolume's claimRef and StorageClass.
var Storage = &Family{
	Name: "storage",
	Sources: []Source{
		{Kind: kindPVC, Targets: []resourceid.GroupKind{kindPV, kindStorageClass}},
		{Kind: kindPV, Targets: []resourceid.GroupKind{kindPVC, kindStorageClass}},
	},
	Targets: []resourceid.GroupKind{kindPV, kindPVC, kindStorageClass},
	// A volume is cluster-scoped and names a claim in its namespace.
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		storageClass := func(name *string, path string) {
			if name != nil && *name != "" {
				visit(Declaration{Target: resourceid.NewRef("storage.k8s.io", "StorageClass", "", *name), Path: path})
			}
		}
		switch o := obj.(type) {
		case *corev1.PersistentVolumeClaim:
			if o.Spec.VolumeName != "" {
				visit(Declaration{Target: resourceid.NewRef("", "PersistentVolume", "", o.Spec.VolumeName), Path: "spec.volumeName"})
			}
			storageClass(o.Spec.StorageClassName, "spec.storageClassName")
		case *corev1.PersistentVolume:
			if c := o.Spec.ClaimRef; c != nil && c.Name != "" && c.Namespace != "" {
				visit(Declaration{Target: resourceid.NewRef("", "PersistentVolumeClaim", c.Namespace, c.Name), Path: "spec.claimRef"})
			}
			storageClass(&o.Spec.StorageClassName, "spec.storageClassName")
		}
		return nil
	},
}

// MachineNodes is the family of the Node a Karpenter NodeClaim or a Cluster
// API Machine launched, as its status records it.
var MachineNodes = &Family{
	Name:    "machineNodes",
	Sources: []Source{{Kind: kindNodeClaim}, {Kind: kindMachine}},
	Targets: []resourceid.GroupKind{kindNode},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindNodeClaim); ok {
			if name := karpenter.ClaimNodeName(u); name != "" {
				visit(Declaration{Target: resourceid.NewRef("", "Node", "", name), Path: "status.nodeName"})
			}
		}
		if u, ok := unstructuredOf(obj, kindMachine); ok {
			if name, _, _ := unstructured.NestedString(u.Object, "status", "nodeRef", "name"); name != "" {
				visit(Declaration{Target: resourceid.NewRef("", "Node", "", name), Path: "status.nodeRef"})
			}
		}
		return nil
	},
}

// AnalysisRuns is the family of the AnalysisRuns an Argo Rollout's status
// points at: the current ones, not its history.
var AnalysisRuns = &Family{
	Name:    "analysisRuns",
	Sources: []Source{{Kind: kindRollout}},
	Targets: []resourceid.GroupKind{{Group: configrefs.ArgoGroup, Kind: "AnalysisRun"}},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindRollout); ok {
			for _, r := range configrefs.RolloutAnalysisRuns(u) {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}
