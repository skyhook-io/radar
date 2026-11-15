package configrefs

import (
	"slices"
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// PathRef is an object a field names, and the field.
type PathRef struct {
	Ref  resourceid.Ref
	Path string
	// Or are the same reference in other API groups the controller also
	// resolves it from, as Traefik 2 reads both of its groups.
	Or []resourceid.Ref
}

// targetGroup is the API group a reference names: from its apiVersion, or,
// for a reference that omits it, the group a built-in kind belongs to.
func targetGroup(apiVersion, kind string) string {
	if apiVersion != "" {
		return resourceid.GroupFromAPIVersion(apiVersion)
	}
	if b, ok := resourceid.BuiltinForKind(kind); ok {
		return b.Group
	}
	return ""
}

// HPAScaleTarget returns the workload a HorizontalPodAutoscaler scales, in
// its own namespace.
func HPAScaleTarget(hpa *autoscalingv2.HorizontalPodAutoscaler) (PathRef, bool) {
	t := hpa.Spec.ScaleTargetRef
	if t.Name == "" || t.Kind == "" {
		return PathRef{}, false
	}
	return PathRef{Ref: resourceid.Ref{Group: targetGroup(t.APIVersion, t.Kind), Kind: t.Kind, Namespace: hpa.Namespace, Name: t.Name}, Path: "spec.scaleTargetRef"}, true
}

// VPATarget returns the workload a VerticalPodAutoscaler sizes, in its own
// namespace.
func VPATarget(vpa *unstructured.Unstructured) (PathRef, bool) {
	name, _, _ := unstructured.NestedString(vpa.Object, "spec", "targetRef", "name")
	kind, _, _ := unstructured.NestedString(vpa.Object, "spec", "targetRef", "kind")
	if name == "" || kind == "" {
		return PathRef{}, false
	}
	apiVersion, _, _ := unstructured.NestedString(vpa.Object, "spec", "targetRef", "apiVersion")
	return PathRef{Ref: resourceid.Ref{Group: targetGroup(apiVersion, kind), Kind: kind, Namespace: vpa.GetNamespace(), Name: name}, Path: "spec.targetRef"}, true
}

// KEDAScaleTargetRef is KEDAScaleTarget as a reference in the ScaledObject's
// namespace.
func KEDAScaleTargetRef(so *unstructured.Unstructured) (PathRef, bool) {
	apiVersion, kind, name, ok := KEDAScaleTarget(so)
	if !ok {
		return PathRef{}, false
	}
	return PathRef{Ref: resourceid.Ref{Group: resourceid.GroupFromAPIVersion(apiVersion), Kind: kind, Namespace: so.GetNamespace(), Name: name}, Path: "spec.scaleTargetRef"}, true
}

// RolloutServices returns the Services an Argo Rollout's strategy names:
// canary and stable for a canary rollout, active and preview for blue-green.
func RolloutServices(rollout *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	for _, field := range [][]string{
		{"spec", "strategy", "canary", "canaryService"},
		{"spec", "strategy", "canary", "stableService"},
		{"spec", "strategy", "blueGreen", "activeService"},
		{"spec", "strategy", "blueGreen", "previewService"},
	} {
		if name, _, _ := unstructured.NestedString(rollout.Object, field...); name != "" {
			refs = append(refs, PathRef{Ref: resourceid.Ref{Kind: "Service", Namespace: rollout.GetNamespace(), Name: name}, Path: strings.Join(field, ".")})
		}
	}
	return refs
}

// CertificateSecret returns the Secret a cert-manager Certificate writes.
func CertificateSecret(cert *unstructured.Unstructured) (PathRef, bool) {
	name, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	if name == "" {
		return PathRef{}, false
	}
	return PathRef{Ref: resourceid.Ref{Kind: "Secret", Namespace: cert.GetNamespace(), Name: name}, Path: "spec.secretName"}, true
}

// RolloutAnalysisRunStatus is where an Argo Rollout's status records an
// AnalysisRun it is running, and what triggered it.
type RolloutAnalysisRunStatus struct {
	Trigger string
	Path    []string
}

// RolloutAnalysisRunStatuses are the AnalysisRuns a Rollout's status points
// at: only the current ones, never its history.
var RolloutAnalysisRunStatuses = []RolloutAnalysisRunStatus{
	{"step", []string{"canary", "currentStepAnalysisRunStatus"}},
	{"background", []string{"canary", "currentBackgroundAnalysisRunStatus"}},
	{"pre-promotion", []string{"blueGreen", "prePromotionAnalysisRunStatus"}},
	{"post-promotion", []string{"blueGreen", "postPromotionAnalysisRunStatus"}},
}

// RolloutAnalysisRuns returns the AnalysisRuns a Rollout's status points at.
func RolloutAnalysisRuns(rollout *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	for _, s := range RolloutAnalysisRunStatuses {
		if name, _, _ := unstructured.NestedString(rollout.Object, slices.Concat([]string{"status"}, s.Path, []string{"name"})...); name != "" {
			refs = append(refs, PathRef{Ref: resourceid.NewRef(ArgoGroup, "AnalysisRun", rollout.GetNamespace(), name), Path: "status." + strings.Join(s.Path, ".")})
		}
	}
	return refs
}
