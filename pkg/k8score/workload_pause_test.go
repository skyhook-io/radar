package k8score

import (
	"context"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func withPaused(paused bool) func(map[string]any) {
	return func(o map[string]any) { o["spec"].(map[string]any)["paused"] = paused }
}

func TestSetDeploymentPaused(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(map[string]any)
		paused    bool
		wantPatch string
	}{
		{"pause sets true", nil, true, `{"spec":{"paused":true}}`},
		{"resume sets false", withPaused(true), false, `{"spec":{"paused":false}}`},
		{"pausing a paused Deployment stays paused", withPaused(true), true, `{"spec":{"paused":true}}`},
		{"resuming an unpaused Deployment stays unpaused", withPaused(false), false, `{"spec":{"paused":false}}`},
		{"resuming when spec.paused is unset stays unpaused", nil, false, `{"spec":{"paused":false}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newAppsClient(deploymentObject(podTemplateV1(), nil, tc.mutate))

			err := NewWorkloadManager(client, appsDiscovery()).SetDeploymentPaused(context.Background(), "prod", "web", tc.paused)
			if err != nil {
				t.Fatalf("SetDeploymentPaused: %v", err)
			}

			patches := patchActions(client)
			if len(patches) != 1 {
				t.Fatalf("issued %d patches, want 1", len(patches))
			}
			if got := patches[0].GetPatchType(); got != types.MergePatchType {
				t.Errorf("patch type = %s, want %s", got, types.MergePatchType)
			}
			if got := string(patches[0].GetPatch()); got != tc.wantPatch {
				t.Errorf("patch = %s, want %s", got, tc.wantPatch)
			}

			d := getDeployment(t, client)
			got, found, err := unstructured.NestedBool(d.Object, "spec", "paused")
			if err != nil || !found {
				t.Fatalf("spec.paused found=%v err=%v", found, err)
			}
			if got != tc.paused {
				t.Errorf("spec.paused = %v, want %v", got, tc.paused)
			}
		})
	}
}

func TestSetDeploymentPausedKeepsTheRestOfTheSpec(t *testing.T) {
	client := newAppsClient(deploymentObject(podTemplateV2(), nil, nil))

	if err := NewWorkloadManager(client, appsDiscovery()).SetDeploymentPaused(context.Background(), "prod", "web", true); err != nil {
		t.Fatalf("SetDeploymentPaused: %v", err)
	}

	spec := getDeployment(t, client).Object["spec"].(map[string]any)
	if !reflect.DeepEqual(spec["template"], podTemplateV2()) {
		t.Errorf("template changed: %v", spec["template"])
	}
	if spec["replicas"] != int64(2) {
		t.Errorf("replicas = %v, want 2", spec["replicas"])
	}
}

func TestSetDeploymentPausedMissingDeploymentIsNotFound(t *testing.T) {
	client := newAppsClient()

	err := NewWorkloadManager(client, appsDiscovery()).SetDeploymentPaused(context.Background(), "prod", "web", true)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("want NotFound through the wrap, got %v", err)
	}
}
