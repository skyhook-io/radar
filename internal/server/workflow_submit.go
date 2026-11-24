package server

import (
	"maps"
	"net/http"
	"regexp"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

const workflowSubmissionAnnotation = "skyhook.io/reviewed-run"
const workflowAPIVersion = "argoproj.io/v1alpha1"

var workflowTemplateResource = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflowtemplates"}
var workflowSubmissionDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var workflowSubmissionParameter = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func (s *Server) handleWorkflowSubmitOptions(w http.ResponseWriter, r *http.Request) {
	s.readWorkflowSubmissionObject(w, r, workflowTemplateResource)
}

func (s *Server) handleWorkflowSubmission(w http.ResponseWriter, r *http.Request) {
	s.readWorkflowSubmissionObject(w, r, argoWorkflowGVR)
}

func (s *Server) readWorkflowSubmissionObject(w http.ResponseWriter, r *http.Request, resource schema.GroupVersionResource) {
	w.Header().Set("Cache-Control", "no-store")
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
		s.writeError(w, http.StatusBadRequest, "a valid Workflow namespace and name are required")
		return
	}
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeErrorCode(w, http.StatusServiceUnavailable, "cluster_unavailable", "the current cluster connection is unavailable")
		return
	}
	object, err := client.Resource(resource).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		s.writeWorkflowSubmitError(w, err)
		return
	}
	s.writeJSON(w, map[string]any{"resource": object})
}

func (s *Server) handleWorkflowSubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	namespace, template := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	var review struct {
		TemplateUID             string            `json:"templateUid"`
		TemplateResourceVersion string            `json:"templateResourceVersion"`
		WorkflowName            string            `json:"workflowName"`
		RequestDigest           string            `json:"requestDigest"`
		Parameters              map[string]string `json:"parameters"`
	}
	if decodeBoundedJSONBody(w, r, 512<<10, &review) != nil || review.TemplateUID == "" || review.TemplateResourceVersion == "" ||
		len(review.TemplateUID) > 253 || len(review.TemplateResourceVersion) > 253 || !workflowSubmissionDigest.MatchString(review.RequestDigest) ||
		len(review.Parameters) > 50 || len(validation.IsDNS1123Label(namespace)) != 0 ||
		len(validation.IsDNS1123Subdomain(template)) != 0 || len(validation.IsDNS1123Subdomain(review.WorkflowName)) != 0 {
		s.writeErrorCode(w, http.StatusBadRequest, "invalid_submission", "provide the reviewed WorkflowTemplate, Workflow name and request digest")
		return
	}
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeErrorCode(w, http.StatusServiceUnavailable, "cluster_unavailable", "the current cluster connection is unavailable")
		return
	}
	ctx := r.Context()
	templates := client.Resource(workflowTemplateResource).Namespace(namespace)
	current := func() bool {
		object, err := templates.Get(ctx, template, metav1.GetOptions{})
		if err != nil {
			s.writeWorkflowSubmitError(w, err)
			return false
		}
		if object.GetAPIVersion() != workflowAPIVersion || object.GetKind() != "WorkflowTemplate" ||
			object.GetNamespace() != namespace || object.GetName() != template ||
			string(object.GetUID()) != review.TemplateUID || object.GetResourceVersion() != review.TemplateResourceVersion {
			s.writeErrorCode(w, http.StatusConflict, "template_changed", "the WorkflowTemplate changed after review")
			return false
		}
		return true
	}
	parameters := make([]any, 0, len(review.Parameters))
	for _, name := range slices.Sorted(maps.Keys(review.Parameters)) {
		value := review.Parameters[name]
		if !workflowSubmissionParameter.MatchString(name) || len(value) > 8192 {
			s.writeErrorCode(w, http.StatusBadRequest, "invalid_submission", "a Workflow parameter is invalid")
			return
		}
		parameters = append(parameters, map[string]any{"name": name, "value": value})
	}
	workflow := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": workflowAPIVersion, "kind": "Workflow",
		"metadata": map[string]any{
			"name": review.WorkflowName, "namespace": namespace,
			"annotations": map[string]any{workflowSubmissionAnnotation: review.RequestDigest},
		},
		"spec": map[string]any{
			"workflowTemplateRef": map[string]any{"name": template},
			"arguments":           map[string]any{"parameters": parameters},
		},
	}}
	workflows := client.Resource(argoWorkflowGVR).Namespace(namespace)
	if !current() {
		return
	}
	if _, err := workflows.Create(ctx, workflow, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
		s.writeWorkflowSubmitError(w, err)
		return
	}
	// Argo references the template by name. Read again after admission
	// validation so a revision observed moving during the review cannot run.
	if !current() {
		return
	}
	auth.AuditLog(r, namespace, review.WorkflowName)
	created, err := workflows.Create(ctx, workflow, metav1.CreateOptions{})
	if err != nil {
		s.writeWorkflowSubmitError(w, err)
		return
	}
	if created.GetUID() == "" || created.GetName() != review.WorkflowName || created.GetNamespace() != namespace ||
		created.GetAnnotations()[workflowSubmissionAnnotation] != review.RequestDigest {
		s.writeErrorCode(w, http.StatusBadGateway, "run_unconfirmed", "Kubernetes returned an unexpected Workflow")
		return
	}
	s.writeJSON(w, map[string]string{"name": created.GetName(), "namespace": created.GetNamespace(), "uid": string(created.GetUID())})
}

func (s *Server) writeWorkflowSubmitError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		s.writeErrorCode(w, http.StatusNotFound, "workflow_resource_not_found", "the requested Workflow resource was not found")
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
		s.writeErrorCode(w, http.StatusForbidden, "cluster_access_denied", "Kubernetes denied access to this Workflow resource")
	case apierrors.IsAlreadyExists(err):
		s.writeErrorCode(w, http.StatusConflict, "workflow_exists", "a Workflow with this name already exists")
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		s.writeErrorCode(w, http.StatusUnprocessableEntity, "run_rejected", "Kubernetes rejected the Workflow")
	default:
		s.writeErrorCode(w, http.StatusBadGateway, "run_unconfirmed", "could not confirm the result from Kubernetes")
	}
}
