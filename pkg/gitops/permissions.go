package gitops

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// PermissionDenied records the exact API operation, including a Flux source
// in another namespace. Unwrap preserves Kubernetes error classification.
type PermissionDenied struct {
	Verb      string `json:"verb"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Err       error  `json:"-"`
}

func (e *PermissionDenied) Error() string { return e.Err.Error() }
func (e *PermissionDenied) Unwrap() error { return e.Err }
func (e *PermissionDenied) Summary() string {
	return fmt.Sprintf("Your role can't %s %s in API group %s in namespace %s. Ask your cluster admin for GitOps action access.", e.Verb, e.Resource, e.Group, e.Namespace)
}

func permissionError(err error, verb string, gvr schema.GroupVersionResource, namespace string) error {
	if !apierrors.IsForbidden(err) {
		return err
	}
	return &PermissionDenied{Verb: verb, Group: gvr.Group, Resource: gvr.Resource, Namespace: namespace, Err: err}
}
