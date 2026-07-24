package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

const maxYAMLPreviewRequestBytes = 6 << 20
const maxYAMLPreviewDocuments = 100

var secretKindDeclaration = regexp.MustCompile(`(?im)(?:^|[,{])[ \t]*["']?kind["']?[ \t]*:(?:[ \t]*["']?secret["']?(?:[ \t\r]*(?:[,}#]|$))|[ \t\r]*\n[ \t]+["']?secret["']?(?:[ \t\r]*(?:#|$)))`)

type yamlPreviewTarget struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type yamlPreviewRequest struct {
	YAML   string             `json:"yaml"`
	Mode   string             `json:"mode"`
	Force  bool               `json:"force"`
	Target *yamlPreviewTarget `json:"target,omitempty"`
}

type yamlPreviewDocument struct {
	Index                   int      `json:"index"`
	Status                  string   `json:"status"`
	APIVersion              string   `json:"apiVersion,omitempty"`
	Kind                    string   `json:"kind,omitempty"`
	Namespace               string   `json:"namespace,omitempty"`
	Name                    string   `json:"name,omitempty"`
	Action                  string   `json:"action,omitempty"`
	SubmittedYAML           string   `json:"submittedYaml"`
	BaselineYAML            string   `json:"baselineYaml,omitempty"`
	PredictedYAML           string   `json:"predictedYaml,omitempty"`
	Warnings                []string `json:"warnings,omitempty"`
	Error                   string   `json:"error,omitempty"`
	ReviewedResourceVersion string   `json:"reviewedResourceVersion,omitempty"`
	Redacted                bool     `json:"redacted,omitempty"`
	parsed                  bool
}

type yamlPreviewResponse struct {
	Documents []yamlPreviewDocument `json:"documents"`
	NonAtomic bool                  `json:"nonAtomic"`
	Context   string                `json:"context"`
}

func (s *Server) handlePreviewResources(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	var req yamlPreviewRequest
	if err := decodeBoundedJSONBody(w, r, maxYAMLPreviewRequestBytes, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid preview request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.YAML) == "" {
		s.writeError(w, http.StatusBadRequest, "yaml is required")
		return
	}
	if req.Mode == "" {
		req.Mode = "apply"
	}
	if req.Mode != "apply" && req.Mode != "create" && req.Mode != "update" {
		s.writeError(w, http.StatusBadRequest, "mode must be 'apply', 'create', or 'update'")
		return
	}
	if req.Mode == "update" && (req.Target == nil || req.Target.Kind == "" || req.Target.Name == "") {
		s.writeError(w, http.StatusBadRequest, "update preview requires target kind and name")
		return
	}

	docs := k8s.SplitYAMLDocuments(req.YAML)
	if len(docs) == 0 {
		s.writeError(w, http.StatusBadRequest, "no valid YAML documents found")
		return
	}
	if len(docs) > maxYAMLPreviewDocuments {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("preview supports at most %d YAML documents", maxYAMLPreviewDocuments))
		return
	}
	if req.Mode == "update" && len(docs) != 1 {
		s.writeError(w, http.StatusBadRequest, "update preview accepts exactly one YAML document")
		return
	}

	client, contextName := s.getDynamicClientSnapshotForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}

	response := yamlPreviewResponse{
		Documents: make([]yamlPreviewDocument, 0, len(docs)),
		NonAtomic: len(docs) > 1,
		Context:   contextName,
	}
	for index, doc := range docs {
		identity := previewDocumentIdentity(index, doc)
		if identity.Kind != "" || identity.Name != "" {
			auth.AuditLog(r, identity.Namespace, identity.Name)
		}
		if req.Mode == "update" {
			result, err := k8s.PreviewUpdateResourceWithClient(r.Context(), k8s.UpdateResourceOptions{
				Kind:      req.Target.Kind,
				Namespace: req.Target.Namespace,
				Name:      req.Target.Name,
				YAML:      doc,
				Force:     req.Force,
			}, client)
			if err != nil {
				response.Documents = append(response.Documents, rejectedPreviewDocument(identity, doc, err, req.Mode))
				continue
			}
			predictedYAML, baselineYAML, redacted, err := normalizedPreviewYAML(result.Object, result.Live, result.Submitted, true)
			if err != nil {
				response.Documents = append(response.Documents, rejectedPreviewDocument(identity, doc, err, req.Mode))
				continue
			}
			identity.Status = "accepted"
			identity.Action = "update"
			identity.BaselineYAML = baselineYAML
			identity.PredictedYAML = predictedYAML
			identity.Warnings = result.Warnings
			identity.ReviewedResourceVersion = result.ResourceVersion
			identity.Redacted = redacted
			response.Documents = append(response.Documents, identity)
			continue
		}

		result, err := k8s.ApplyResourceWithClient(r.Context(), k8s.ApplyResourceOptions{
			YAML:               doc,
			Mode:               req.Mode,
			DryRun:             true,
			Force:              req.Force,
			UseCreateForAbsent: req.Mode == "apply",
		}, client)
		if err != nil {
			if result != nil {
				identity.Action = result.Action
				if result.Previous != nil {
					identity.ReviewedResourceVersion = result.Previous.GetResourceVersion()
				}
			}
			response.Documents = append(response.Documents, rejectedPreviewDocument(identity, doc, err, req.Mode))
			continue
		}
		comparison := result.Previous
		includeBaseline := comparison != nil
		if comparison == nil {
			comparison = result.Submitted
		}
		predictedYAML, baselineYAML, redacted, err := normalizedPreviewYAML(result.Object, comparison, result.Submitted, includeBaseline)
		if err != nil {
			response.Documents = append(response.Documents, rejectedPreviewDocument(identity, doc, err, req.Mode))
			continue
		}
		identity.Status = "accepted"
		identity.Action = result.Action
		identity.BaselineYAML = baselineYAML
		identity.PredictedYAML = predictedYAML
		identity.Warnings = result.Warnings
		if result.Previous != nil {
			identity.ReviewedResourceVersion = result.Previous.GetResourceVersion()
		}
		identity.Redacted = redacted
		requireAcknowledgementWithoutLiveState(&identity)
		response.Documents = append(response.Documents, identity)
	}

	s.writeJSON(w, response)
}

