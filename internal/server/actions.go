package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// The action contract shared by integrations that write to the cluster on the
// caller's behalf. A capability says whether an action is offered and why
// not; a POST binds the context and facts the caller reviewed and is refused
// with a stable code when they no longer hold. Writes are made with the
// caller's impersonated client and are never retried automatically.

// Grant lives in auth because Prometheus-backed reads report the grant they
// lacked too.
type Grant = auth.Grant

const (
	actionBodyLimit = 64 << 10

	actionCodeChanged        = "changed"
	actionCodeContextChanged = "context_changed"
	actionCodeBlocked        = "blocked"
	actionCodePartial        = "partial"

	permissionAllowed = "allowed"
	permissionDenied  = "denied"
	permissionUnknown = "unknown"
)

// ActionCapability is one action's verdict: allowed only when the state
// guards pass and the caller's grant is not known to be missing. An unknown
// grant (the SubjectAccessReview itself failed) leaves the apiserver to decide.
type ActionCapability struct {
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason,omitempty"`
	Permission string `json:"permission"`
	Grant      *Grant `json:"grant,omitempty"`
}

// ActionRequest is the POST body of every action.
type ActionRequest struct {
	ReviewedContext string          `json:"reviewedContext"`
	UID             string          `json:"uid"`
	Facts           json.RawMessage `json:"facts,omitempty"`
	Params          json.RawMessage `json:"params,omitempty"`
}

// actionError is a refusal with a stable code; Current carries the facts as
// they are now when a confirmation no longer matches.
type actionError struct {
	Status  int
	Code    string
	Message string
	Current any
	// Completed lists the mutations that took effect before a multi-step
	// action stopped (code partial).
	Completed []string
}

func (e *actionError) Error() string { return e.Message }

func refuseAction(status int, code, format string, args ...any) *actionError {
	return &actionError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// changedAction refuses a confirmation whose reviewed facts no longer hold.
func changedAction(current any, format string, args ...any) *actionError {
	e := refuseAction(http.StatusConflict, actionCodeChanged, format, args...)
	e.Current = current
	return e
}

func blockedAction(reason string) error {
	return refuseAction(http.StatusConflict, actionCodeBlocked, "%s", reason)
}

// partialAction reports a multi-step action that stopped after some of its
// mutations took effect; retrying it blindly would act on a changed target.
func partialAction(completed []string, cause error) *actionError {
	status := http.StatusInternalServerError
	var ae *actionError
	switch {
	case errors.As(cause, &ae):
		status = ae.Status
	case apierrors.IsForbidden(cause):
		status = http.StatusForbidden
	case apierrors.IsConflict(cause), apierrors.IsNotFound(cause):
		status = http.StatusConflict
	case errors.Is(cause, context.DeadlineExceeded) || apierrors.IsTimeout(cause) || apierrors.IsServerTimeout(cause):
		status = http.StatusGatewayTimeout
	}
	e := &actionError{
		Status: status, Code: actionCodePartial, Completed: append([]string(nil), completed...),
		Message: fmt.Sprintf("Stopped part-way: %s. Already done: %s", cause.Error(), strings.Join(completed, ", ")),
	}
	if ae != nil {
		e.Current = ae.Current
	}
	return e
}

// decodeActionRequest reads the body and checks the reviewed context. The
// dynamic client is the caller's, snapshotted with the context it belongs to.
// The error is already written when ok is false.
func (s *Server) decodeActionRequest(w http.ResponseWriter, r *http.Request) (ActionRequest, dynamic.Interface, bool) {
	var req ActionRequest
	if err := decodeBoundedJSONBody(w, r, actionBodyLimit, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return req, nil, false
		}
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return req, nil, false
	}
	if req.ReviewedContext == "" || req.UID == "" {
		s.writeError(w, http.StatusBadRequest, "reviewedContext and uid are required")
		return req, nil, false
	}
	dyn, contextName := s.getDynamicClientSnapshotForRequest(r)
	if dyn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return req, nil, false
	}
	if err := checkReviewedContext(req.ReviewedContext, contextName); err != nil {
		s.writeActionError(w, "actions", err, "context", "", "", nil)
		return req, nil, false
	}
	return req, dyn, true
}

func checkReviewedContext(reviewed, active string) error {
	if reviewed != active {
		return refuseAction(http.StatusConflict, actionCodeContextChanged,
			"The active cluster context is %q, not the %q you reviewed; review the action again", active, reviewed)
	}
	return nil
}

// decodeActionParams decodes an action's params strictly: an unknown field is
// a client that means something this server does not do.
func decodeActionParams(raw json.RawMessage, into any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		trimmed = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return refuseAction(http.StatusBadRequest, "", "invalid params: %v", err)
	}
	return nil
}

