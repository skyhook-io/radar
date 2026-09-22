package issues

import (
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const strimziGroup = "kafka.strimzi.io"
const maxStrimziContextFindings = 5
const maxStrimziContextScan = 500

type strimziContextReader struct {
	get  func(Ref) (runtime.Object, error)
	list func(string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, error)
}

func CachedStrimziEvidence(obj any, canRead func(Ref) bool) *issuesapi.RelatedApplicationFindings {
	dynamic, discovery := k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()
	if dynamic == nil || discovery == nil || obj == nil {
		return nil
	}
	if _, ok := discovery.GetGVRWithGroup("KafkaConnect", strimziGroup); !ok {
		return nil
	}
	reader := strimziContextReader{
		get: func(ref Ref) (runtime.Object, error) {
			if k8s.TypedKindOwnsGroup(ref.Kind, ref.Group) {
				return k8s.FetchResource(k8s.GetResourceCache(), ref.Kind, ref.Namespace, ref.Name)
			}
			gvr, ok := discovery.GetGVRWithGroup(ref.Kind, ref.Group)
			if !ok {
				return nil, k8s.ErrUnknownDynamicKind
			}
			if !dynamic.IsNamespaceSynced(gvr, ref.Namespace) {
				return nil, fmt.Errorf("cache not synced")
			}
			return dynamic.GetWatched(gvr, ref.Namespace, ref.Name)
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
	return strimziEvidence(runtimeObj, canRead, reader)
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
			name := m.GetLabels()["strimzi.io/cluster"]
			if name == "" {
				return nil
			}
			ref := Ref{Group: strimziGroup, Kind: "KafkaConnect", Namespace: m.GetNamespace(), Name: name}
			if canRead != nil && !canRead(ref) {
				return nil
			}
			parent, err := reader.get(ref)
			if err != nil || parent == nil {
				return nil
			}
			connect = ref
			break
		}
		owner := metav1.GetControllerOf(m)
		// Strimzi's PodSet references KafkaConnect without controller=true.
		if owner == nil && len(m.GetOwnerReferences()) == 1 {
			sole := m.GetOwnerReferences()[0]
			owner = &sole
		}
		if owner == nil || owner.UID == "" {
			return nil
		}
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err != nil {
			return nil
		}
		ref := Ref{Group: gv.Group, Kind: owner.Kind, Namespace: m.GetNamespace(), Name: owner.Name}
		if canRead != nil && !canRead(ref) {
			return nil
		}
		parent, err := reader.get(ref)
		if err != nil || parent == nil {
			return nil
		}
		pm, err := meta.Accessor(parent)
		if err != nil || pm.GetUID() != owner.UID {
			return nil
		}
		current = parent
	}
	if connect.Name == "" || canRead != nil && !canRead(connect) {
		return nil
	}
	out := &issuesapi.RelatedApplicationFindings{Source: connect, Coverage: "Only authorized, currently cached connector status is included; missing findings do not establish application health."}
	if obj.GetObjectKind().GroupVersionKind().Kind == "KafkaConnector" {
		return out
	}
	rows, gvr, truncated, err := reader.list(connect.Namespace)
	if err != nil {
		return out
	}
	if truncated || len(rows) > maxStrimziContextScan {
		out.Truncated = true
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
	checked := 0
	for _, row := range matched {
		checked++
		findings := detectStrimziConnectorIssues(gvr, row)
		if len(findings) == 0 {
			continue
		}
		if len(out.Findings) == maxStrimziContextFindings {
			out.Truncated = true
			break
		}
		out.Findings = append(out.Findings, findings...)
	}
	out.Coverage = fmt.Sprintf("Checked %d authorized cached connector snapshots; missing findings do not establish application health.", checked)
	return out
}
