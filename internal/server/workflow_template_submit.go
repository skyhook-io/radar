package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/skyhook-io/radar/internal/auth"
)

var argoWorkflowTemplateGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "workflowtemplates"}

const (
	// The same annotation Velero "Back up now" uses for one confirmation.
	workflowSubmissionAnnotation = "radar.skyhook.io/submission"
	// Argo's own labels: a controller started with --instanceid only picks up
	// Workflows carrying its instance ID, and Argo's CLI and UI label a run
	// with the template it came from.
	argoControllerInstanceLabel = "workflows.argoproj.io/controller-instanceid"
	argoWorkflowTemplateLabel   = "workflows.argoproj.io/workflow-template"
	// Argo refuses a Workflow name longer than 63 characters: the name is
	// copied into label values on the run's Pods.
	maxArgoWorkflowName = 63
	// The API server keeps at most 58 characters of a generateName.
	maxGenerateNamePrefix = 58
)

// WorkflowTemplateSubmitRequest describes the Workflow to start from a
// WorkflowTemplate. Every field is optional; an empty body starts a run with
// the template's defaults and a generated name.
type WorkflowTemplateSubmitRequest struct {
	// Parameters sets values for the parameters the template declares: its
	// spec.arguments.parameters, and the inputs of its entrypoint template,
	// which Argo fills from a Workflow's arguments by name. A parameter with
	// no value of its own must be set here; a value must be one of the
	// parameter's enum when it has one; a name the template does not declare
	// is refused, and so is a null value.
	Parameters map[string]*string `json:"parameters,omitempty"`
	// Name names the Workflow. At most one of Name and GenerateName.
	Name string `json:"name,omitempty"`
	// GenerateName is the prefix the API server completes. Defaults to
	// "<template>-", as `argo submit --from` does.
	GenerateName string `json:"generateName,omitempty"`
	// Submission identifies one confirmation: 8–64 letters, digits or
	// hyphens, stored on the Workflow as the radar.skyhook.io/submission
	// annotation. It needs Name: a request whose answer was lost can be
	// repeated unchanged, with the same name and submission, and returns the
	// Workflow it created while that Workflow still exists. Once Argo or
	// anyone deletes it, the repeat creates a new run. Another Workflow
	// holding the name is a 409.
	Submission string `json:"submission,omitempty"`
	// ResourceVersion of the WorkflowTemplate the caller reviewed. A template
	// that changed since is refused with 409. This covers only Radar's read:
	// the Workflow references the template by name, and Argo reads the
	// template again when it starts the run.
	ResourceVersion string `json:"resourceVersion,omitempty"`
}

