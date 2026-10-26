package issues

import (
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const strimziGroup = "kafka.strimzi.io"
const maxStrimziContextFindings = 5
const maxStrimziContextScan = 500

type strimziContextReader struct {
	get   func(Ref) (runtime.Object, error)
	owner func(runtime.Object) *topology.ResourceRef
	list  func(string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, error)
}

func CachedStrimziEvidence(obj any, canRead func(Ref) bool, ownerAccess func(*ownership.Catalog) ownership.Access) *issuesapi.RelatedApplicationFindings {
	typed, dynamic, discovery := k8s.GetResourceCache(), k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()
	if dynamic == nil || discovery == nil || obj == nil {
		return nil
	}
	if _, ok := discovery.GetGVRWithGroup("KafkaConnect", strimziGroup); !ok {
		return nil
	}
	reader := strimziContextReader{
		owner: func(obj runtime.Object) *topology.ResourceRef {
			fetched, ok := k8s.OwnershipObservation(obj)
			if !ok {
				return nil
			}
			rel := &topology.Relationships{}
			k8s.AnswerOwner(rel, fetched, ownerAccess)
			return rel.Owner
		},
		get: func(ref Ref) (runtime.Object, error) {
			if k8s.TypedKindOwnsGroup(ref.Kind, ref.Group) {
				if typed == nil {
					return nil, fmt.Errorf("typed cache is unavailable")
				}
				gvr, _ := k8s.BuiltinGVR(ref.Kind, ref.Group)
				obj, found, readable := typed.ReadableObject(gvr.Resource, ref.Namespace, ref.Name)
				if !readable || !found {
					return nil, fmt.Errorf("owner is unavailable in the cache")
				}
				copy := obj.DeepCopyObject()
				k8s.SetTypeMeta(copy)
				return copy, nil
			}
			gvr, ok := discovery.GetGVRWithGroup(ref.Kind, ref.Group)
			if !ok {
				return nil, k8s.ErrUnknownDynamicKind
			}
			obj, found, readable := dynamic.ReadableObject(gvr, ref.Namespace, ref.Name)
			if !readable || !found {
				return nil, fmt.Errorf("owner is unavailable in the cache")
			}
			return obj, nil
		},
		list: func(namespace string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, error) {
			gvr, ok := discovery.GetGVRWithGroup("KafkaConnector", strimziGroup)
			if !ok {
				return nil, gvr, false, k8s.ErrUnknownDynamicKind
			}
			if !dynamic.IsNamespaceSynced(gvr, namespace) {
				return nil, gvr, false, fmt.Errorf("cache not synced")
			}
			rows, truncated, err := dynamic.ListWatchedNamespaceReadOnly(gvr, namespace, maxStrimziContextScan)
			return rows, gvr, truncated, err
		},
	}
	runtimeObj, ok := obj.(runtime.Object)
	if !ok {
		return nil
	}
	m, err := meta.Accessor(runtimeObj)
	if err != nil || m.GetUID() == "" {
		return nil
	}
	gvk := runtimeObj.GetObjectKind().GroupVersionKind()
	current, err := reader.get(Ref{Group: gvk.Group, Kind: gvk.Kind, Namespace: m.GetNamespace(), Name: m.GetName()})
	if err != nil {
		return nil
	}
	currentMeta, err := meta.Accessor(current)
	if err != nil || currentMeta.GetUID() != m.GetUID() {
		return nil
	}
	out := strimziEvidence(runtimeObj, canRead, reader)
	if typed != k8s.GetResourceCache() || dynamic != k8s.GetDynamicResourceCache() || discovery != k8s.GetResourceDiscovery() || dynamic.IsStopped() {
		return nil
	}
	return out
}

func strimziEvidence(obj runtime.Object, canRead func(Ref) bool, reader strimziContextReader) *issuesapi.RelatedApplicationFindings {
	current := obj
	var connect Ref
	for depth := 0; depth < 6 && current != nil; depth++ {
		m, err := meta.Accessor(current)
		if err != nil {
			return nil
		}
		gvk := current.GetObjectKind().GroupVersionKind()
		if gvk.Group == strimziGroup && gvk.Kind == "KafkaConnect" {
			connect = Ref{Group: strimziGroup, Kind: "KafkaConnect", Namespace: m.GetNamespace(), Name: m.GetName()}
			break
		}
		if gvk.Group == strimziGroup && gvk.Kind == "KafkaConnector" {
			return nil
		}
		owner := reader.owner(current)
		if owner == nil {
			return nil
		}
		ref := Ref{Group: owner.Group, Kind: owner.Kind, Namespace: owner.Namespace, Name: owner.Name}

		parent, err := reader.get(ref)
		if err != nil || parent == nil {
			return nil
		}
		pm, err := meta.Accessor(parent)
		if err != nil || pm.GetUID() == "" {
			return nil
		}
		matched := false
		for _, declared := range m.GetOwnerReferences() {
			gv, err := schema.ParseGroupVersion(declared.APIVersion)
			if err == nil && gv.Group == ref.Group && declared.Kind == ref.Kind && declared.Name == ref.Name && declared.UID == pm.GetUID() {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		current = parent
	}
	if connect.Name == "" || canRead != nil && !canRead(connect) {
		return nil
	}
	out := &issuesapi.RelatedApplicationFindings{CoverageState: "unavailable", Source: connect, Coverage: "Only authorized, currently cached connector status is included; missing findings do not establish application health."}
	rows, gvr, truncated, err := reader.list(connect.Namespace)
	if err != nil {
		return out
	}
	if truncated || len(rows) > maxStrimziContextScan {
		out.Truncated = true
		out.CoverageState = "limited"
		out.Coverage = "Connector findings were not checked because this namespace exceeds the inspection budget; missing findings do not establish application health."
		return out
	}
	var matched []*unstructured.Unstructured
	for _, row := range rows {
		if row.GetAPIVersion() != gvr.GroupVersion().String() || row.GetKind() != "KafkaConnector" || row.GetNamespace() != connect.Namespace || row.GetLabels()["strimzi.io/cluster"] != connect.Name {
			continue
		}
		ref := Ref{Group: strimziGroup, Kind: "KafkaConnector", Namespace: row.GetNamespace(), Name: row.GetName()}
		if canRead != nil && !canRead(ref) {
			continue
		}
		matched = append(matched, row)
	}
	sort.Slice(matched, func(i, j int) bool { return strings.Compare(matched[i].GetName(), matched[j].GetName()) < 0 })
	checked, unknown := 0, 0
	for _, row := range matched {
		checked++
		findings := detectStrimziConnectorIssues(gvr, row)
		findings, _ = filterShapedIssues(findings, Filters{})
		if len(findings) == 0 && !StrimziConnectorRecoveryObserved(row) {
			unknown++
		}
		if len(findings) == 0 {
			continue
		}
		if len(out.Findings) == maxStrimziContextFindings {
			out.Truncated = true
			continue
		}
		out.Findings = append(out.Findings, findings...)
	}
	out.Checked, out.Unknown = checked, unknown
	out.CoverageState = "cached"
	if out.Truncated || unknown > 0 {
		out.CoverageState = "limited"
	}
	out.Coverage = fmt.Sprintf("Checked %d authorized cached connector snapshots; %d were unverifiable or inactive. Missing findings do not establish application health.", checked, unknown)
	if out.Truncated {
		out.Coverage += " The five-finding limit was reached; additional findings may be omitted."
	}
	return out
}
