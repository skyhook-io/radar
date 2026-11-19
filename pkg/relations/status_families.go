package relations

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
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
	Name:          "podPlacement",
	Projection:    Disposition{DirectRead: "getRelationshipsWithObject reads Pod.Node and Node.Pods through the access-aware relationDirectProvider"},
	ScanRationale: "An index of Pods by Node costs every Pod relist more than the rare Node question costs (docs/loadtest.md#relationship-cost-baselines).",
	Sources:       []Source{{Kind: kindPod}},
	Targets:       []resourceid.GroupKind{kindNode},
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
	Name:           "reflections",
	Projection:     project("configures", true, "ConfigRefs,Reflection.Source", "Consumers,Reflection.Mirrors").labelled("Reflects to"),
	CrossNamespace: true,
	Sources: []Source{
		{Kind: kindSecret, Targets: []resourceid.GroupKind{kindSecret}},
		{Kind: kindConfigMap, Targets: []resourceid.GroupKind{kindConfigMap}},
	},
	Targets:        []resourceid.GroupKind{kindSecret, kindConfigMap},
	ReferrersIndex: referrersIndex("reflections"),
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
	ScanRationale: "Indexing a claim's or volume's binding and StorageClass exceeds the per-update admission bar (BenchmarkRelationIndexUpdate).",
	Name:          "storage",
	Projection:    Disposition{DirectRead: "getRelationshipsWithObject reads the PVC/PV binding and StorageClass; relationDirectProvider authorizes reverse PV/PVC lists"},
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
	ReferrersIndex: referrersIndex("machineNodes"),
	Name:           "machineNodes",
	Projection:     project("manages", false, "Manages", "Managers"),
	Sources:        []Source{{Kind: kindNodeClaim}, {Kind: kindMachine}},
	Targets:        []resourceid.GroupKind{kindNode},
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
	Name:       "analysisRuns",
	Projection: project("manages", false, "Manages", "Managers"),
	Sources:    []Source{{Kind: kindRollout}},
	Targets:    []resourceid.GroupKind{{Group: configrefs.ArgoGroup, Kind: "AnalysisRun"}},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, kindRollout); ok {
			for _, r := range configrefs.RolloutAnalysisRuns(u) {
				visitPathRef(visit, r)
			}
		}
		return nil
	},
}

// SealedSecrets is the family of the Secret a Bitnami SealedSecret
// produces: the controller names it after the SealedSecret, whatever
// spec.template.metadata.name says. The Secret's consumers are its
// ConfigReferences referrers, also when the caller may not read the Secret.
var SealedSecrets = &Family{
	Name:       "sealedSecrets",
	Projection: project("manages", false, "Manages", "Managers"),
	Sources:    []Source{{Kind: resourceid.GroupKind{Group: "bitnami.com", Kind: "SealedSecret"}}},
	Targets:    []resourceid.GroupKind{kindSecret},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		if u, ok := unstructuredOf(obj, resourceid.GroupKind{Group: "bitnami.com", Kind: "SealedSecret"}); ok {
			visit(Declaration{Target: resourceid.NewRef("", "Secret", u.GetNamespace(), u.GetName()), Path: "metadata.name"})
		}
		return nil
	},
}
