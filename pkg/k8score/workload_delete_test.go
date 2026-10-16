package k8score

import (
	"context"
	"testing"

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
