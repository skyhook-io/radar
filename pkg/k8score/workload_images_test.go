package k8score

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func imageWorkload(apiVersion, kind, namespace, name string, containers, initContainers []any) *unstructured.Unstructured {
	spec := map[string]any{"containers": containers}
	if initContainers != nil {
		spec["initContainers"] = initContainers
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
			"labels":    map[string]any{"preserve": "yes"},
		},
		"spec": map[string]any{
			"replicas": int64(3),
			"template": map[string]any{"spec": spec},
		},
	}}
}

func imageContainer(name, image string) map[string]any {
	return map[string]any{"name": name, "image": image}
}

func fakeImageClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
}

func TestGetWorkloadImagesInventoriesRegularAndInitContainers(t *testing.T) {
	deployment := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("app", "repo/app:v1"), imageContainer("sidecar", "repo/sidecar:v1")},
		[]any{imageContainer("migrate", "repo/migrate:v1")},
	)
	client := fakeImageClient(deployment)

	inventory, err := NewWorkloadManager(client, nil).GetWorkloadImages(context.Background(), "deployments", "prod", "web")
	if err != nil {
		t.Fatalf("GetWorkloadImages: %v", err)
	}
	if len(inventory.Containers) != 3 {
		t.Fatalf("containers = %v, want 3 entries", inventory.Containers)
	}
	if inventory.Containers[0] != (WorkloadContainerImage{Type: containerTypeRegular, Name: "app", Image: "repo/app:v1"}) {
		t.Errorf("first container = %+v", inventory.Containers[0])
	}
	if inventory.Containers[2].Type != containerTypeInit || inventory.Containers[2].Name != "migrate" {
		t.Errorf("init container = %+v", inventory.Containers[2])
	}
	if inventory.Target.Group != "apps" || inventory.Target.Resource != "deployments" || inventory.Target.Name != "web" {
		t.Errorf("target = %+v", inventory.Target)
	}
	if inventory.Behavior.Type != "rolling" {
		t.Errorf("behavior = %+v, want rolling", inventory.Behavior)
	}
}

func TestSetWorkloadImagesBuildsOneAtomicNarrowPatch(t *testing.T) {
	deployment := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("app", "repo/app:v1"), imageContainer("sidecar", "repo/sidecar:v1")},
		[]any{imageContainer("migrate", "repo/migrate:v1")},
	)
	client := fakeImageClient(deployment)
	updated := deployment.DeepCopy()
	updated.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "test"}})
	updated.SetAnnotations(map[string]string{lastAppliedAnnotation: `{"kind":"Deployment"}`, "preserve": "yes"})
	_ = unstructured.SetNestedSlice(updated.Object,
		[]any{imageContainer("app", "repo/app:v2"), imageContainer("sidecar", "repo/sidecar:v1")},
		"spec", "template", "spec", "containers")
	_ = unstructured.SetNestedSlice(updated.Object,
		[]any{imageContainer("migrate", "repo/migrate:v2")},
		"spec", "template", "spec", "initContainers")
	var captured clienttesting.PatchAction
	client.PrependReactor("patch", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		captured = action.(clienttesting.PatchAction)
		return true, updated, nil
	})

	result, err := NewWorkloadManager(client, nil).SetWorkloadImages(context.Background(), "Deployment", "prod", "web", []WorkloadImageUpdate{
		{Type: containerTypeRegular, Name: "app", PreviousImage: "repo/app:v1", Image: "repo/app:v2"},
		{Type: containerTypeInit, Name: "migrate", PreviousImage: "repo/migrate:v1", Image: "repo/migrate:v2"},
	})
	if err != nil {
		t.Fatalf("SetWorkloadImages: %v", err)
	}
	if captured == nil {
		t.Fatal("no patch was issued")
	}
	if captured.GetPatchType() != "application/json-patch+json" {
		t.Errorf("patch type = %s", captured.GetPatchType())
	}
	var operations []map[string]any
	if err := json.Unmarshal(captured.GetPatch(), &operations); err != nil {
		t.Fatalf("patch JSON: %v", err)
	}
	if len(operations) != 6 {
		t.Fatalf("operations = %v, want 6", operations)
	}
	if operations[0]["op"] != "test" || operations[0]["path"] != "/spec/template/spec/containers/0/name" {
		t.Errorf("first operation = %v", operations[0])
	}
	if operations[2]["op"] != "replace" || operations[2]["path"] != "/spec/template/spec/containers/0/image" {
		t.Errorf("regular replacement = %v", operations[2])
	}
	if operations[5]["path"] != "/spec/template/spec/initContainers/0/image" {
		t.Errorf("init replacement = %v", operations[5])
	}
	if result.Containers[0].Image != "repo/app:v2" || result.Containers[2].Image != "repo/migrate:v2" {
		t.Errorf("result inventory = %+v", result.Containers)
	}
	if result.Object.GetLabels()["preserve"] != "yes" {
		t.Errorf("unrelated label was lost: %v", result.Object.GetLabels())
	}
	if len(result.Object.GetManagedFields()) != 0 {
		t.Errorf("managed fields leaked into result: %v", result.Object.GetManagedFields())
	}
	if _, found := result.Object.GetAnnotations()[lastAppliedAnnotation]; found {
		t.Errorf("last-applied annotation leaked into result: %v", result.Object.GetAnnotations())
	}
	if result.Object.GetAnnotations()["preserve"] != "yes" {
		t.Errorf("unrelated annotation was lost: %v", result.Object.GetAnnotations())
	}
}

