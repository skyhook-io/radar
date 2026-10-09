package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/auth"
)

const (
	ActionBodyLimit = 64 << 10

	ActionCodeChanged        = "changed"
	ActionCodeContextChanged = "context_changed"
	ActionCodeBlocked        = "blocked"
	ActionCodePartial        = "partial"

	PermissionAllowed = "allowed"
	PermissionDenied  = "denied"
	PermissionUnknown = "unknown"
)

// ActionCapability is one action's verdict: allowed only when the state
// guards pass and the caller's grant is not known to be missing. An unknown
// grant (the SubjectAccessReview itself failed) leaves the apiserver to decide.
type ActionCapability struct {
	Allowed    bool        `json:"allowed"`
	Reason     string      `json:"reason,omitempty"`
	ReasonCode string      `json:"reasonCode,omitempty"`
	Permission string      `json:"permission"`
	Grant      *auth.Grant `json:"grant,omitempty"`
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
type ActionError struct {
	Status  int
	Code    string
	Message string
	Current any
	// Completed lists the mutations that took effect before a multi-step
	// action stopped (code partial).
	Completed []string
}

func (e *ActionError) Error() string { return e.Message }

func RefuseAction(status int, code, format string, args ...any) *ActionError {
	return &ActionError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// changedAction refuses a confirmation whose reviewed facts no longer hold.
func ChangedAction(current any, format string, args ...any) *ActionError {
	e := RefuseAction(http.StatusConflict, ActionCodeChanged, format, args...)
	e.Current = current
	return e
}

func BlockedAction(reason string) error {
	return RefuseAction(http.StatusConflict, ActionCodeBlocked, "%s", reason)
}

// partialAction reports a multi-step action that stopped after some of its
// mutations took effect; retrying it blindly would act on a changed target.
func PartialAction(completed []string, cause error) *ActionError {
	status := http.StatusInternalServerError
	var ae *ActionError
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
	e := &ActionError{
		Status: status, Code: ActionCodePartial, Completed: append([]string(nil), completed...),
		Message: fmt.Sprintf("Stopped part-way: %s. Already done: %s", cause.Error(), strings.Join(completed, ", ")),
	}
	if ae != nil {
		e.Current = ae.Current
	}
	return e
}

func CheckReviewedContext(reviewed, active string) error {
	if reviewed != active {
		return RefuseAction(http.StatusConflict, ActionCodeContextChanged,
			"The active cluster context is %q, not the %q you reviewed; review the action again", active, reviewed)
	}
	return nil
}

// decodeActionParams decodes an action's params strictly: an unknown field is
// a client that means something this server does not do.
func DecodeActionParams(raw json.RawMessage, into any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		trimmed = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return RefuseAction(http.StatusBadRequest, "", "invalid params: %v", err)
	}
	return nil
}

// mergePatchAtVersion merge-patches obj bound to the resourceVersion it was
// read at, so a write the caller did not review is refused with a Conflict
// rather than applied over. Callers turn the Conflict into "changed"; it is
// never retried.
func MergePatchAtVersion(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured, body map[string]any, subresources ...string) error {
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
func GrantText(g *auth.Grant) string {
	if g == nil {
		return ""
	}
	return g.String()
}

// capabilityVerdict folds a guard reason and the permission of each grant
// (perms[i] answers grants[i]) into a verdict. The first denied grant is
// named; an unknown grant leaves the action offered.
func CapabilityVerdict(guard string, perms []string, grants []auth.Grant) ActionCapability {
	out := ActionCapability{Permission: PermissionAllowed}
	for i, p := range perms {
		if p == PermissionDenied {
			out.Permission = PermissionDenied
			out.Grant = grants[i].Ref()
			break
		}
		if p == PermissionUnknown {
			out.Permission = PermissionUnknown
			if out.Grant == nil {
				out.Grant = grants[i].Ref()
			}
		}
	}
	if out.Permission == PermissionAllowed {
		out.Grant = nil
	}
	switch {
	case out.Permission == PermissionDenied:
		out.Reason = "You are not allowed to " + out.Grant.String()
	case guard != "":
		out.Reason = guard
	default:
		out.Allowed = true
	}
	return out
}