// WorkflowTemplateSubmitResponse names the Workflow created, or the one an
// earlier identical request created.
type WorkflowTemplateSubmitResponse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// handleWorkflowTemplateSubmit creates a Workflow that runs a WorkflowTemplate
// through spec.workflowTemplateRef, as `argo submit --from workflowtemplate/<name>`.
//
//	POST /api/workflowtemplates/{namespace}/{name}/submit
//	{"parameters": {"message": "hi"}, "name": "...", "submission": "...", "resourceVersion": "..."}
//
// The template read and the create both run as the caller, so Kubernetes
// decides: `get workflowtemplates` and `create workflows` in the namespace.
// Argo runs it as the template's service account, not the caller's.
func (s *Server) handleWorkflowTemplateSubmit(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if outsideNamespacePin(namespace) != nil {
		s.writeError(w, http.StatusForbidden, "namespace is outside Radar's namespace scope")
		return
	}
	// A pointer so a JSON null, which decodes without error, can be refused.
	req := &WorkflowTemplateSubmitRequest{}
	if err := decodeBoundedJSONBody(w, r, 256<<10, &req); err != nil && !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req == nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body: the body must be a JSON object")
		return
	}
	if msg := req.problem(); msg != "" {
		s.writeError(w, http.StatusBadRequest, msg)
		return
	}

	auth.AuditLog(r, namespace, name)
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	template, err := client.Resource(argoWorkflowTemplateGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		s.writeWorkflowSubmitError(w, err, namespace, name, "read")
		return
	}
	workflows := client.Resource(argoWorkflowGVR).Namespace(namespace)
	// The same request sent again after its answer was lost: the run it asked
	// for exists, so return it, even if the template has changed since.
	if req.Name != "" && req.Submission != "" {
		if existing, getErr := workflows.Get(r.Context(), req.Name, metav1.GetOptions{}); getErr == nil {
			s.answerExistingWorkflow(w, existing, name, req)
			return
		}
	}
	if req.ResourceVersion != "" && req.ResourceVersion != template.GetResourceVersion() {
		s.writeErrorCode(w, http.StatusConflict, "workflow_template_changed", "the workflow template changed since you opened it; review it again before submitting")
		return
	}
	if entrypoint, _, _ := unstructured.NestedString(template.Object, "spec", "entrypoint"); entrypoint == "" {
		s.writeErrorCode(w, http.StatusBadRequest, "workflow_template_no_entrypoint", "the workflow template has no entrypoint, so a Workflow that references it cannot start")
		return
	}
	arguments, problems := workflowArguments(template, req.Parameters)
	if len(problems) > 0 {
		s.writeErrorCode(w, http.StatusBadRequest, "invalid_parameters", strings.Join(problems, "; "))
		return
	}

	created, err := workflows.Create(r.Context(), workflowFromTemplate(template, req, arguments), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) && req.Name != "" {
		existing, getErr := workflows.Get(r.Context(), req.Name, metav1.GetOptions{})
		if getErr != nil {
			// Possibly this request's own run, created before its answer was
			// lost: never advise another name for a Workflow Radar can't see.
			s.writeErrorCode(w, http.StatusConflict, "workflow_unverified", "a workflow with this name already exists, and Radar could not read it to tell whether this request created it; check the template's runs before submitting again")
			return
		}
		s.answerExistingWorkflow(w, existing, name, req)
		return
	}
	if err != nil {
		s.writeWorkflowSubmitError(w, err, namespace, name, "create")
		return
	}
	s.writeJSON(w, WorkflowTemplateSubmitResponse{Namespace: created.GetNamespace(), Name: created.GetName(), UID: string(created.GetUID())})
}

// answerExistingWorkflow answers a request whose name is taken: with that
// Workflow when this request created it, otherwise 409.
func (s *Server) answerExistingWorkflow(w http.ResponseWriter, existing *unstructured.Unstructured, template string, req *WorkflowTemplateSubmitRequest) {
	ref, _, _ := unstructured.NestedString(existing.Object, "spec", "workflowTemplateRef", "name")
	clusterScope, _, _ := unstructured.NestedBool(existing.Object, "spec", "workflowTemplateRef", "clusterScope")
	if req.Submission == "" || existing.GetAnnotations()[workflowSubmissionAnnotation] != req.Submission || ref != template || clusterScope {
		s.writeErrorCode(w, http.StatusConflict, "workflow_name_taken", "a workflow with this name already exists; choose another name")
		return
	}
	if !sameArguments(existing, req.Parameters) {
		s.writeErrorCode(w, http.StatusConflict, "workflow_submission_reused", "this submission already created a workflow with different parameters; a changed request needs a new submission")
		return
	}
	s.writeJSON(w, WorkflowTemplateSubmitResponse{Namespace: existing.GetNamespace(), Name: existing.GetName(), UID: string(existing.GetUID())})
}

// sameArguments reports whether a Workflow's arguments are the values a
// request asks for: what workflowFromTemplate would have written for it.
func sameArguments(existing *unstructured.Unstructured, values map[string]*string) bool {
	params, _, _ := unstructured.NestedSlice(existing.Object, "spec", "arguments", "parameters")
	if len(params) != len(values) {
		return false
	}
	for _, item := range params {
		param, _ := item.(map[string]any)
		paramName, _ := param["name"].(string)
		value, _ := param["value"].(string)
		want, ok := values[paramName]
		if !ok || want == nil || *want != value {
			return false
		}
	}
	return true
}

