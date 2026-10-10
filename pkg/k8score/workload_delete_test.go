package k8score

import (
	"context"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The delete preview lists dependents garbage collection removes, so the
// delete itself must cascade: batch/v1 Jobs otherwise orphan their Pods.
func TestDeleteResourceCascadesInTheBackground(t *testing.T) {
	jobs := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	job := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"namespace": "team", "name": "nightly-1"},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{jobs: "JobList"}, job)
	var got *metav1.DeleteOptions
	client.PrependReactor("delete", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		opts := action.(k8stesting.DeleteActionImpl).DeleteOptions
		got = &opts
		return false, nil, nil
	})
	discovery := &ResourceDiscovery{resourceMap: make(map[string]APIResource), gvrMap: make(map[string]schema.GroupVersionResource)}
	discovery.AddAPIResource(APIResource{Group: "batch", Version: "v1", Kind: "Job", Name: "jobs", Namespaced: true, Verbs: []string{"delete"}})

	for _, force := range []bool{false, true} {
		got = nil
		if err := NewWorkloadManager(client, discovery).DeleteResource(context.Background(), DeleteResourceOptions{Kind: "Job", Group: "batch", Namespace: "team", Name: "nightly-1", Force: force}); err != nil {
			t.Fatalf("force=%v: %v", force, err)
		}
		if got == nil || got.PropagationPolicy == nil || *got.PropagationPolicy != metav1.DeletePropagationBackground {
			t.Errorf("force=%v: delete options %+v, want background propagation", force, got)
		}
		if err := client.Tracker().Add(job.DeepCopy()); err != nil {
			t.Fatal(err)
		}
	}
}

// Force delete exists to skip finalizer-driven teardown; deleting with the
// finalizers still in place would run it anyway.
func TestForceDeleteStopsWhenFinalizersCannotBeStripped(t *testing.T) {
	apps := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"namespace": "argocd", "name": "shop", "finalizers": []any{"resources-finalizer.argocd.argoproj.io"}},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{apps: "ApplicationList"}, app)
	client.PrependReactor("patch", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	deleted := false
	client.PrependReactor("delete", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		deleted = true
		return false, nil, nil
	})
	discovery := &ResourceDiscovery{resourceMap: make(map[string]APIResource), gvrMap: make(map[string]schema.GroupVersionResource)}
	discovery.AddAPIResource(APIResource{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application", Name: "applications", Namespaced: true, IsCRD: true, Verbs: []string{"delete", "patch"}})

	err := NewWorkloadManager(client, discovery).DeleteResource(context.Background(), DeleteResourceOptions{Kind: "Application", Group: "argoproj.io", Namespace: "argocd", Name: "shop", Force: true})
	if err == nil || deleted {
		t.Fatalf("err=%v deleted=%v; want an error and no delete", err, deleted)
	}
}
