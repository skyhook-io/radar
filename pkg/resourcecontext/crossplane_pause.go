package resourcecontext

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/conditions"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// CrossplanePauseState describes a CRD's exact annotation intent independently of readiness.
// Acknowledged means a stored Synced=False/ReconcilePaused condition corroborates
// that intent, not a fresh check, generation match, or any child controller's state.
type CrossplanePauseState string

const (
	CrossplanePauseRequested    CrossplanePauseState = "requested"
	CrossplanePauseAcknowledged CrossplanePauseState = "acknowledged"
)

// CrossplanePauseFor reports annotation intent even before an XR or Claim is
// bound. Known built-in identities stay excluded after conversion to unstructured.
// Requested does not establish that a controller honors the annotation.
// An absent value makes no claim about policy-based pause or a prior resume.
func CrossplanePauseFor(obj runtime.Object) CrossplanePauseState {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || u == nil || u.GetAnnotations()["crossplane.io/paused"] != "true" {
		return ""
	}
	if group, builtin := resourceid.BuiltinGroup(u.GetKind()); builtin && group == u.GroupVersionKind().Group {
		return ""
	}
	synced, _ := conditions.Find(u, "Synced")
	if synced.Status == "False" && synced.Reason == "ReconcilePaused" {
		return CrossplanePauseAcknowledged
	}
	return CrossplanePauseRequested
}
