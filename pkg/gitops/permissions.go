package gitops

import (
	"errors"
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
	return fmt.Sprintf("Your role can't %s %s in %s.", verb, label, e.Namespace)
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
	if e.Policy == "" {
		return "Rejected by an admission policy: " + e.Err.Error()
	}
	if e.Message == "" {
		return fmt.Sprintf("Rejected by %s %s.", e.Mechanism, e.Policy)
	}
	return fmt.Sprintf("Rejected by %s %s: %s", e.Mechanism, e.Policy, e.Message)
}

var (
	authorizerDenial = regexp.MustCompile(`is forbidden: User "[^"\n]+" cannot \w+ resource "([^"\n]+)"`)
	policyDenial     = regexp.MustCompile(`(?s)ValidatingAdmissionPolicy '([^'\n]+)'(?: with binding '[^'\n]*')? denied request: (.*)`)
	webhookDenial    = regexp.MustCompile(`(?s)admission webhook "([^"\n]+)" denied the request(?:: (.*))?`)
)

// ClassifyPermissionError distinguishes authorizer refusals from admission
// policies. Admission denials are recognized at any status: a
// ValidatingAdmissionPolicy without a reason answers 422 Invalid, and a
// webhook that sets no code answers 400.
func ClassifyPermissionError(err error, verb string, gvr schema.GroupVersionResource, namespace, name string) error {
	var status apierrors.APIStatus
	if err == nil || !errors.As(err, &status) {
		return err
	}
	message := err.Error()
	if match := policyDenial.FindStringSubmatch(message); match != nil {
		return &AdmissionDenied{Mechanism: "ValidatingAdmissionPolicy", Policy: match[1], Message: match[2], Err: err}
	}
	if match := webhookDenial.FindStringSubmatch(message); match != nil {
		return &AdmissionDenied{Mechanism: "admission webhook", Policy: match[1], Message: match[2], Err: err}
	}
	if !apierrors.IsForbidden(err) {
		return err
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "admission webhook") || strings.Contains(lower, "denied by a webhook") || strings.Contains(lower, "denied by webhook") {
		return &AdmissionDenied{Err: err}
	}
	// The denied resource must be the one acted on, so Radar's own refused
	// impersonation ("cannot impersonate resource \"users\"") never reads as
	// the caller's missing grant.
	if match := authorizerDenial.FindStringSubmatch(message); match == nil || match[1] != gvr.Resource {
		return err
	}
	kind := "Application"
	if entry, lookupErr := ResolveFluxKind(gvr.Resource); lookupErr == nil {
		kind = entry.Kind
	}
	return &PermissionDenied{Kind: kind, Verb: verb, Group: gvr.Group, Resource: gvr.Resource, Namespace: namespace, Name: name, Err: err}
}
