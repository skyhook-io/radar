package issues

import (
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const strimziGroup = "kafka.strimzi.io"
const maxStrimziContextFindings = 5
const maxStrimziContextScan = 500

type strimziContextReader struct {
	owner func(resourceid.Ref) ownership.OwnerAnswer
	list  func(string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, readreason.Code)
}

func CachedStrimziEvidence(obj any, canRead func(Ref) bool, ownerAccess func(*ownership.Catalog) ownership.Access) *issuesapi.RelatedApplicationFindings {
	typed, dynamic, discovery := k8s.GetResourceCache(), k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()
	if dynamic == nil || discovery == nil || obj == nil {
		return nil
	}
	if _, ok := discovery.GetGVRWithGroup("KafkaConnect", strimziGroup); !ok {
		return nil
	}
	runtimeObj, ok := obj.(runtime.Object)
	if !ok {
		return nil
	}
	fetched, ok := k8s.OwnershipObservation(runtimeObj)
	if !ok || fetched.UID == "" {
		return nil
	}
	session, err := k8s.BeginOwnershipRead(typed)
	if err != nil {
		return nil
	}
	current := session.Object(fetched.Ref)
	if current.State != ownership.Present || current.Object.UID != fetched.UID {
		return nil
	}
	view := ownership.NewView(session, ownerAccess(session.Catalog()))
	observed := map[resourceid.Ref]ownership.Observation{fetched.Ref: current.Object}
	reader := strimziContextReader{
		owner: func(ref resourceid.Ref) ownership.OwnerAnswer {
			answer := view.Owner(ref)
			if answer.Resolution.State == ownership.OwnerFound {
				owner := answer.Resolution.Owner
				observed[owner.Ref] = owner
			}
			return answer
		},
		list: func(namespace string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, readreason.Code) {
			gvr, ok := discovery.GetGVRWithGroup("KafkaConnector", strimziGroup)
			if !ok {
				return nil, gvr, false, readreason.KindUnknown
			}
			plan := readplan.Dynamic(dynamic, gvr, []string{namespace}, false)
			for _, part := range plan.Partitions {
				if part.Verdict.State != readplan.Readable {
					return nil, gvr, false, part.Verdict.Code
				}
			}
			rows, truncated, err := dynamic.ListWatchedNamespaceReadOnly(gvr, namespace, maxStrimziContextScan)
			if err != nil {
				code, known := readplan.CollectorErrorCode(err)
				if !known {
					code = readreason.ReadError
				}
				return nil, gvr, false, code
			}
			return rows, gvr, truncated, ""
		},
	}
	out := strimziEvidence(runtimeObj, canRead, reader)
	for ref, earlier := range observed {
		current := session.Object(ref)
		if current.State != ownership.Present || current.Object.UID != earlier.UID {
			return nil
		}
	}
	if session.ValidateCaches() != nil || dynamic != k8s.GetDynamicResourceCache() || discovery != k8s.GetResourceDiscovery() || dynamic.IsStopped() {
		return nil
	}
	return out
}

func strimziEvidence(obj runtime.Object, canRead func(Ref) bool, reader strimziContextReader) *issuesapi.RelatedApplicationFindings {
	current, ok := k8s.OwnershipObservation(obj)
	if !ok || current.UID == "" {
		return nil
	}
	var connect Ref
	for depth := 0; depth < 6; depth++ {
		ref := current.Ref
		if ref.Group == strimziGroup && ref.Kind == "KafkaConnect" {
			connect = Ref{Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
			break
		}
		if ref.Group == strimziGroup && ref.Kind == "KafkaConnector" {
			return nil
		}
		answer := reader.owner(ref)
		if !answer.Subject.Present() || answer.Subject.Read.Object.UID != current.UID || answer.Resolution.State != ownership.OwnerFound {
			return nil
		}
		current = answer.Resolution.Owner
	}
	if connect.Name == "" || canRead != nil && !canRead(connect) {
		return nil
	}
	out := &issuesapi.RelatedApplicationFindings{Gaps: []resourceapi.Gap{}, Source: connect, Coverage: "Connector status could not be read from the cache; missing findings do not establish application health."}
	rows, gvr, truncated, code := reader.list(connect.Namespace)
	if truncated || len(rows) > maxStrimziContextScan {
		out.Truncated = true
		code = readreason.ListTooLarge
		out.Coverage = "Connector findings were not checked because this namespace exceeds the inspection budget; missing findings do not establish application health."
	}
	if code != "" {
		out.Gaps = []resourceapi.Gap{{
			Code:       code,
			Kinds:      resourceapi.Kinds([]resourceapi.KindRef{{Group: strimziGroup, Kind: "KafkaConnector"}}, 1),
			Namespaces: resourceapi.Names([]string{connect.Namespace}, 1),
		}}
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
		findings := StrimziConnectorIssues(gvr, row)
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
	out.Coverage = fmt.Sprintf("Checked %d authorized cached connector snapshots; %d were unverifiable or inactive. Missing findings do not establish application health.", checked, unknown)
	if out.Truncated {
		out.Coverage += " The five-finding limit was reached; additional findings may be omitted."
	}
	return out
}
