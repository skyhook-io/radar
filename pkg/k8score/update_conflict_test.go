package k8score

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

var deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

func deployment(namespace, name, resourceVersion string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"namespace":       namespace,
			"name":            name,
			"resourceVersion": resourceVersion,
		},
		"spec": map[string]any{"replicas": int64(1)},
	}}
}

// The RAD-604 repro: the editor reviewed version 1, a controller wrote the
// object while the review was open, and the apply carries the reviewed version.
// The write must be refused before it lands, as a conflict — the class the UI
// answers by refreshing the review rather than by asking the user to fix their
// YAML.
func TestPatchUpdateResource_ReviewedVersionWentStale(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{deploymentGVR: "DeploymentList"},
		deployment("shop", "bubble-relay", "2"),
	)
	m := NewWorkloadManager(client, nil)
	ri := client.Resource(deploymentGVR).Namespace("shop")

	_, _, err := m.patchUpdateResource(
		context.Background(),
		UpdateResourceOptions{Kind: "Deployment", Namespace: "shop", Name: "bubble-relay", ExpectedResourceVersion: "1"},
		deployment("shop", "bubble-relay", "1"),
		deploymentGVR,
		ri,
	)
	if err == nil {
		t.Fatal("stale reviewed version was accepted")
	}
	if !apierrors.IsConflict(err) {
		t.Fatalf("err = %v, want a Conflict (the handler maps only those to 409)", err)
	}
	// The message is what the user reads under the diff, so it has to name the
	// cause rather than the symptom.
	if !strings.Contains(err.Error(), "changed after review") {
		t.Errorf("err = %q, want it to say the resource changed after review", err)
	}
}

// With no review to go stale (the plain save path) the version check is skipped
// entirely — it must not invent a conflict out of an unread resourceVersion.
func TestPatchUpdateResource_NoReviewedVersionSkipsTheCheck(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{deploymentGVR: "DeploymentList"},
		deployment("shop", "bubble-relay", "2"),
	)
	m := NewWorkloadManager(client, nil)
	ri := client.Resource(deploymentGVR).Namespace("shop")

	_, _, err := m.patchUpdateResource(
		context.Background(),
		UpdateResourceOptions{Kind: "Deployment", Namespace: "shop", Name: "bubble-relay"},
		deployment("shop", "bubble-relay", "1"),
		deploymentGVR,
		ri,
	)
	if err != nil && apierrors.IsConflict(err) {
		t.Fatalf("unreviewed save reported a conflict: %v", err)
	}
}
