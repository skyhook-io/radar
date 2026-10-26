package k8score

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// TestDeleteResourceSendsBackgroundPropagation pins the delete request to
// kubectl's: an explicit background policy, so a Job's Pods go with it
// instead of being orphaned by the API server's legacy default for Jobs.
func TestDeleteResourceSendsBackgroundPropagation(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	for _, force := range []bool{false, true} {
		disc, err := NewResourceDiscovery(fake.NewSimpleClientset().Discovery())
		if err != nil {
			t.Fatal(err)
		}
		disc.AddAPIResource(APIResource{Group: "batch", Version: "v1", Kind: "Job", Name: "jobs", Namespaced: true, Verbs: []string{"delete"}})
		job := &unstructured.Unstructured{}
		job.SetAPIVersion("batch/v1")
		job.SetKind("Job")
		job.SetNamespace("team")
		job.SetName("nightly")
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "JobList"}, job)
		var deletes []clienttesting.DeleteActionImpl
		dyn.PrependReactor("delete", "jobs", func(a clienttesting.Action) (bool, runtime.Object, error) {
			deletes = append(deletes, a.(clienttesting.DeleteActionImpl))
			return false, nil, nil
		})

		err = NewWorkloadManager(dyn, disc).DeleteResource(context.Background(), DeleteResourceOptions{Kind: "Job", Group: "batch", Namespace: "team", Name: "nightly", Force: force})
		if err != nil {
			t.Fatalf("force=%v: %v", force, err)
		}
		if len(deletes) != 1 {
			t.Fatalf("force=%v: %d delete requests, want 1", force, len(deletes))
		}
		got := deletes[0].DeleteOptions
		if got.PropagationPolicy == nil || *got.PropagationPolicy != metav1.DeletePropagationBackground {
			t.Errorf("force=%v: propagationPolicy = %v, want Background", force, got.PropagationPolicy)
		}
		if force != (got.GracePeriodSeconds != nil && *got.GracePeriodSeconds == 0) {
			t.Errorf("force=%v: gracePeriodSeconds = %v", force, got.GracePeriodSeconds)
		}
	}
}

func TestDeleteResourcePreservesPendingDeletionPolicy(t *testing.T) {
	disc, err := NewResourceDiscovery(fake.NewSimpleClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	disc.AddAPIResource(APIResource{Group: "batch", Version: "v1", Kind: "Job", Name: "jobs", Namespaced: true, Verbs: []string{"delete"}})
	for _, tt := range []struct {
		name           string
		finalizer      string
		terminating    bool
		wantBackground bool
	}{
		{name: "terminating foreground", finalizer: metav1.FinalizerDeleteDependents, terminating: true},
		{name: "terminating orphan", finalizer: metav1.FinalizerOrphanDependents, terminating: true},
		{name: "terminating custom finalizer uses background", finalizer: "example.com/cleanup", terminating: true, wantBackground: true},
		{name: "terminating without finalizers uses background", terminating: true, wantBackground: true},
		{name: "live orphan", finalizer: metav1.FinalizerOrphanDependents},
	} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%v", tt.name, force), func(t *testing.T) {
				job := &unstructured.Unstructured{}
				job.SetAPIVersion("batch/v1")
				job.SetKind("Job")
				job.SetNamespace("team")
				job.SetName("nightly")
				if tt.terminating {
					now := metav1.Now()
					job.SetDeletionTimestamp(&now)
				}
				if tt.finalizer != "" {
					job.SetFinalizers([]string{tt.finalizer})
				}
				dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), job)
				var deletes []clienttesting.DeleteActionImpl
				dyn.PrependReactor("delete", "jobs", func(a clienttesting.Action) (bool, runtime.Object, error) {
					deletes = append(deletes, a.(clienttesting.DeleteActionImpl))
					return false, nil, nil
				})

				err := NewWorkloadManager(dyn, disc).DeleteResource(context.Background(), DeleteResourceOptions{
					Kind: "Job", Group: "batch", Namespace: "team", Name: "nightly", Force: force,
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(deletes) != 1 {
					t.Fatalf("%d delete requests, want 1", len(deletes))
				}
				got := deletes[0].DeleteOptions
				if tt.wantBackground || force {
					if got.PropagationPolicy == nil || *got.PropagationPolicy != metav1.DeletePropagationBackground {
						t.Errorf("propagationPolicy = %v, want Background", got.PropagationPolicy)
					}
				} else if got.PropagationPolicy != nil {
					t.Errorf("propagationPolicy = %v, want nil", *got.PropagationPolicy)
				}
				if force {
					if got.GracePeriodSeconds == nil || *got.GracePeriodSeconds != 0 {
						t.Errorf("gracePeriodSeconds = %v, want 0", got.GracePeriodSeconds)
					}
				} else if got.GracePeriodSeconds != nil {
					t.Errorf("gracePeriodSeconds = %v, want nil", got.GracePeriodSeconds)
				}
			})
		}
	}
}