func TestSetWorkloadImagesRejectsStalePreviousImageBeforePatching(t *testing.T) {
	deployment := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("app", "repo/app:v2")}, nil)
	client := fakeImageClient(deployment)

	_, err := NewWorkloadManager(client, nil).SetWorkloadImages(context.Background(), "deployments", "prod", "web", []WorkloadImageUpdate{{
		Type: containerTypeRegular, Name: "app", PreviousImage: "repo/app:v1", Image: "repo/app:v3",
	}})
	if !apierrors.IsConflict(err) {
		t.Fatalf("error = %v, want Conflict", err)
	}
	for _, action := range client.Actions() {
		if _, ok := action.(clienttesting.PatchAction); ok {
			t.Fatalf("stale request issued a patch: %v", client.Actions())
		}
	}
}

func TestSetWorkloadImagesRetriesWhenContainerOrderChanges(t *testing.T) {
	initial := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("app", "repo/app:v1"), imageContainer("sidecar", "repo/sidecar:v1")}, nil)
	reordered := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("sidecar", "repo/sidecar:v1"), imageContainer("app", "repo/app:v1")}, nil)
	updated := reordered.DeepCopy()
	_ = unstructured.SetNestedSlice(updated.Object,
		[]any{imageContainer("sidecar", "repo/sidecar:v1"), imageContainer("app", "repo/app:v2")},
		"spec", "template", "spec", "containers")
	client := fakeImageClient(initial)
	getCount := 0
	client.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		getCount++
		if getCount == 1 {
			return true, initial.DeepCopy(), nil
		}
		return true, reordered.DeepCopy(), nil
	})
	patchCount := 0
	client.PrependReactor("patch", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patchCount++
		if patchCount == 1 {
			return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{
				field.Invalid(field.NewPath("spec", "template", "spec", "containers"), nil, "JSON Patch test failed"),
			})
		}
		var operations []map[string]any
		_ = json.Unmarshal(action.(clienttesting.PatchAction).GetPatch(), &operations)
		if operations[0]["path"] != "/spec/template/spec/containers/1/name" {
			t.Errorf("retry did not rebuild index: %v", operations)
		}
		return true, updated, nil
	})

	result, err := NewWorkloadManager(client, nil).SetWorkloadImages(context.Background(), "deployments", "prod", "web", []WorkloadImageUpdate{{
		Type: containerTypeRegular, Name: "app", PreviousImage: "repo/app:v1", Image: "repo/app:v2",
	}})
	if err != nil {
		t.Fatalf("SetWorkloadImages: %v", err)
	}
	if patchCount != 2 {
		t.Fatalf("patch count = %d, want 2", patchCount)
	}
	if result.Containers[1].Image != "repo/app:v2" {
		t.Errorf("result = %+v", result.Containers)
	}
}

