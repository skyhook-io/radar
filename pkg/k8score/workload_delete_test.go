package k8score

import (
	"context"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestDeleteResourceAcceptsPendingFinalizers(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "held", "namespace": "demo"}}}
	obj.SetFinalizers([]string{"example.com/cleanup"})
	now := metav1.Now()
	obj.SetDeletionTimestamp(&now)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	dyn.PrependReactor("delete", "configmaps", func(a clienttesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
	mgr := NewWorkloadManager(dyn, stubDiscovery(t, "ConfigMap", gvr))
	if err := mgr.DeleteResource(context.Background(), DeleteResourceOptions{Kind: "ConfigMap", Namespace: "demo", Name: "held"}); err != nil {
		t.Fatalf("accepted deletion returned error: %v", err)
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "patch" {
			t.Fatal("normal delete stripped finalizers")
		}
	}
}

func TestDeleteResourceResultObservesWithoutFailingAcceptedDelete(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	for _, tc := range []struct {
		name      string
		readError error
	}{
		{name: "pending finalizers"},
		{name: "read forbidden", readError: apierrors.NewForbidden(gvr.GroupResource(), "held", fmt.Errorf("no get permission"))},
		{name: "already gone", readError: apierrors.NewNotFound(gvr.GroupResource(), "held")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "held", "namespace": "demo"}}}
			now := metav1.Now()
			obj.SetDeletionTimestamp(&now)
			obj.SetFinalizers([]string{"example.com/cleanup"})
			dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
			dyn.PrependReactor("delete", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
			if tc.readError != nil {
				dyn.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, tc.readError })
			}
			result, err := NewWorkloadManager(dyn, stubDiscovery(t, "ConfigMap", gvr)).DeleteResourceWithResult(context.Background(), DeleteResourceOptions{Kind: "ConfigMap", Namespace: "demo", Name: "held"})
			if err != nil {
				t.Fatalf("accepted delete failed: %v", err)
			}
			if tc.readError == nil && (result.DeletionTimestamp == nil || len(result.PendingFinalizers) != 1) {
				t.Fatalf("missing observation: %+v", result)
			}
			if apierrors.IsForbidden(tc.readError) && result.ObservationError == "" {
				t.Fatal("missing read error observation")
			}
			if apierrors.IsNotFound(tc.readError) && (result.ObservationError != "" || result.DeletionTimestamp != nil) {
				t.Fatalf("gone observation: %+v", result)
			}
		})
	}
}

func TestObserveResourceDeletionIgnoresReplacement(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "held", "namespace": "demo", "uid": "replacement"}}}
	now := metav1.Now()
	obj.SetDeletionTimestamp(&now)
	obj.SetFinalizers([]string{"example.com/cleanup"})
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	result := ObserveResourceDeletion(context.Background(), dyn.Resource(gvr).Namespace("demo"), "held", "original")
	if result.DeletionTimestamp != nil || len(result.PendingFinalizers) > 0 {
		t.Fatalf("observed replacement: %+v", result)
	}
}
