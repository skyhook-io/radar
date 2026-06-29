package summarycontext

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/internal/k8s"
)

// AttachToTypedList fills in SummaryContext for each *aicontext.ResourceSummary
// row produced from typed runtime.Object items (typed-cache list path).
// results and objs must be parallel slices — length mismatch is treated as a
// caller bug and the function returns without touching the rows.
//
// Group is sourced per-object from the typed object's GVK via SetTypeMeta +
// GetObjectKind, so list paths that mix kinds stay correct.
func AttachToTypedList(results []any, objs []runtime.Object, builder Builder) {
	if len(results) != len(objs) {
		return
	}
	for i, row := range results {
		summary, ok := row.(*aicontext.ResourceSummary)
		if !ok || summary == nil {
			continue
		}
		group := GroupFromObject(objs[i])
		summary.SummaryContext = builder(objs[i], nil, group, summary.Kind, summary.Namespace, summary.Name)
	}
}

// AttachToUnstructuredList is the dynamic-CRD counterpart of
// AttachToTypedList. Group comes from each item's apiVersion so two CRDs that
// share kind+ns+name across API groups (e.g. multiple operators each shipping
// a "Cluster" resource) get independent issue counts.
func AttachToUnstructuredList(results []any, items []*unstructured.Unstructured, builder Builder) {
	if len(results) != len(items) {
		return
	}
	for i, row := range results {
		summary, ok := row.(*aicontext.ResourceSummary)
		if !ok || summary == nil {
			continue
		}
		group := GroupFromUnstructured(items[i])
		summary.SummaryContext = builder(nil, items[i], group, summary.Kind, summary.Namespace, summary.Name)
	}
}

// GroupFromObject extracts the API group from a typed runtime.Object's
// GroupVersionKind. Returns "" for core-group objects (Pod, Service, etc.)
// and when the GVK is unset. Calls k8s.SetTypeMeta so the GVK is populated
// from scheme metadata when the object came out of the typed cache without
// it set.
func GroupFromObject(obj runtime.Object) string {
	if obj == nil {
		return ""
	}
	k8s.SetTypeMeta(obj)
	return obj.GetObjectKind().GroupVersionKind().Group
}

// GroupFromUnstructured pulls the API group from an unstructured's apiVersion.
// Mirrors GroupFromObject for the dynamic-CRD path.
func GroupFromUnstructured(u *unstructured.Unstructured) string {
	if u == nil {
		return ""
	}
	return u.GroupVersionKind().Group
}