func TestDeleteResourceReadFailureDoesNotBlockDelete(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	disc, err := NewResourceDiscovery(fake.NewSimpleClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	disc.AddAPIResource(APIResource{Group: "batch", Version: "v1", Kind: "Job", Name: "jobs", Namespaced: true, Verbs: []string{"delete"}})
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"forbidden", apierrors.NewForbidden(gvr.GroupResource(), "nightly", errors.New("read denied"))},
		{"not found", apierrors.NewNotFound(gvr.GroupResource(), "nightly")},
		{"other", errors.New("read failed")},
	} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%v", tt.name, force), func(t *testing.T) {
				job := &unstructured.Unstructured{}
				job.SetAPIVersion("batch/v1")
				job.SetKind("Job")
				job.SetNamespace("team")
				job.SetName("nightly")
				dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), job)
				reads := 0
				dyn.PrependReactor("get", "jobs", func(a clienttesting.Action) (bool, runtime.Object, error) {
					reads++
					return true, nil, tt.err
				})
				var deletes []clienttesting.DeleteActionImpl
				dyn.PrependReactor("delete", "jobs", func(a clienttesting.Action) (bool, runtime.Object, error) {
					if reads == 0 {
						t.Error("delete sent without attempting a read")
					}
					deletes = append(deletes, a.(clienttesting.DeleteActionImpl))
					return false, nil, nil
				})

				err := NewWorkloadManager(dyn, disc).DeleteResource(context.Background(), DeleteResourceOptions{
					Kind: "Job", Group: "batch", Namespace: "team", Name: "nightly", Force: force,
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(deletes) != 1 {
					t.Fatalf("%d delete requests, want 1", len(deletes))
				}
				got := deletes[0].DeleteOptions
				if got.PropagationPolicy == nil || *got.PropagationPolicy != metav1.DeletePropagationBackground {
					t.Errorf("propagationPolicy = %v, want Background", got.PropagationPolicy)
				}
			})
		}
	}
}

func TestForceDeleteStopsWhenFinalizersCannotBeStripped(t *testing.T) {
	apps := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"namespace": "argocd", "name": "shop", "finalizers": []any{"resources-finalizer.argocd.argoproj.io"}},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{apps: "ApplicationList"}, app)
	client.PrependReactor("patch", "applications", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	deleted := false
	client.PrependReactor("delete", "applications", func(clienttesting.Action) (bool, runtime.Object, error) {
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

func TestForceDeleteSkipsTheFinalizerPatchWhenThereAreNone(t *testing.T) {
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"namespace": "team", "name": "stuck"},
	}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{pods: "PodList"}, pod)
	client.PrependReactor("patch", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("admission webhook denied the request"))
	})
	discovery := &ResourceDiscovery{resourceMap: make(map[string]APIResource), gvrMap: make(map[string]schema.GroupVersionResource)}
	discovery.AddAPIResource(APIResource{Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: []string{"delete", "patch", "get"}})

	if err := NewWorkloadManager(client, discovery).DeleteResource(context.Background(), DeleteResourceOptions{Kind: "Pod", Namespace: "team", Name: "stuck", Force: true}); err != nil {
		t.Fatalf("force delete of a Pod without finalizers failed on a rejected patch: %v", err)
	}
}

func TestDeleteResourceBindsObservedUID(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	for _, force := range []bool{false, true} {
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "demo", "uid": "original", "resourceVersion": "1"}}}
		if force {
			obj.SetFinalizers([]string{"hold"})
		}
		dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
		dyn.PrependReactor("delete", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
			opts := a.(clienttesting.DeleteAction).GetDeleteOptions()
			if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "original" {
				t.Fatalf("unbound delete: %+v", opts)
			}
			return false, nil, nil
		})
		dyn.PrependReactor("patch", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
			var patch map[string]any
			if err := json.Unmarshal(a.(clienttesting.PatchAction).GetPatch(), &patch); err != nil {
				t.Fatal(err)
			}
			m := patch["metadata"].(map[string]any)
			if m["uid"] != "original" || m["resourceVersion"] != "1" {
				t.Fatalf("unbound finalizer patch: %+v", m)
			}
			return false, nil, nil
		})
		discovery := stubDiscovery(t, "Deployment", gvr)
		discovery.AddAPIResource(APIResource{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true})
		if err := NewWorkloadManager(dyn, discovery).DeleteResource(context.Background(), DeleteResourceOptions{Kind: "Deployment", Group: "apps", Namespace: "demo", Name: "web", Force: force}); err != nil {
			t.Fatal(err)
		}
	}
}
