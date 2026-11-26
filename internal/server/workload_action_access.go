package server

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// handleReviewedActionAccess only asks about the caller's own effective RBAC.
// The Kubernetes authorizer still checks the actual preview and mutation.
func (s *Server) handleReviewedActionAccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if !s.requireWorkloadActionUser(w, r) {
		return
	}
	ns, name, ok := s.workloadActionScope(w, r)
	if !ok {
		return
	}
	var in struct {
		Action string `json:"action"`
	}
	if !s.readWorkloadActionInput(w, r, &in) {
		return
	}
	kind := chi.URLParam(r, "kind")
	serviceRoute := kind == ""
	if serviceRoute {
		kind = "Service"
	}
	field, err := reviewedActionTarget(kind, in.Action)
	if err != nil || (field.kind == "Service" && !serviceRoute) {
		s.writeError(w, http.StatusBadRequest, "unsupported reviewed operation or target")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	access := reviewedActionAccess(ctx, s.getDynamicClientForRequest(r), in.Action, field.gvr, ns, name)
	s.writeJSON(w, map[string]string{"access": access})
}

func reviewedActionAccess(ctx context.Context, client dynamic.Interface, action string, target schema.GroupVersionResource, ns, name string) string {
	if client == nil {
		return "unknown"
	}
	// All reviewed handlers patch the named parent, including scale. Routing
	// preview and execution additionally list their evidence in this namespace.
	checks := []authv1.ResourceAttributes{
		{Group: target.Group, Version: target.Version, Resource: target.Resource, Namespace: ns, Name: name, Verb: "get"},
		{Group: target.Group, Version: target.Version, Resource: target.Resource, Namespace: ns, Name: name, Verb: "patch"},
	}
	switch action {
	case "setPodTemplateLabel":
		checks = append(checks, authv1.ResourceAttributes{Version: corev1.SchemeGroupVersion.Version, Resource: "services", Namespace: ns, Verb: "list"})
	case "setServiceSelectorLabel":
		checks = append(checks,
			authv1.ResourceAttributes{Version: corev1.SchemeGroupVersion.Version, Resource: "pods", Namespace: ns, Verb: "list"},
			authv1.ResourceAttributes{Group: discoveryv1.GroupName, Version: discoveryv1.SchemeGroupVersion.Version, Resource: "endpointslices", Namespace: ns, Verb: "list"},
		)
	}

	access := "allowed"
	reviews := client.Resource(authv1.SchemeGroupVersion.WithResource("selfsubjectaccessreviews"))
	for _, check := range checks {
		attributes, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&check)
		if err != nil {
			return "unknown"
		}
		// Decode allowed as an explicitly present boolean. The typed API's zero
		// value can't distinguish a malformed response from an actual denial.
		request := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": authv1.SchemeGroupVersion.String(), "kind": "SelfSubjectAccessReview",
			"spec": map[string]any{"resourceAttributes": attributes},
		}}
		result, err := reviews.Create(ctx, request, metav1.CreateOptions{})
		if err != nil || result == nil || result.GetAPIVersion() != authv1.SchemeGroupVersion.String() || result.GetKind() != "SelfSubjectAccessReview" {
			access = "unknown"
			continue
		}
		allowed, present, allowedErr := unstructured.NestedBool(result.Object, "status", "allowed")
		denied, _, deniedErr := unstructured.NestedBool(result.Object, "status", "denied")
		evaluationErr, _, statusErr := unstructured.NestedString(result.Object, "status", "evaluationError")
		if !present || allowedErr != nil || deniedErr != nil || statusErr != nil || evaluationErr != "" || (allowed && denied) {
			access = "unknown"
			continue
		}
		// With a complete result, no authorizer allowing the request is a denial.
		if !allowed {
			// Service routing lists only matching Pods and EndpointSlices. A
			// selector-aware authorizer may allow that narrower request even
			// when this namespace-wide list was denied. This preflight
			// does not read the live Service or its routing evidence.
			if action == "setServiceSelectorLabel" && check.Verb == "list" {
				access = "unknown"
				continue
			}
			return "denied"
		}
	}
	return access
}