func TestSetWorkloadImagesReturnsConflictWhenImageChangesDuringPatch(t *testing.T) {
	initial := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("app", "repo/app:v1")}, nil)
	changed := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("app", "repo/app:v2")}, nil)
	client := fakeImageClient(initial)
	getCount := 0
	client.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		getCount++
		if getCount == 1 {
			return true, initial.DeepCopy(), nil
		}
		return true, changed.DeepCopy(), nil
	})
	client.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{
			field.Invalid(field.NewPath("spec", "template", "spec", "containers", "0", "image"), nil, "JSON Patch test failed"),
		})
	})

	_, err := NewWorkloadManager(client, nil).SetWorkloadImages(context.Background(), "deployments", "prod", "web", []WorkloadImageUpdate{{
		Type: containerTypeRegular, Name: "app", PreviousImage: "repo/app:v1", Image: "repo/app:v3",
	}})
	if !apierrors.IsConflict(err) {
		t.Fatalf("error = %v, want Conflict", err)
	}
	patches := 0
	for _, action := range client.Actions() {
		if _, ok := action.(clienttesting.PatchAction); ok {
			patches++
		}
	}
	if patches != 1 {
		t.Fatalf("patch count = %d, want no retry after the image changed", patches)
	}
}

func TestSetWorkloadImagesDetectsTerminationAfterRejectedPatch(t *testing.T) {
	tests := []struct {
		name           string
		getResponses   []*unstructured.Unstructured
		wantPatchCount int
	}{
		{
			name: "before retry",
			getResponses: []*unstructured.Unstructured{
				imageWorkload("apps/v1", "Deployment", "prod", "web", []any{imageContainer("app", "repo/app:v1")}, nil),
				imageWorkload("apps/v1", "Deployment", "prod", "web", []any{imageContainer("app", "repo/app:v1")}, nil),
			},
			wantPatchCount: 1,
		},
		{
			name: "after retry",
			getResponses: []*unstructured.Unstructured{
				imageWorkload("apps/v1", "Deployment", "prod", "web", []any{imageContainer("app", "repo/app:v1"), imageContainer("sidecar", "repo/sidecar:v1")}, nil),
				imageWorkload("apps/v1", "Deployment", "prod", "web", []any{imageContainer("sidecar", "repo/sidecar:v1"), imageContainer("app", "repo/app:v1")}, nil),
				imageWorkload("apps/v1", "Deployment", "prod", "web", []any{imageContainer("sidecar", "repo/sidecar:v1"), imageContainer("app", "repo/app:v1")}, nil),
			},
			wantPatchCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := metav1.Now()
			tt.getResponses[len(tt.getResponses)-1].SetDeletionTimestamp(&now)
			client := fakeImageClient(tt.getResponses[0])
			getCount := 0
			client.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
				response := tt.getResponses[getCount]
				getCount++
				return true, response.DeepCopy(), nil
			})
			patchCount := 0
			client.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
				patchCount++
				return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{
					field.Invalid(field.NewPath("spec", "template", "spec", "containers"), nil, "JSON Patch test failed"),
				})
			})

			_, err := NewWorkloadManager(client, nil).SetWorkloadImages(context.Background(), "deployments", "prod", "web", []WorkloadImageUpdate{{
				Type: containerTypeRegular, Name: "app", PreviousImage: "repo/app:v1", Image: "repo/app:v2",
			}})
			if !errors.Is(err, ErrImageWorkloadTerminating) {
				t.Fatalf("error = %v, want ErrImageWorkloadTerminating", err)
			}
			if patchCount != tt.wantPatchCount {
				t.Fatalf("patch count = %d, want %d", patchCount, tt.wantPatchCount)
			}
		})
	}
}