func (req *WorkflowTemplateSubmitRequest) problem() string {
	switch {
	case req.Name != "" && req.GenerateName != "":
		return "set name or generateName, not both"
	case req.Submission != "" && !submissionToken.MatchString(req.Submission):
		return "submission must be 8–64 letters, digits or hyphens"
	case req.Submission != "" && req.Name == "":
		return "submission needs a name: only a named workflow can be found again when the request is repeated"
	case req.Name != "" && (len(req.Name) > maxArgoWorkflowName || len(validation.IsDNS1123Subdomain(req.Name)) > 0):
		return fmt.Sprintf("name must be at most %d lowercase letters, digits, '-' or '.', starting and ending with a letter or digit", maxArgoWorkflowName)
	case req.GenerateName != "" && (len(req.GenerateName) > maxGenerateNamePrefix || len(validation.IsDNS1123Subdomain(req.GenerateName+"x")) > 0):
		return fmt.Sprintf("generateName must be at most %d lowercase letters, digits, '-' or '.', starting with a letter or digit", maxGenerateNamePrefix)
	}
	return ""
}

// declaredParameter is one parameter a submission may set.
type declaredParameter struct {
	name string
	// satisfied: the run has a value without one from the request.
	satisfied bool
	enum      []any
}

// declaredParameters lists what a Workflow referencing the template can be
// given: its spec.arguments.parameters, then the entrypoint template's inputs
// those don't name. Argo passes a Workflow's arguments to the entrypoint's
// inputs by name, so `argo submit --from` accepts either.
func declaredParameters(template *unstructured.Unstructured) []declaredParameter {
	var out []declaredParameter
	seen := map[string]bool{}
	add := func(items []any, defaultKeys ...string) {
		for _, item := range items {
			param, ok := item.(map[string]any)
			if !ok {
				continue
			}
			paramName, _ := param["name"].(string)
			if paramName == "" || seen[paramName] {
				continue
			}
			seen[paramName] = true
			satisfied := false
			for _, key := range defaultKeys {
				if _, ok := param[key]; ok {
					satisfied = true
				}
			}
			// Argo resolves a ConfigMap reference for a submitted run; other
			// sources (supplied, event) have nothing to read from.
			if _, ok, _ := unstructured.NestedMap(param, "valueFrom", "configMapKeyRef"); ok {
				satisfied = true
			}
			enum, _ := param["enum"].([]any)
			out = append(out, declaredParameter{name: paramName, satisfied: satisfied, enum: enum})
		}
	}
	arguments, _, _ := unstructured.NestedSlice(template.Object, "spec", "arguments", "parameters")
	add(arguments, "value")
	entrypoint, _, _ := unstructured.NestedString(template.Object, "spec", "entrypoint")
	templates, _, _ := unstructured.NestedSlice(template.Object, "spec", "templates")
	for _, item := range templates {
		t, _ := item.(map[string]any)
		if t == nil || t["name"] != entrypoint || entrypoint == "" {
			continue
		}
		inputs, _, _ := unstructured.NestedSlice(t, "inputs", "parameters")
		add(inputs, "value", "default")
		break
	}
	return out
}

