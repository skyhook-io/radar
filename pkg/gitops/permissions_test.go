package gitops

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, "demo", errors.New("not allowed"))
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
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories"}, "repo", errors.New("not allowed"))
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