// writeActionError answers a failed action. A refusal keeps its status, code,
// current facts and completed steps; an apiserver error maps to its status,
// and a Forbidden one names the grant needs(action) returns, bound to
// namespace. tag prefixes the log lines.
func (s *Server) writeActionError(w http.ResponseWriter, tag string, err error, action, namespace, name string, needs func(action string) (Grant, bool)) {
	var ae *actionError
	if errors.As(err, &ae) {
		log.Printf("[%s] %q %s/%s refused %d: %s", tag, action, sanitizeForLog(namespace), sanitizeForLog(name), ae.Status, ae.Message)
		body := map[string]any{"error": ae.Message}
		if ae.Code != "" {
			body["code"] = ae.Code
		}
		if ae.Current != nil {
			body["current"] = ae.Current
		}
		if len(ae.Completed) > 0 {
			body["completed"] = ae.Completed
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(ae.Status)
		if encErr := json.NewEncoder(w).Encode(body); encErr != nil {
			log.Printf("Failed to encode error response: %v", encErr)
		}
		return
	}
	msg := err.Error()
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err):
		status = http.StatusGatewayTimeout
	case apierrors.IsNotFound(err):
		status = http.StatusNotFound
	case apierrors.IsForbidden(err):
		status = http.StatusForbidden
		if needs != nil {
			if g, ok := needs(action); ok {
				msg = "This needs " + g.In(namespace).String() + ": " + msg
			}
		}
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		status = http.StatusConflict
	case apierrors.IsInvalid(err):
		status = http.StatusUnprocessableEntity
	}
	log.Printf("[%s] Failed to %s %s/%s (%d): %v", tag, sanitizeForLog(action), sanitizeForLog(namespace), sanitizeForLog(name), status, err)
	s.writeError(w, status, msg)
}

// mergePatchAtVersion merge-patches obj bound to the resourceVersion it was
// read at, so a write the caller did not review is refused with a Conflict
// rather than applied over. Callers turn the Conflict into "changed"; it is
// never retried.
func mergePatchAtVersion(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured, body map[string]any, subresources ...string) error {
	meta, _ := body["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		body["metadata"] = meta
	}
	meta["resourceVersion"] = obj.GetResourceVersion()
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = dyn.Resource(gvr).Namespace(obj.GetNamespace()).Patch(ctx, obj.GetName(), types.MergePatchType, data, metav1.PatchOptions{}, subresources...)
	return err
}

// grantText words an optional grant, "" when there is none.
func grantText(g *Grant) string {
	if g == nil {
		return ""
	}
	return g.String()
}

// grantPermission answers one grant for the caller: allowed, denied, or
// unknown when the SubjectAccessReview itself failed.
func (s *Server) grantPermission(r *http.Request, g Grant) string {
	allowed, authoritative := s.grantDecision(r, g)
	switch {
	case !authoritative:
		return permissionUnknown
	case allowed:
		return permissionAllowed
	default:
		return permissionDenied
	}
}

// grantDecision asks for g as the caller. Subresource answers are memoized on
// the same per-user cache as canRead, keyed resource/subresource.
func (s *Server) grantDecision(r *http.Request, g Grant) (bool, bool) {
	if auth.UserFromContext(r.Context()) == nil {
		return localCanI(r.Context(), g)
	}
	if g.Subresource == "" {
		return s.canReadDecision(r, g.Group, g.Resource, g.Namespace, g.Verb)
	}
	key := g.Resource + "/" + g.Subresource
	var perms *auth.UserPermissions
	if user := auth.UserFromContext(r.Context()); user != nil && s.permCache != nil {
		perms = s.permCache.Get(user.Username, user.Groups)
	}
	if perms != nil {
		if v, ok := perms.CanI(g.Verb, g.Group, key, g.Namespace); ok {
			return v, true
		}
	}
	allowed, authoritative := s.canReadSubresourceDecision(r, g.Group, g.Resource, g.Subresource, g.Namespace, g.Verb)
	if authoritative && perms != nil {
		perms.SetCanI(g.Verb, g.Group, key, g.Namespace, allowed)
	}
	return allowed, authoritative
}

// Without auth the apiserver still enforces the kubeconfig identity's RBAC on
// every write and proxy read; asking it first lets capabilities name a missing
// grant instead of offering an action that will fail.
var (
	localCanIMu   sync.Mutex
	localCanIMemo = map[string]localCanIEntry{}
	localCanITTL  = 30 * time.Second
)

type localCanIEntry struct {
	allowed bool
	expires time.Time
}

func localCanI(ctx context.Context, g Grant) (bool, bool) {
	resource := g.Resource
	if g.Subresource != "" {
		resource += "/" + g.Subresource
	}
	key := strings.Join([]string{k8s.GetContextName(), g.Verb, g.Group, resource, g.Namespace}, "\x00")
	now := time.Now()
	localCanIMu.Lock()
	if e, ok := localCanIMemo[key]; ok && now.Before(e.expires) {
		localCanIMu.Unlock()
		return e.allowed, true
	}
	localCanIMu.Unlock()
	client := k8s.GetClient()
	if client == nil {
		return true, true
	}
	allowed, apiErr := k8score.CanI(ctx, client, g.Namespace, g.Group, resource, g.Verb)
	if apiErr {
		return false, false
	}
	localCanIMu.Lock()
	localCanIMemo[key] = localCanIEntry{allowed: allowed, expires: now.Add(localCanITTL)}
	localCanIMu.Unlock()
	return allowed, true
}

// capabilityVerdict folds a guard reason and the permission of each grant
// (perms[i] answers grants[i]) into a verdict. The first denied grant is
// named; an unknown grant leaves the action offered.
func capabilityVerdict(guard string, perms []string, grants []Grant) ActionCapability {
	out := ActionCapability{Permission: permissionAllowed}
	for i, p := range perms {
		if p == permissionDenied {
			out.Permission = permissionDenied
			out.Grant = grants[i].Ref()
			break
		}
		if p == permissionUnknown {
			out.Permission = permissionUnknown
			if out.Grant == nil {
				out.Grant = grants[i].Ref()
			}
		}
	}
	if out.Permission == permissionAllowed {
		out.Grant = nil
	}
	switch {
	case out.Permission == permissionDenied:
		out.Reason = "You are not allowed to " + out.Grant.String()
	case guard != "":
		out.Reason = guard
	default:
		out.Allowed = true
	}
	return out
}