func TestSetWorkloadImagesPreservesUnrelatedInvalidErrors(t *testing.T) {
	deployment := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("app", "repo/app:v1")}, nil)
	client := fakeImageClient(deployment)
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{
		field.Forbidden(field.NewPath("spec", "template"), "admission rejected the image"),
	})
	client.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, invalid
	})

	_, err := NewWorkloadManager(client, nil).SetWorkloadImages(context.Background(), "deployments", "prod", "web", []WorkloadImageUpdate{{
		Type: containerTypeRegular, Name: "app", PreviousImage: "repo/app:v1", Image: "repo/app:v2",
	}})
	if !apierrors.IsInvalid(err) {
		t.Fatalf("error = %v, want original Invalid", err)
	}
	patches := 0
	for _, action := range client.Actions() {
		if _, ok := action.(clienttesting.PatchAction); ok {
			patches++
		}
	}
	if patches != 1 {
		t.Fatalf("patch count = %d, want no retry", patches)
	}
}

func TestSetWorkloadImagesKeepsAdmissionErrorAccurateAfterReorderRetry(t *testing.T) {
	initial := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("app", "repo/app:v1"), imageContainer("sidecar", "repo/sidecar:v1")}, nil)
	reordered := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("sidecar", "repo/sidecar:v1"), imageContainer("app", "repo/app:v1")}, nil)
	client := fakeImageClient(initial)
	getCount := 0
	client.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		getCount++
		if getCount == 1 {
			return true, initial.DeepCopy(), nil
		}
		return true, reordered.DeepCopy(), nil
	})
	patchCount := 0
	client.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		patchCount++
		if patchCount == 1 {
			return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{
				field.Invalid(field.NewPath("spec", "template", "spec", "containers"), nil, "JSON Patch test failed"),
			})
		}
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "web", field.ErrorList{
			field.Forbidden(field.NewPath("spec", "template"), "admission rejected the image"),
		})
	})

	_, err := NewWorkloadManager(client, nil).SetWorkloadImages(context.Background(), "deployments", "prod", "web", []WorkloadImageUpdate{{
		Type: containerTypeRegular, Name: "app", PreviousImage: "repo/app:v1", Image: "repo/app:v2",
	}})
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "admission rejected the image") {
		t.Fatalf("error = %v, want admission Invalid", err)
	}
	if strings.Contains(err.Error(), "container order") {
		t.Fatalf("error blamed the resolved reorder: %v", err)
	}
	if patchCount != 2 {
		t.Fatalf("patch count = %d, want one bounded retry", patchCount)
	}
}

func TestSetWorkloadImagesFollowsRolloutWorkloadRef(t *testing.T) {
	rollout := imageWorkload("argoproj.io/v1alpha1", "Rollout", "prod", "web", nil, nil)
	unstructured.RemoveNestedField(rollout.Object, "spec", "template")
	_ = unstructured.SetNestedMap(rollout.Object, map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web-target"}, "spec", "workloadRef")
	_ = unstructured.SetNestedMap(rollout.Object, map[string]any{"canary": map[string]any{}}, "spec", "strategy")
	deployment := imageWorkload("apps/v1", "Deployment", "prod", "web-target",
		[]any{imageContainer("app", "repo/app:v1")}, nil)
	updated := deployment.DeepCopy()
	_ = unstructured.SetNestedSlice(updated.Object, []any{imageContainer("app", "repo/app:v2")}, "spec", "template", "spec", "containers")
	client := fakeImageClient(rollout, deployment)
	client.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, updated, nil
	})

	result, err := NewWorkloadManager(client, nil).SetWorkloadImages(context.Background(), "rollouts", "prod", "web", []WorkloadImageUpdate{{
		Type: containerTypeRegular, Name: "app", PreviousImage: "repo/app:v1", Image: "repo/app:v2",
	}})
	if err != nil {
		t.Fatalf("SetWorkloadImages: %v", err)
	}
	if result.Target.Resource != "deployments" || result.Target.Name != "web-target" {
		t.Errorf("target = %+v", result.Target)
	}
	if result.Behavior.Type != "canary" {
		t.Errorf("behavior = %+v, want canary", result.Behavior)
	}
}

