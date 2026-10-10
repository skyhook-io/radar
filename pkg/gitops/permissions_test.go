package gitops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestGitOpsDenialPreservesExactOperation(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "demo", "namespace": "argocd"}}}
	for _, verb := range []string{"get", "patch"} {
		t.Run(verb, func(t *testing.T) {
			client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), app)
			client.PrependReactor(verb, "applications", func(action ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "demo", fmt.Errorf(`User "alice" cannot %s resource "applications"`, verb))
			})
			_, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{})
			var denied *PermissionDenied
			if !apierrors.IsForbidden(err) || !errors.As(err, &denied) || denied.Verb != verb || denied.Namespace != "argocd" || denied.Resource != "applications" {
				t.Fatalf("denial=%+v err=%v", denied, err)
			}
		})
	}
}

func TestFluxSourceReadDeniedDoesNotPatch(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "demo", "namespace": "apps"}, "spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo", "namespace": "sources"}}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	client.PrependReactor("get", "gitrepositories", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories"}, "repo", errors.New(`User "alice" cannot get resource "gitrepositories"`))
	})
	_, err := SyncFluxWithSource(context.Background(), client, "kustomization", "apps", "demo")
	var denied *PermissionDenied
	if !errors.As(err, &denied) || denied.Verb != "get" || denied.Namespace != "sources" || denied.Resource != "gitrepositories" {
		t.Fatalf("denial=%+v err=%v", denied, err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "patch" {
			t.Fatalf("patch after denied source GET: %v", action)
		}
	}
}

func TestGitOpsForbiddenClassification(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Resource: "applications"}
	forbidden := func(message string) error {
		return apierrors.NewForbidden(gvr.GroupResource(), "demo", errors.New(message))
	}
	// Mirrors the apiserver: a ValidatingAdmissionPolicy denial is built with
	// admission.NewForbidden, then takes the validation's reason (Invalid
	// when unset) and that reason's code.
	policy := func(message string, reason metav1.StatusReason, code int32) error {
		err := apierrors.NewForbidden(gvr.GroupResource(), "demo", errors.New(message))
		err.ErrStatus.Reason, err.ErrStatus.Code = reason, code
		return err
	}
	webhookWithoutCode := &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 400, Message: `admission webhook "policy.example.com" denied the request: owner label required`}}
	for _, tc := range []struct {
		name    string
		err     error
		want    string // "rbac", "admission" or "" for unclassified
		summary string
	}{
		{"policy default reason", policy(`ValidatingAdmissionPolicy 'freeze' with binding 'freeze-binding' denied request: Application demo is change-frozen`, metav1.StatusReasonInvalid, 422), "admission", "Rejected by ValidatingAdmissionPolicy freeze: Application demo is change-frozen"},
		{"policy forbidden reason", policy(`ValidatingAdmissionPolicy 'freeze' with binding 'freeze-binding' denied request: Application demo is change-frozen`, metav1.StatusReasonForbidden, 403), "admission", "Rejected by ValidatingAdmissionPolicy freeze: Application demo is change-frozen"},
		{"policy without binding", policy(`ValidatingAdmissionPolicy 'freeze' denied request: frozen`, metav1.StatusReasonInvalid, 422), "admission", "Rejected by ValidatingAdmissionPolicy freeze: frozen"},
		{"webhook", forbidden(`admission webhook "validation.gatekeeper.sh" denied the request: missing owner`), "admission", "Rejected by admission webhook validation.gatekeeper.sh: missing owner"},
		{"webhook without code", webhookWithoutCode, "admission", "Rejected by admission webhook policy.example.com: owner label required"},
		{"webhook without explanation", forbidden(`admission webhook "validation.gatekeeper.sh" denied the request without explanation`), "admission", "Rejected by admission webhook validation.gatekeeper.sh."},
		{"denied by a webhook", forbidden(`User "alice" cannot patch resource "applications": denied by a webhook`), "admission", ""},
		{"denied by webhook policy", forbidden(`User "alice" cannot patch resource "applications": denied by webhook policy.example`), "admission", ""},
		{"rbac", forbidden(`User "alice" cannot patch resource "applications" in API group "argoproj.io" in the namespace "argocd"`), "rbac", "Your role can't patch Argo CD Application demo in argocd."},
		{"radar impersonation refused", forbidden(`User "system:serviceaccount:radar:radar" cannot impersonate resource "users" in API group "" at the cluster scope`), "", ""},
		{"unrecognized forbidden", forbidden(`policy rejected this change`), "", ""},
		{"invalid without policy", apierrors.NewInvalid(schema.GroupKind{Group: "argoproj.io", Kind: "Application"}, "demo", nil), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ClassifyPermissionError(tc.err, "patch", gvr, "argocd", "demo")
			var denied *PermissionDenied
			var admission *AdmissionDenied
			got := ""
			switch {
			case errors.As(err, &denied):
				got = "rbac"
				if denied.Summary() != tc.summary {
					t.Errorf("summary=%q, want %q", denied.Summary(), tc.summary)
				}
			case errors.As(err, &admission):
				got = "admission"
				if tc.summary != "" && admission.Summary() != tc.summary {
					t.Errorf("summary=%q, want %q", admission.Summary(), tc.summary)
				}
			}
			if got != tc.want || apierrors.ReasonForError(err) != apierrors.ReasonForError(tc.err) {
				t.Fatalf("classified %q as %q (%T), want %q", tc.err, got, err, tc.want)
			}
		})
	}
}

func TestPermissionDeniedSummaryNamesTheObject(t *testing.T) {
	for _, tc := range []struct {
		denied PermissionDenied
		want   string
	}{
		{PermissionDenied{Verb: "get", Group: "argoproj.io", Resource: "applications", Kind: "Application", Namespace: "argocd", Name: "demo"}, "Your role can't read Argo CD Application demo in argocd."},
		{PermissionDenied{Verb: "patch", Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories", Kind: "GitRepository", Namespace: "sources", Name: "repo"}, "Your role can't patch Flux GitRepository repo in sources."},
	} {
		if got := tc.denied.Summary(); got != tc.want {
			t.Errorf("Summary()=%q, want %q", got, tc.want)
		}
	}
}

func TestTerminateReportsPolicyDenialNotFinishedOperation(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "demo", "namespace": "argocd"},
		"operation": map[string]any{"sync": map[string]any{}}, "status": map[string]any{"operationState": map[string]any{"phase": "Running"}}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), app)
	client.PrependReactor("patch", "applications", func(ktesting.Action) (bool, runtime.Object, error) {
		err := apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "demo", errors.New(`ValidatingAdmissionPolicy 'freeze' with binding 'freeze' denied request: frozen`))
		err.ErrStatus.Reason, err.ErrStatus.Code = metav1.StatusReasonInvalid, 422
		return true, nil, err
	})
	_, err := TerminateArgoSync(context.Background(), client, "argocd", "demo")
	var admission *AdmissionDenied
	if !errors.As(err, &admission) || errors.Is(err, ErrNoOperationInProgress) {
		t.Fatalf("err=%v", err)
	}
}

func TestFluxSyncSourceUnsupported(t *testing.T) {
	entry, err := ResolveFluxKind("helmrelease")
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"chartRef": map[string]any{"kind": "OCIRepository", "name": "chart"}}}}
	_, err = FluxSyncSource(obj, entry, "apps", "demo")
	if !errors.Is(err, ErrSyncWithSourceUnsupported) || !strings.Contains(err.Error(), "spec.chartRef") {
		t.Fatalf("err=%v", err)
	}
}