func requireAcknowledgementWithoutLiveState(doc *yamlPreviewDocument) {
	if doc.Action != "unknown" {
		return
	}
	doc.Status = "unavailable"
	doc.Error = "Kubernetes accepted the server-side dry-run, but Radar could not read the live resource. Applying it will not be protected against changes after review."
}

func decodeBoundedJSONBody(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain a single JSON object")
		}
		return err
	}
	return nil
}

func previewDocumentIdentity(index int, content string) yamlPreviewDocument {
	doc := yamlPreviewDocument{Index: index}
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(content), &obj.Object); err == nil {
		doc.parsed = true
		doc.APIVersion = obj.GetAPIVersion()
		doc.Kind = obj.GetKind()
		doc.Namespace = obj.GetNamespace()
		doc.Name = obj.GetName()
		if strings.EqualFold(doc.Kind, "Secret") {
			doc.Redacted = true
			redactSecretManifestWithMarkers(obj.Object, map[string]any{}, "<redacted:unchanged>", "<redacted:unchanged>")
			if redacted, marshalErr := yaml.Marshal(obj.Object); marshalErr == nil {
				doc.SubmittedYAML = string(redacted)
			} else {
				doc.SubmittedYAML = "# Secret YAML could not be rendered safely for review.\n"
			}
			return doc
		}
	}
	if !doc.parsed && manifestDeclaresSecret(content) {
		doc.Redacted = true
		doc.SubmittedYAML = "# Secret YAML could not be rendered safely for review.\n"
	} else {
		doc.SubmittedYAML = content
	}
	return doc
}

func rejectedPreviewDocument(doc yamlPreviewDocument, content string, err error, mode string) yamlPreviewDocument {
	doc.Status = previewErrorStatus(err, mode)
	doc.Error = secretSafePreviewError(doc, content, err, doc.Status)
	return doc
}

func secretSafePreviewError(doc yamlPreviewDocument, content string, err error, status string) string {
	if !strings.EqualFold(doc.Kind, "Secret") && (doc.parsed || !manifestDeclaresSecret(content)) {
		return err.Error()
	}
	if apierrors.IsForbidden(err) {
		return "You are not authorized to preview this Secret. Sensitive details were hidden."
	}
	if status == "unavailable" {
		return "Kubernetes cannot server-dry-run this Secret in its current API or dependency state. Sensitive details were hidden."
	}
	if apierrors.IsConflict(err) {
		return "Kubernetes reported an ownership or version conflict while previewing this Secret. Sensitive details were hidden."
	}
	return "Kubernetes rejected this Secret. Error details were hidden to protect Secret values."
}

func manifestDeclaresSecret(content string) bool {
	return secretKindDeclaration.MatchString(content)
}

func previewErrorStatus(err error, mode string) string {
	message := strings.ToLower(err.Error())
	if apierrors.IsNotFound(err) {
		if mode == "update" {
			return "rejected"
		}
		return "unavailable"
	}
	if strings.Contains(message, "unknown resource kind") ||
		strings.Contains(message, "does not support dry run") ||
		strings.Contains(message, "dry run is not supported") ||
		strings.Contains(message, "sideeffects") {
		return "unavailable"
	}
	return "rejected"
}

func normalizedPreviewYAML(obj, comparison, submitted *unstructured.Unstructured, includeComparison bool) (string, string, bool, error) {
	if obj == nil {
		return "", "", false, fmt.Errorf("cluster returned an empty preview")
	}
	keepStatus := false
	if submitted != nil {
		_, keepStatus, _ = unstructured.NestedFieldNoCopy(submitted.Object, "status")
	}
	cleaned := normalizePreviewObject(obj, keepStatus)
	var comparisonCleaned *unstructured.Unstructured
	if comparison != nil {
		comparisonCleaned = normalizePreviewObject(comparison, keepStatus)
	}
	redacted := strings.EqualFold(cleaned.GetKind(), "Secret")
	if redacted {
		comparisonObject := map[string]any{}
		if comparisonCleaned != nil {
			comparisonObject = comparisonCleaned.Object
		}
		redactSecretManifestWithMarkers(cleaned.Object, comparisonObject, "<redacted:after>", "<redacted:before>")
	}
	content, err := yaml.Marshal(cleaned.Object)
	if err != nil {
		return "", "", redacted, fmt.Errorf("failed to render cluster preview: %w", err)
	}
	if !includeComparison || comparisonCleaned == nil {
		return string(content), "", redacted, nil
	}
	baseline, err := yaml.Marshal(comparisonCleaned.Object)
	if err != nil {
		return "", "", redacted, fmt.Errorf("failed to render preview baseline: %w", err)
	}
	return string(content), string(baseline), redacted, nil
}

func normalizePreviewObject(obj *unstructured.Unstructured, keepStatus bool) *unstructured.Unstructured {
	cleaned := obj.DeepCopy()
	if !keepStatus {
		unstructured.RemoveNestedField(cleaned.Object, "status")
	}
	for _, field := range []string{
		"managedFields",
		"resourceVersion",
		"uid",
		"creationTimestamp",
		"generation",
		"selfLink",
	} {
		unstructured.RemoveNestedField(cleaned.Object, "metadata", field)
	}
	return cleaned
}