func TestClassifyWorkloadUpdateBehavior(t *testing.T) {
	stateful := imageWorkload("apps/v1", "StatefulSet", "prod", "db", []any{imageContainer("db", "db:v1")}, nil)
	_ = unstructured.SetNestedField(stateful.Object, "RollingUpdate", "spec", "updateStrategy", "type")
	_ = unstructured.SetNestedField(stateful.Object, int64(2), "spec", "updateStrategy", "rollingUpdate", "partition")
	behavior := classifyWorkloadUpdateBehavior("statefulsets", stateful)
	if behavior.Type != "partitioned" || behavior.Partition == nil || *behavior.Partition != 2 {
		t.Errorf("partitioned behavior = %+v", behavior)
	}

	daemonSet := imageWorkload("apps/v1", "DaemonSet", "prod", "agent", []any{imageContainer("agent", "agent:v1")}, nil)
	_ = unstructured.SetNestedField(daemonSet.Object, "OnDelete", "spec", "updateStrategy", "type")
	if got := classifyWorkloadUpdateBehavior("daemonsets", daemonSet).Type; got != "onDelete" {
		t.Errorf("DaemonSet behavior = %q", got)
	}

	deployment := imageWorkload("apps/v1", "Deployment", "prod", "web", []any{imageContainer("app", "app:v1")}, nil)
	_ = unstructured.SetNestedField(deployment.Object, true, "spec", "paused")
	if got := classifyWorkloadUpdateBehavior("deployments", deployment).Type; got != "paused" {
		t.Errorf("Deployment behavior = %q", got)
	}

	rollout := imageWorkload("argoproj.io/v1alpha1", "Rollout", "prod", "web", []any{imageContainer("app", "app:v1")}, nil)
	_ = unstructured.SetNestedMap(rollout.Object, map[string]any{"canary": map[string]any{}}, "spec", "strategy")
	_ = unstructured.SetNestedField(rollout.Object, true, "spec", "paused")
	if got := classifyWorkloadUpdateBehavior("rollouts", rollout).Type; got != "paused" {
		t.Errorf("paused Rollout behavior = %q", got)
	}

	unstructured.RemoveNestedField(rollout.Object, "spec", "paused")
	canary := classifyWorkloadUpdateBehavior("rollouts", rollout)
	if canary.Type != "canary" || canary.Gated == nil || *canary.Gated {
		t.Errorf("ungated canary behavior = %+v", canary)
	}
	_ = unstructured.SetNestedSlice(rollout.Object, []any{map[string]any{"setWeight": int64(20)}}, "spec", "strategy", "canary", "steps")
	canary = classifyWorkloadUpdateBehavior("rollouts", rollout)
	if canary.Gated == nil || !*canary.Gated {
		t.Errorf("gated canary behavior = %+v", canary)
	}

	_ = unstructured.SetNestedMap(rollout.Object, map[string]any{"blueGreen": map[string]any{}}, "spec", "strategy")
	blueGreen := classifyWorkloadUpdateBehavior("rollouts", rollout)
	if blueGreen.Type != "blueGreen" || blueGreen.AutoPromote == nil || !*blueGreen.AutoPromote {
		t.Errorf("default blue-green behavior = %+v", blueGreen)
	}
	_ = unstructured.SetNestedField(rollout.Object, false, "spec", "strategy", "blueGreen", "autoPromotionEnabled")
	blueGreen = classifyWorkloadUpdateBehavior("rollouts", rollout)
	if blueGreen.AutoPromote == nil || *blueGreen.AutoPromote {
		t.Errorf("manual blue-green behavior = %+v", blueGreen)
	}

	unstructured.RemoveNestedField(rollout.Object, "spec", "strategy")
	if got := classifyWorkloadUpdateBehavior("rollouts", rollout).Type; got != "rolling" {
		t.Errorf("Rollout without a strategy behavior = %q", got)
	}
}

