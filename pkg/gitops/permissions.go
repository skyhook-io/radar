package gitops

import (
	"errors"
	"fmt"
	"regexp"

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
	tool := "Flux"
	if e.Group == "argoproj.io" {
		tool = "Argo CD"
	}
	label := tool + " " + e.Kind
	if e.Kind == "" {
		label = tool + " " + e.Resource
	}
	if e.Name != "" {
		label += " " + e.Name
	}
	verb := e.Verb
	if verb == "get" {
		verb = "read"
	}
	return fmt.Sprintf("Kubernetes denied permission to %s %s in %s.", verb, label, e.Namespace)
}

// AdmissionDenied is a write that an admission webhook or a
// ValidatingAdmissionPolicy refused. Mechanism, Policy and Message are parsed
// from the apiserver's denial when its shape is recognized.
type AdmissionDenied struct {
	Mechanism string
	Policy    string
	Message   string
	Err       error
}

func (e *AdmissionDenied) Error() string { return e.Err.Error() }
func (e *AdmissionDenied) Unwrap() error { return e.Err }
func (e *AdmissionDenied) Summary() string {
	if e.Message == "" {
		return fmt.Sprintf("Rejected by %s %s.", e.Mechanism, e.Policy)
	}
	return fmt.Sprintf("Rejected by %s %s: %s", e.Mechanism, e.Policy, e.Message)
}

var (
	forbiddenEnvelope = regexp.MustCompile(`^(?:[a-z0-9.-]+(?: "(?:[^"\\]|\\.)*")? is )?forbidden: `)
	authorizerDenial  = regexp.MustCompile(`^User "(?:[^"\\\n]|\\.)*" cannot (\w+) resource "([^"\n]+)"`)
	policyDenial      = regexp.MustCompile(`(?s)^ValidatingAdmissionPolicy '([^'\n]+)'(?: with binding '[^'\n]*')? denied request: (.*)`)
	webhookDenial     = regexp.MustCompile(`(?s)^admission webhook "([^"\n]+)" denied the request(?:: (.*)| without explanation)?$`)
)

// ClassifyPermissionError distinguishes the API server's authorization denial
// from an admission webhook or policy refusal. A webhook may choose the HTTP
// status of its refusal.
func ClassifyPermissionError(err error, verb string, gvr schema.GroupVersionResource, namespace, name string) error {
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return err
	}
	message := forbiddenEnvelope.ReplaceAllString(status.Status().Message, "")
	if match := authorizerDenial.FindStringSubmatch(message); match != nil {
		// The authorizer can append a reason in its own words. Classify the
		// envelope, including impersonation refusals, before that reason.
		if !apierrors.IsForbidden(err) || match[1] != verb || match[2] != gvr.Resource {
			return err
		}
		kind := "Application"
		if entry, lookupErr := ResolveFluxKind(gvr.Resource); lookupErr == nil {
			kind = entry.Kind
		}
		return &PermissionDenied{Kind: kind, Verb: verb, Group: gvr.Group, Resource: gvr.Resource, Namespace: namespace, Name: name, Err: err}
	}
	if verb == "patch" {
		if match := policyDenial.FindStringSubmatch(message); match != nil {
			return &AdmissionDenied{Mechanism: "ValidatingAdmissionPolicy", Policy: match[1], Message: match[2], Err: err}
		}
		if match := webhookDenial.FindStringSubmatch(message); match != nil {
			return &AdmissionDenied{Mechanism: "admission webhook", Policy: match[1], Message: match[2], Err: err}
		}
	}
	return err
}
