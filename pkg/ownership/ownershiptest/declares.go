package ownershiptest

import (
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// DeclaringSpec is the pod spec that declares declares: a volume for each
// ConfigMap, Secret and PersistentVolumeClaim, and the ServiceAccount.
func DeclaringSpec(declares []resourceid.Ref) corev1.PodSpec {
	var spec corev1.PodSpec
	for _, d := range declares {
		switch d.Kind {
		case "ConfigMap":
			spec.Volumes = append(spec.Volumes, corev1.Volume{Name: "cm-" + d.Name, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: d.Name}}}})
		case "Secret":
			spec.Volumes = append(spec.Volumes, corev1.Volume{Name: "secret-" + d.Name, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: d.Name}}})
		case "PersistentVolumeClaim":
			spec.Volumes = append(spec.Volumes, corev1.Volume{Name: "pvc-" + d.Name, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: d.Name}}})
		case "ServiceAccount":
			spec.ServiceAccountName = d.Name
		}
	}
	return spec
}

// ApplyDeclarations writes declares into obj's pod spec, or its pod
// template's, when obj is a Pod, ReplicaSet, Deployment, Job or Rollout.
func ApplyDeclarations(obj runtime.Object, declares []resourceid.Ref) {
	if len(declares) == 0 {
		return
	}
	spec := DeclaringSpec(declares)
	switch o := obj.(type) {
	case *unstructured.Unstructured:
		if o.GroupVersionKind().GroupKind() == (schema.GroupKind{Group: ArgoRollout.Group, Kind: ArgoRollout.Kind}) {
			template, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
			if err == nil {
				_ = unstructured.SetNestedMap(o.Object, template, "spec", "template", "spec")
			}
		}
	case *corev1.Pod:
		o.Spec = spec
	case *appsv1.ReplicaSet:
		o.Spec.Template.Spec = spec
	case *appsv1.Deployment:
		o.Spec.Template.Spec = spec
	case *batchv1.Job:
		o.Spec.Template.Spec = spec
	}
}

// APIObject is o as an API object: typed for a typed kind, unstructured
// otherwise, with its metadata and declarations. Status is not written.
func APIObject(o Object) runtime.Object {
	k, _ := KindOf(o.Ref.GroupKind())
	gvk := schema.GroupVersionKind{Group: k.Group, Version: k.Version, Kind: k.Kind}
	var obj runtime.Object = &unstructured.Unstructured{}
	if k.Typed {
		if typed, err := scheme.Scheme.New(gvk); err == nil {
			obj = typed
		}
	}
	obj.GetObjectKind().SetGroupVersionKind(gvk)
	m, _ := apimeta.Accessor(obj)
	m.SetNamespace(o.Ref.Namespace)
	m.SetName(o.Ref.Name)
	m.SetUID(o.UID)
	m.SetResourceVersion(o.ResourceVersion)
	m.SetOwnerReferences(slices.Clone(o.OwnerReferences))
	m.SetLabels(o.Labels)
	m.SetAnnotations(o.Annotations)
	m.SetDeletionTimestamp(o.DeletionTimestamp)
	m.SetFinalizers(o.Finalizers)
	ApplyDeclarations(obj, o.Declares)
	return obj
}

// Borrow is Object, lending o as an API object when Present.
func (s scanSession) Borrow(ref resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	read := s.Object(ref)
	if read.State != ownership.Present {
		return nil, read
	}
	return APIObject(s.byRef[ref]), read
}

// Indexed answers the namespace index by each object's namespace, and an
// empty index with every readable object; it knows no other index.
func (s scanSession) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	var r relations.IndexedRead
	gap := func(gk resourceid.GroupKind, namespace string, reason readreason.Code) {
		r.Gaps = append(r.Gaps, ownership.Gap{Kind: gk, Namespace: namespace, Reason: reason})
	}
	var namespaces []string
	if !scope.AllNamespaces {
		for _, ns := range slices.Compact(slices.Sorted(slices.Values(scope.Namespaces))) {
			if ns == "" {
				gap(resourceid.GroupKind{}, "", readreason.NamespaceRequired)
			} else {
				namespaces = append(namespaces, ns)
			}
		}
	}
	namespaced, served := s.scope(gk)
	switch {
	case s.ambiguous(gk):
		gap(gk, "", readreason.ReadError)
	case !served && !s.covers(gk.Group):
		gap(gk, "", readreason.ReadError)
	case served:
		read, ok := s.readable(dependentKind{gk: gk, namespaced: namespaced}, scope, namespaces, gap)
		if !ok {
			break
		}
		// Another index only narrows what the caller evaluates, so every
		// readable object stands in for what it files under keys. With no
		// keys an index files nothing.
		for _, o := range s.byKind[gk] {
			if read(o) && (index == "" || len(keys) > 0) && (index != cache.NamespaceIndex || slices.Contains(keys, o.Ref.Namespace)) {
				r.Objects = append(r.Objects, relations.IndexedObject{Ref: o.Ref, Object: APIObject(o)})
			}
		}
	}
	slices.SortFunc(r.Objects, func(a, b relations.IndexedObject) int { return compareRefs(a.Ref, b.Ref) })
	slices.SortFunc(r.Gaps, compareGaps)
	r.Gaps = slices.Compact(r.Gaps)
	return r
}

var _ relations.Session = scanSession{}
