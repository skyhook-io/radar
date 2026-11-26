package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/rollouts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	kjson "sigs.k8s.io/json"
)

// An omitted version tolerates status updates. Explicit empty/null values must
// not accidentally downgrade a caller's exact-version review to that mode.
type optionalWorkloadVersion string

func (v *optionalWorkloadVersion) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil || strings.TrimSpace(value) == "" {
		return errors.New("expectedResourceVersion must be a nonempty string")
	}
	*v = optionalWorkloadVersion(value)
	return nil
}

type workloadActionInput struct {
	Action                  string                  `json:"action"`
	Replicas                *int32                  `json:"replicas,omitempty"`
	UID                     string                  `json:"uid,omitempty"`
	ReviewToken             string                  `json:"reviewToken,omitempty"`
	ExpectedResourceVersion optionalWorkloadVersion `json:"expectedResourceVersion,omitempty"`
	RestartAt               string                  `json:"restartAt,omitempty"`
}

type workloadActionView struct {
	Action          string `json:"action"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	ReviewToken     string `json:"reviewToken"`
	Field           string `json:"field"`
	Before          any    `json:"before"`
	After           any    `json:"after"`
}

// workloadActionField represents a closed set of actions. No resource, patch
// path, or arbitrary arguments are accepted from the client.
type workloadActionField struct {
	gvr   schema.GroupVersionResource
	kind  string
	path  []string
	after any
}

func resolveWorkloadAction(kind string, in workloadActionInput, execute bool, now time.Time) (workloadActionField, error) {
	var field workloadActionField
	b, ok := resourceid.BuiltinForName(kind)
	if ok {
		field.gvr = schema.GroupVersionResource{Group: b.Group, Version: b.Version, Resource: b.Resource}
		field.kind = b.Kind
	} else if k8score.NormalizeWorkloadKind(strings.ToLower(kind)) == "rollouts" {
		field.gvr = rollouts.GVR
		field.kind = "Rollout"
	}
	if execute {
		digest, err := hex.DecodeString(in.ReviewToken)
		if strings.TrimSpace(in.UID) == "" || err != nil || len(digest) != sha256.Size {
			return field, errors.New("execution requires the uid and reviewToken returned by preview")
		}
	} else if in.UID != "" || in.ReviewToken != "" || in.RestartAt != "" || in.ExpectedResourceVersion != "" {
		return field, errors.New("preview accepts only the action and its arguments")
	}
	if in.Action != "scale" && in.Replicas != nil {
		return field, errors.New("replicas is only valid for scale")
	}
	if in.Action != "restart" && in.RestartAt != "" {
		return field, errors.New("restartAt is only valid for restart")
	}
	switch {
	case in.Action == "scale" && (field.kind == "Deployment" || field.kind == "StatefulSet" || field.kind == "Rollout"):
		if in.Replicas == nil || *in.Replicas < 0 || *in.Replicas > 10000 {
			return field, errors.New("scale requires replicas between 0 and 10000")
		}
		field.path = []string{"spec", "replicas"}
		field.after = int64(*in.Replicas)
	case (in.Action == "pause" || in.Action == "resume") && field.kind == "Deployment":
		field.path = []string{"spec", "paused"}
		field.after = in.Action == "pause"
	case (in.Action == "suspend" || in.Action == "resume") && field.kind == "CronJob":
		field.path = []string{"spec", "suspend"}
		field.after = in.Action == "suspend"
	case in.Action == "restart" && (field.kind == "Deployment" || field.kind == "StatefulSet" || field.kind == "DaemonSet" || field.kind == "Rollout"):
		layout := time.RFC3339Nano
		field.path = []string{"spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt"}
		if field.kind == "Rollout" {
			layout = time.RFC3339
			field.path = []string{"spec", rollouts.RestartAtField}
		}
		marker := now.UTC().Format(layout)
		if execute {
			t, err := time.Parse(layout, in.RestartAt)
			if err != nil || t.UTC().Format(layout) != in.RestartAt || t.After(now) {
				return field, errors.New("restartAt must be the UTC restart marker returned by preview")
			}
			marker = in.RestartAt
		}
		field.after = marker
	default:
		return field, fmt.Errorf("action %q is not supported on %q", in.Action, kind)
	}
	return field, nil
}

func (f workloadActionField) value(obj *unstructured.Unstructured) (any, error) {
	v, found, err := unstructured.NestedFieldNoCopy(obj.Object, f.path...)
	if err != nil {
		return nil, err
	}
	switch f.after.(type) {
	case bool:
		// apps/v1 omits paused:false; CronJobs also default to running.
		if !found {
			return false, nil
		}
		if value, ok := v.(bool); ok {
			return value, nil
		}
	case int64:
		if !found {
			return nil, nil
		}
		if value, ok := v.(int64); ok {
			return value, nil
		}
	case string:
		if !found {
			return nil, nil
		}
		if value, ok := v.(string); ok {
			return value, nil
		}
	}
	return nil, fmt.Errorf("unexpected type for %s on %s", strings.Join(f.path, "."), f.kind)
}

// The digest is a stale-review check, not an authorization credential. Hub owns
// the human decision. Exclude observed status so a rollout progressing during
// review does not itself invalidate the intended write. Keep the spec and its
// ownership: a target can be adopted between preview and execution.
func (f workloadActionField) review(obj *unstructured.Unstructured, action, connectionID string) (workloadActionView, error) {
	before, err := f.value(obj)
	if err != nil {
		return workloadActionView{}, err
	}
	owners := obj.GetManagedFields()
	materialOwners := owners[:0]
	for _, owner := range owners {
		if owner.Subresource == "status" {
			continue
		}
		owner.Time = nil
		materialOwners = append(materialOwners, owner)
	}
	digest, err := json.Marshal([]any{
		connectionID, f.gvr, obj.GetNamespace(), obj.GetName(), obj.GetUID(),
		obj.GetGeneration(), obj.Object["spec"], obj.GetLabels(), obj.GetAnnotations(),
		obj.GetOwnerReferences(), materialOwners, action, f.path, f.after,
	})
	if err != nil {
		return workloadActionView{}, err
	}
	sum := sha256.Sum256(digest)
	field := strings.Join(f.path, ".")
	if action == "restart" && f.kind != "Rollout" {
		field = `spec.template.metadata.annotations["kubectl.kubernetes.io/restartedAt"]`
	}
	return workloadActionView{
		Action: action, Kind: f.kind, Namespace: obj.GetNamespace(), Name: obj.GetName(),
		UID: string(obj.GetUID()), ResourceVersion: obj.GetResourceVersion(),
		ReviewToken: hex.EncodeToString(sum[:]), Field: field, Before: before, After: f.after,
	}, nil
}

func (s *Server) handleWorkloadActionPreview(w http.ResponseWriter, r *http.Request) {
	s.workloadAction(w, r, false)
}

func (s *Server) handleWorkloadActionExecute(w http.ResponseWriter, r *http.Request) {
	s.workloadAction(w, r, true)
}

// Both endpoints use the captured connection and caller's live Kubernetes
// identity. Preview issues only GET. Execute applies one reviewed field using
// the UID and current resourceVersion at the API server, with no blind retry.
func (s *Server) workloadAction(w http.ResponseWriter, r *http.Request, execute bool) {
	ns, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if len(validation.IsDNS1123Label(ns)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 {
		s.writeError(w, http.StatusBadRequest, "a valid namespace and resource name are required")
		return
	}
	if failure := outsideNamespacePin(ns); failure != nil {
		s.writeErrorCode(w, readStatusFor(failure.code), string(failure.code), failure.message)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		} else {
			s.writeError(w, http.StatusBadRequest, "could not read request body")
		}
		return
	}
	var in workloadActionInput
	fieldErrors, err := kjson.UnmarshalStrict(data, &in)
	if err != nil || len(fieldErrors) > 0 {
		s.writeError(w, http.StatusBadRequest, "invalid workload action: "+errors.Join(append(fieldErrors, err)...).Error())
		return
	}
	field, err := resolveWorkloadAction(chi.URLParam(r, "kind"), in, execute, time.Now())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeErrorCode(w, http.StatusServiceUnavailable, errCodeClusterUnavailable, "cluster client unavailable")
		return
	}
	if execute {
		auth.AuditLog(r, ns, name)
	}
	obj, err := client.Resource(field.gvr).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		s.writeWorkloadActionError(w, err)
		return
	}
	if obj.GetUID() == "" || obj.GetResourceVersion() == "" || obj.GetName() != name || obj.GetNamespace() != ns || obj.GetKind() != field.kind {
		s.writeError(w, http.StatusBadGateway, "Kubernetes returned a resource without the requested identity or version")
		return
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		s.writeErrorCode(w, http.StatusConflict, errCodeWorkloadTerminating, "the workload is being deleted")
		return
	}
	// In-cluster replicas can serve the same Hub-bound cluster. The Hub binds
	// the cluster ID to its approval. Desktop connections can switch locally.
	var connectionID string
	if connectionIdentityPublished() {
		connectionID = k8s.ConnectionFrom(r.Context()).ID()
	}
	view, err := field.review(obj, in.Action, connectionID)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if execute && (view.UID != in.UID || view.ReviewToken != in.ReviewToken || (in.ExpectedResourceVersion != "" && string(in.ExpectedResourceVersion) != view.ResourceVersion)) {
		s.writeErrorCode(w, http.StatusConflict, errCodeWorkloadChanged, "the target or proposed change differs from the reviewed action; request a fresh preview")
		return
	}
	if in.Action == "restart" {
		if err := k8score.CheckReviewedRestart(obj); err != nil {
			s.writeErrorCode(w, http.StatusConflict, errCodeRestartNotApplicable, err.Error())
			return
		}
	}
	if view.Before == view.After {
		s.writeErrorCode(w, http.StatusConflict, "workload_action_unchanged", "the target is already in the requested state")
		return
	}
	if previous, ok := view.Before.(string); ok && field.kind == "Rollout" && in.Action == "restart" {
		if prior, err := time.Parse(time.RFC3339, previous); err == nil {
			marker, _ := time.Parse(time.RFC3339, view.After.(string))
			if !marker.After(prior) {
				s.writeErrorCode(w, http.StatusConflict, "workload_action_unchanged", "the existing rollout restart cutoff already covers the requested time")
				return
			}
		}
	}
	if !execute {
		s.writeJSON(w, view)
		return
	}
	patch := map[string]any{"metadata": map[string]any{"uid": view.UID}}
	if err := unstructured.SetNestedField(patch, field.after, field.path...); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stored, err := integration.ApplyVersionedMergePatch(r.Context(), client, field.gvr, obj, patch)
	if err != nil {
		s.writeWorkloadActionError(w, err)
		return
	}
	actual, err := field.value(stored)
	if err != nil || actual != view.After || stored.GetUID() != obj.GetUID() {
		s.writeErrorCode(w, http.StatusConflict, "workload_action_not_stored", "Kubernetes accepted the request but did not return the reviewed value; check the live resource")
		return
	}
	view.ResourceVersion = stored.GetResourceVersion()
	s.writeJSON(w, view)
}

func (s *Server) writeWorkloadActionError(w http.ResponseWriter, err error) {
	var status apierrors.APIStatus
	switch {
	case apierrors.IsNotFound(err):
		s.writeErrorCode(w, http.StatusNotFound, errCodeWorkloadNotFound, "the workload was not found")
	case apierrors.IsConflict(err):
		s.writeErrorCode(w, http.StatusConflict, errCodeWorkloadChanged, "Kubernetes rejected the change as conflicting; refresh the preview")
	case errors.As(err, &status) && status.Status().Code >= 400 && status.Status().Code < 600:
		s.writeError(w, int(status.Status().Code), err.Error())
	default:
		s.writeError(w, http.StatusBadGateway, "Kubernetes did not return a confirmed result: "+err.Error())
	}
}
