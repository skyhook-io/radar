package configrefs

import (
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// PathRef is an object a field names, and the field.
type PathRef struct {
	Ref  resourceid.Ref
	Path string
	// Or are the same reference in older API groups, in the order a
	// controller may try them after Ref; nil means Ref alone. They are
	// candidates, not groups every controller version reads: Traefik 2.10
	// and 2.11 read both of Traefik's groups, Traefik 3 only the first. See
	// relations.Declaration.Or for how a family resolves them.
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
