package gitops

import (
	"fmt"
	"regexp"
	"strings"

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
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Err       error  `json:"-"`
}

func (e *PermissionDenied) Error() string { return e.Err.Error() }
func (e *PermissionDenied) Unwrap() error { return e.Err }
func (e *PermissionDenied) Summary() string {
	label := e.Resource
	if e.Group == "argoproj.io" && e.Resource == "applications" {
		label = "Argo CD Applications"
	} else if entry, err := ResolveFluxKind(e.Resource); err == nil {
		label = "Flux " + entry.Kind
		if e.Name != "" {
			label += " " + e.Name
		}
	}
	return fmt.Sprintf("Your role can't %s %s in %s.", e.Verb, label, e.Namespace)
}

type AdmissionDenied struct{ Err error }

func (e *AdmissionDenied) Error() string { return e.Err.Error() }
func (e *AdmissionDenied) Unwrap() error { return e.Err }

var authorizerDenial = regexp.MustCompile(`is forbidden: User "[^"\n]+" cannot \w+ resource `)

// ClassifyPermissionError distinguishes authorizer refusals from admission policies.
func ClassifyPermissionError(err error, verb string, gvr schema.GroupVersionResource, namespace, name string) error {
	if !apierrors.IsForbidden(err) {
		return err
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "admission webhook") || strings.Contains(message, "denied by a webhook") || strings.Contains(message, "denied by webhook") {
		return &AdmissionDenied{Err: err}
	}
	if !authorizerDenial.MatchString(err.Error()) {
		return err
	}
	kind := "Application"
	if entry, lookupErr := ResolveFluxKind(gvr.Resource); lookupErr == nil {
		kind = entry.Kind
	}
	return &PermissionDenied{Kind: kind, Verb: verb, Group: gvr.Group, Resource: gvr.Resource, Namespace: namespace, Name: name, Err: err}
}