func TestGetWorkloadImagesRejectsTerminatingRootAndReferencedTarget(t *testing.T) {
	now := metav1.Now()
	terminatingDeployment := imageWorkload("apps/v1", "Deployment", "prod", "web",
		[]any{imageContainer("app", "app:v1")}, nil)
	terminatingDeployment.SetDeletionTimestamp(&now)
	_, err := NewWorkloadManager(fakeImageClient(terminatingDeployment), nil).GetWorkloadImages(context.Background(), "deployments", "prod", "web")
	if !errors.Is(err, ErrImageWorkloadTerminating) {
		t.Fatalf("terminating root error = %v", err)
	}

	rollout := imageWorkload("argoproj.io/v1alpha1", "Rollout", "prod", "rollout", nil, nil)
	unstructured.RemoveNestedField(rollout.Object, "spec", "template")
	_ = unstructured.SetNestedMap(rollout.Object, map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web"}, "spec", "workloadRef")
	_, err = NewWorkloadManager(fakeImageClient(rollout, terminatingDeployment), nil).GetWorkloadImages(context.Background(), "rollouts", "prod", "rollout")
	if !errors.Is(err, ErrImageWorkloadTerminating) {
		t.Fatalf("terminating referenced target error = %v", err)
	}
}

func TestWorkloadImageTargetForRollout(t *testing.T) {
	inline := imageWorkload("argoproj.io/v1alpha1", "Rollout", "prod", "web", []any{imageContainer("app", "app:v1")}, nil)
	group, resource, needsGet, supported := WorkloadImageTargetForRollout(inline)
	if group != "argoproj.io" || resource != "rollouts" || needsGet || !supported {
		t.Errorf("inline target = (%q, %q, %v, %v)", group, resource, needsGet, supported)
	}

	ref := inline.DeepCopy()
	unstructured.RemoveNestedField(ref.Object, "spec", "template")
	_ = unstructured.SetNestedMap(ref.Object, map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web-target"}, "spec", "workloadRef")
	group, resource, needsGet, supported = WorkloadImageTargetForRollout(ref)
	if group != "apps" || resource != "deployments" || !needsGet || !supported {
		t.Errorf("workloadRef target = (%q, %q, %v, %v)", group, resource, needsGet, supported)
	}

	_ = unstructured.SetNestedMap(ref.Object, map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "web-target"}, "spec", "workloadRef")
	_, _, _, supported = WorkloadImageTargetForRollout(ref)
	if supported {
		t.Error("unsupported workloadRef reported as supported")
	}
}

func TestValidateImageUpdates(t *testing.T) {
	tests := []struct {
		name    string
		updates []WorkloadImageUpdate
	}{
		{"empty", nil},
		{"unknown type", []WorkloadImageUpdate{{Type: "ephemeralContainer", Name: "debug", PreviousImage: "a", Image: "b"}}},
		{"missing image", []WorkloadImageUpdate{{Type: containerTypeRegular, Name: "app", PreviousImage: "a"}}},
		{"unchanged", []WorkloadImageUpdate{{Type: containerTypeRegular, Name: "app", PreviousImage: "a", Image: "a"}}},
		{"duplicate", []WorkloadImageUpdate{
			{Type: containerTypeRegular, Name: "app", PreviousImage: "a", Image: "b"},
			{Type: containerTypeRegular, Name: "app", PreviousImage: "a", Image: "c"},
		}},
		{"too many", func() []WorkloadImageUpdate {
			updates := make([]WorkloadImageUpdate, 65)
			for i := range updates {
				updates[i] = WorkloadImageUpdate{Type: containerTypeRegular, Name: fmt.Sprintf("app-%d", i), PreviousImage: "a", Image: "b"}
			}
			return updates
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateImageUpdates(tc.updates); !errors.Is(err, ErrInvalidImageUpdate) {
				t.Fatalf("error = %v, want ErrInvalidImageUpdate", err)
			}
		})
	}
}