// workflowArguments checks the requested values against the parameters the
// template declares, with Argo's rules for a submitted Workflow: a parameter
// with no value of its own needs one, and a value must be one of the
// parameter's enum when it has one. It returns the requested values in the
// template's order.
func workflowArguments(template *unstructured.Unstructured, values map[string]*string) ([]any, []string) {
	var arguments []any
	var problems []string
	known := map[string]bool{}
	for _, param := range declaredParameters(template) {
		known[param.name] = true
		value, set := values[param.name]
		if !set {
			if !param.satisfied {
				problems = append(problems, fmt.Sprintf("parameter %q is required", param.name))
			}
			continue
		}
		if value == nil {
			problems = append(problems, fmt.Sprintf("parameter %q is null; send a string", param.name))
			continue
		}
		if len(param.enum) > 0 {
			allowed := make([]string, 0, len(param.enum))
			for _, e := range param.enum {
				if s, ok := argoParameterString(e); ok {
					allowed = append(allowed, s)
				}
			}
			if !slices.Contains(allowed, *value) {
				problems = append(problems, fmt.Sprintf("parameter %q must be one of %s", param.name, strings.Join(allowed, ", ")))
				continue
			}
		}
		arguments = append(arguments, map[string]any{"name": param.name, "value": *value})
	}
	var unknown []string
	for paramName := range values {
		if !known[paramName] {
			unknown = append(unknown, paramName)
		}
	}
	slices.Sort(unknown)
	for _, paramName := range unknown {
		problems = append(problems, fmt.Sprintf("the template declares no parameter %q", paramName))
	}
	return arguments, problems
}

// argoParameterString reads a parameter value as Argo does: YAML numbers and
// booleans are strings to Argo.
func argoParameterString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	}
	return "", false
}

func workflowFromTemplate(template *unstructured.Unstructured, req *WorkflowTemplateSubmitRequest, arguments []any) *unstructured.Unstructured {
	metadata := map[string]any{"namespace": template.GetNamespace()}
	switch {
	case req.Name != "":
		metadata["name"] = req.Name
	case req.GenerateName != "":
		metadata["generateName"] = req.GenerateName
	default:
		metadata["generateName"] = template.GetName() + "-"
	}
	labels := map[string]any{}
	if len(validation.IsValidLabelValue(template.GetName())) == 0 {
		labels[argoWorkflowTemplateLabel] = template.GetName()
	}
	if instance := template.GetLabels()[argoControllerInstanceLabel]; instance != "" {
		labels[argoControllerInstanceLabel] = instance
	}
	metadata["labels"] = labels
	if req.Submission != "" {
		metadata["annotations"] = map[string]any{workflowSubmissionAnnotation: req.Submission}
	}
	spec := map[string]any{"workflowTemplateRef": map[string]any{"name": template.GetName()}}
	if len(arguments) > 0 {
		spec["arguments"] = map[string]any{"parameters": arguments}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Workflow",
		"metadata":   metadata,
		"spec":       spec,
	}}
}

func (s *Server) writeWorkflowSubmitError(w http.ResponseWriter, err error, namespace, name, step string) {
	switch {
	case apierrors.IsNotFound(err) && step == "read":
		s.writeError(w, http.StatusNotFound, "workflow template not found")
	case apierrors.IsUnauthorized(err):
		s.writeError(w, http.StatusForbidden, "Kubernetes rejected the credentials for this request")
	case apierrors.IsForbidden(err) && step == "read":
		s.writeError(w, http.StatusForbidden, "failed to read the workflow template: "+err.Error())
	case apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, "failed to create the workflow: "+err.Error())
	case apierrors.IsAlreadyExists(err):
		s.writeErrorCode(w, http.StatusConflict, "workflow_name_taken", "a workflow with this name already exists; choose another name")
	case apierrors.IsInvalid(err):
		s.writeError(w, http.StatusUnprocessableEntity, "Kubernetes rejected the workflow: "+err.Error())
	case apierrors.IsBadRequest(err):
		s.writeError(w, http.StatusBadRequest, "Kubernetes rejected the workflow: "+err.Error())
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		if step == "read" {
			s.writeError(w, http.StatusGatewayTimeout, "Kubernetes timed out reading the workflow template; nothing was submitted")
			return
		}
		s.writeError(w, http.StatusGatewayTimeout, "Kubernetes timed out; check the template's runs before submitting again")
	case step == "read":
		log.Printf("[workflows] Failed to read workflow template %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "could not read the workflow template")
	default:
		log.Printf("[workflows] Failed to submit workflow template %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusInternalServerError, "could not create the workflow")
	}
}
