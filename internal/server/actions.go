package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
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

// decodeActionRequest reads the body and checks the reviewed context. The
// dynamic client is the caller's, snapshotted with the context it belongs to.
// The error is already written when ok is false.
func (s *Server) decodeActionRequest(w http.ResponseWriter, r *http.Request) (integration.ActionRequest, dynamic.Interface, bool) {
	var req integration.ActionRequest
	if err := decodeBoundedJSONBody(w, r, integration.ActionBodyLimit, &req); err != nil {
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
	if err := integration.CheckReviewedContext(req.ReviewedContext, contextName); err != nil {
		s.writeActionError(w, "actions", err, "context", "", "")
		return req, nil, false
	}
	return req, dyn, true
}

// writeActionError answers a failed action. A refusal keeps its status, code,
// current facts and completed steps; an apiserver error maps to its status,
// and a Forbidden one retains the apiserver's exact denial: a multi-resource
// action can fail on a prerequisite read rather than its write. tag prefixes logs.
func (s *Server) writeActionError(w http.ResponseWriter, tag string, err error, action, namespace, name string) {
	var ae *integration.ActionError
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
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		status = http.StatusConflict
	case apierrors.IsInvalid(err):
		status = http.StatusUnprocessableEntity
	}
	log.Printf("[%s] Failed to %s %s/%s: %v", tag, sanitizeForLog(action), sanitizeForLog(namespace), sanitizeForLog(name), err)
	s.writeError(w, status, msg)
}

// grantPermission answers one grant for the caller: allowed, denied, or
// unknown when the SubjectAccessReview itself failed.
func (s *Server) grantPermission(r *http.Request, g Grant) string {
	allowed, authoritative := s.grantDecision(r, g)
	switch {
	case !authoritative:
		return integration.PermissionUnknown
	case allowed:
		return integration.PermissionAllowed
	default:
		return integration.PermissionDenied
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
