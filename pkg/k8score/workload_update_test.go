package k8score

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

const clusterPolicyYAML = `
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: restrict-image-registries
  resourceVersion: "12345"
  uid: 5e8a1b2c-3d4f-5a6b-7c8d-9e0f1a2b3c4d
  managedFields:
  - manager: kyverno
    operation: Update
spec:
  validationFailureAction: Audit
status:
  ready: true
`

func newFakeDynamicWithClusterPolicy(t *testing.T) (*dynamicfake.FakeDynamicClient, schema.GroupVersionResource) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"}
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{gvr: "ClusterPolicyList"}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds), gvr
}

// stubDiscovery installs a single Kind→GVR mapping so UpdateResource skips the
// real discovery client. ResourceDiscovery's exported API doesn't allow direct
// seeding, but the unexported maps are package-internal — fine for tests.
func stubDiscovery(t *testing.T, kind string, gvr schema.GroupVersionResource) *ResourceDiscovery {
	t.Helper()
	rd := &ResourceDiscovery{
		resourceMap: map[string]APIResource{
			strings.ToLower(kind): {Kind: kind, Name: gvr.Resource, Group: gvr.Group, Version: gvr.Version, Namespaced: false},
			gvr.Resource:          {Kind: kind, Name: gvr.Resource, Group: gvr.Group, Version: gvr.Version, Namespaced: false},
		},
		gvrMap: map[string]schema.GroupVersionResource{
			strings.ToLower(kind): gvr,
			gvr.Resource:          gvr,
		},
	}
	return rd
}

// TestUpdateResource_UsesServerSideApply pins the SSA wire shape: PATCH with
// ApplyPatchType, FieldManager=radar, Force=true, and server-managed metadata
// stripped from the body. Editor flows submit YAML without a resourceVersion,
// which PUT rejects — SSA is the contract.
func TestUpdateResource_UsesServerSideApply(t *testing.T) {
	dyn, gvr := newFakeDynamicWithClusterPolicy(t)
	disc := stubDiscovery(t, "ClusterPolicy", gvr)
	mgr := NewWorkloadManager(dyn, disc)

	var captured clienttesting.PatchAction
	dyn.PrependReactor("patch", "clusterpolicies", func(a clienttesting.Action) (bool, runtime.Object, error) {
		captured = a.(clienttesting.PatchAction)
		// Return a minimal object so the call succeeds.
		return true, nil, nil
	})

	_, err := mgr.UpdateResource(context.Background(), UpdateResourceOptions{
		Kind:  "clusterpolicies",
		Name:  "restrict-image-registries",
		YAML:  clusterPolicyYAML,
		Force: true,
	})
	if err != nil {
		t.Fatalf("UpdateResource failed: %v", err)
	}

	if captured == nil {
		t.Fatal("expected a PATCH action; got none")
	}
	if got := captured.GetPatchType(); got != types.ApplyPatchType {
		t.Errorf("patch type = %v, want %v (server-side apply)", got, types.ApplyPatchType)
	}

	impl, ok := captured.(clienttesting.PatchActionImpl)
	if !ok {
		t.Fatalf("captured action is %T, want clienttesting.PatchActionImpl", captured)
	}
	if impl.PatchOptions.FieldManager != "radar" {
		t.Errorf("FieldManager = %q, want %q", impl.PatchOptions.FieldManager, "radar")
	}
	if impl.PatchOptions.FieldValidation != metav1.FieldValidationStrict {
		t.Errorf("FieldValidation = %q, want %q", impl.PatchOptions.FieldValidation, metav1.FieldValidationStrict)
	}
	if impl.PatchOptions.Force == nil || !*impl.PatchOptions.Force {
		t.Errorf("Force = %v, want *true", impl.PatchOptions.Force)
	}

	var body map[string]any
	if err := json.Unmarshal(captured.GetPatch(), &body); err != nil {
		t.Fatalf("patch body is not JSON: %v", err)
	}
	meta, _ := body["metadata"].(map[string]any)
	for _, banned := range []string{"resourceVersion", "uid", "managedFields", "generation", "creationTimestamp", "selfLink"} {
		if _, present := meta[banned]; present {
			t.Errorf("patch body still contains metadata.%s; SSA expects these stripped", banned)
		}
	}
	// status must NOT be stripped: CRDs without a status subresource treat it
	// as a user-writable field, and stripping silently discards user edits.
	// For subresourced kinds, the apiserver ignores status on /apply anyway.
	if _, present := body["status"]; !present {
		t.Error("status was stripped from the patch body; CRDs without status subresource need it preserved")
	}
}

// TestUpdateResource_ForcePlumbedToPatchOptions verifies the caller's Force
// choice reaches the SSA PatchOptions (the editor's Force checkbox opts out).
func TestUpdateResource_ForcePlumbedToPatchOptions(t *testing.T) {
	dyn, gvr := newFakeDynamicWithClusterPolicy(t)
	disc := stubDiscovery(t, "ClusterPolicy", gvr)
	mgr := NewWorkloadManager(dyn, disc)

	var captured clienttesting.PatchAction
	dyn.PrependReactor("patch", "clusterpolicies", func(a clienttesting.Action) (bool, runtime.Object, error) {
		captured = a.(clienttesting.PatchAction)
		return true, nil, nil
	})

	_, err := mgr.UpdateResource(context.Background(), UpdateResourceOptions{
		Kind:  "ClusterPolicy",
		Name:  "restrict-image-registries",
		YAML:  clusterPolicyYAML,
		Force: false,
	})
	if err != nil {
		t.Fatalf("UpdateResource failed: %v", err)
	}
	impl, ok := captured.(clienttesting.PatchActionImpl)
	if !ok {
		t.Fatalf("captured action is %T, want clienttesting.PatchActionImpl", captured)
	}
	if impl.PatchOptions.Force == nil || *impl.PatchOptions.Force {
		t.Errorf("Force = %v, want *false", impl.PatchOptions.Force)
	}
}

// TestUpdateResource_RejectsMismatchedName guards the existing safety check
// (caller's URL params must match the YAML body).
func TestUpdateResource_RejectsMismatchedName(t *testing.T) {
	dyn, gvr := newFakeDynamicWithClusterPolicy(t)
	disc := stubDiscovery(t, "ClusterPolicy", gvr)
	mgr := NewWorkloadManager(dyn, disc)

	_, err := mgr.UpdateResource(context.Background(), UpdateResourceOptions{
		Kind: "ClusterPolicy",
		Name: "different-name",
		YAML: clusterPolicyYAML,
	})
	if err == nil || !strings.Contains(err.Error(), "name mismatch") {
		t.Fatalf("expected name mismatch error, got: %v", err)
	}
}

func TestUpdateResource_RejectsMismatchedKind(t *testing.T) {
	dyn, gvr := newFakeDynamicWithClusterPolicy(t)
	disc := stubDiscovery(t, "ClusterPolicy", gvr)
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	disc.resources = append(disc.resources,
		APIResource{Kind: "ClusterPolicy", Name: gvr.Resource, Group: gvr.Group, Version: gvr.Version},
		APIResource{Kind: "Pod", Name: podGVR.Resource, Group: podGVR.Group, Version: podGVR.Version},
	)
	disc.resourceMap["pod"] = APIResource{Kind: "Pod", Name: podGVR.Resource, Group: podGVR.Group, Version: podGVR.Version}
	disc.resourceMap[podGVR.Resource] = APIResource{Kind: "Pod", Name: podGVR.Resource, Group: podGVR.Group, Version: podGVR.Version}
	disc.gvrMap["pod"] = podGVR
	disc.gvrMap[podGVR.Resource] = podGVR
	mgr := NewWorkloadManager(dyn, disc)

	_, err := mgr.UpdateResource(context.Background(), UpdateResourceOptions{
		Kind: "Pod",
		Name: "restrict-image-registries",
		YAML: clusterPolicyYAML,
	})
	if err == nil || !strings.Contains(err.Error(), "kind mismatch") {
		t.Fatalf("expected kind mismatch error, got: %v", err)
	}
}

func TestPreviewUpdateResource_DryRunsSamePatchWithResourceVersion(t *testing.T) {
	dyn, gvr := newFakeDynamicWithClusterPolicy(t)
	disc := stubDiscovery(t, "ClusterPolicy", gvr)
	mgr := NewWorkloadManager(dyn, disc)
	live := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kyverno.io/v1",
		"kind":       "ClusterPolicy",
		"metadata": map[string]any{
			"name":            "restrict-image-registries",
			"resourceVersion": "42",
		},
	}}
	getCount := 0
	dyn.PrependReactor("get", "clusterpolicies", func(clienttesting.Action) (bool, runtime.Object, error) {
		getCount++
		return true, live.DeepCopy(), nil
	})
	var captured clienttesting.PatchActionImpl
	dyn.PrependReactor("patch", "clusterpolicies", func(a clienttesting.Action) (bool, runtime.Object, error) {
		captured = a.(clienttesting.PatchActionImpl)
		return true, live.DeepCopy(), nil
	})

	result, err := mgr.PreviewUpdateResource(context.Background(), UpdateResourceOptions{
		Kind: "ClusterPolicy",
		Name: "restrict-image-registries",
		YAML: clusterPolicyYAML,
	})
	if err != nil {
		t.Fatalf("PreviewUpdateResource failed: %v", err)
	}
	if result.ResourceVersion != "42" {
		t.Fatalf("ResourceVersion = %q, want 42", result.ResourceVersion)
	}
	if getCount != 1 {
		t.Fatalf("GET count = %d, want one live read", getCount)
	}
	if len(captured.PatchOptions.DryRun) != 1 || captured.PatchOptions.DryRun[0] != metav1.DryRunAll {
		t.Fatalf("DryRun = %v, want [%q]", captured.PatchOptions.DryRun, metav1.DryRunAll)
	}
	if captured.PatchOptions.FieldValidation != metav1.FieldValidationStrict {
		t.Fatalf("FieldValidation = %q, want %q", captured.PatchOptions.FieldValidation, metav1.FieldValidationStrict)
	}
	var body map[string]any
	if err := json.Unmarshal(captured.GetPatch(), &body); err != nil {
		t.Fatalf("patch body is not JSON: %v", err)
	}
	meta := body["metadata"].(map[string]any)
	if meta["resourceVersion"] != "42" {
		t.Fatalf("metadata.resourceVersion = %v, want 42", meta["resourceVersion"])
	}
}

func TestUpdateResource_RejectsStaleReviewedVersionBeforePatch(t *testing.T) {
	dyn, gvr := newFakeDynamicWithClusterPolicy(t)
	disc := stubDiscovery(t, "ClusterPolicy", gvr)
	mgr := NewWorkloadManager(dyn, disc)
	dyn.PrependReactor("get", "clusterpolicies", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"name": "restrict-image-registries", "resourceVersion": "new"},
		}}, nil
	})
	patched := false
	dyn.PrependReactor("patch", "clusterpolicies", func(clienttesting.Action) (bool, runtime.Object, error) {
		patched = true
		return true, nil, nil
	})

	_, err := mgr.UpdateResource(context.Background(), UpdateResourceOptions{
		Kind:                    "ClusterPolicy",
		Name:                    "restrict-image-registries",
		YAML:                    clusterPolicyYAML,
		ExpectedResourceVersion: "reviewed",
	})
	if !apierrors.IsConflict(err) {
		t.Fatalf("error = %v, want conflict", err)
	}
	if patched {
		t.Fatal("stale review reached PATCH")
	}
}

func TestApplyResource_RejectsStaleReviewedVersionBeforePatch(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	pre := mkWorkload(t, "Deployment", nil)
	pre.SetResourceVersion("new")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
		pre,
	)
	patched := false
	dyn.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		patched = true
		return true, nil, nil
	})
	discovery := &ResourceDiscovery{
		resources: []APIResource{{
			Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
	}
	mgr := NewWorkloadManager(dyn, discovery)

	_, err := mgr.ApplyResource(context.Background(), ApplyResourceOptions{
		ExpectedResourceVersion: "reviewed",
		YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: w
  namespace: ns
`,
	})
	if !apierrors.IsConflict(err) {
		t.Fatalf("error = %v, want conflict", err)
	}
	if patched {
		t.Fatal("stale review reached PATCH")
	}
}

func TestApplyResource_RejectsResourceCreatedAfterReview(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	createdAfterReview := mkWorkload(t, "Deployment", nil)
	createdAfterReview.SetResourceVersion("1")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
		createdAfterReview,
	)
	patched := false
	dyn.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		patched = true
		return true, nil, nil
	})
	discovery := &ResourceDiscovery{
		resources: []APIResource{{
			Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
	}
	mgr := NewWorkloadManager(dyn, discovery)

	_, err := mgr.ApplyResource(context.Background(), ApplyResourceOptions{
		ExpectedResourceAbsent: true,
		YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: w
  namespace: ns
`,
	})
	if !apierrors.IsConflict(err) {
		t.Fatalf("error = %v, want conflict", err)
	}
	if patched {
		t.Fatal("resource created after review reached PATCH")
	}
}

func TestApplyResource_ReviewedCreateIsAtomic(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
	)
	patched := false
	dyn.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(gvr.GroupResource(), "w")
	})
	dyn.PrependReactor("create", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		create := action.(clienttesting.CreateActionImpl)
		if create.GetCreateOptions().FieldManager != "radar" {
			t.Fatalf("field manager = %q, want radar", create.GetCreateOptions().FieldManager)
		}
		if create.GetCreateOptions().FieldValidation != metav1.FieldValidationStrict {
			t.Fatalf("field validation = %q, want %q", create.GetCreateOptions().FieldValidation, metav1.FieldValidationStrict)
		}
		return true, nil, apierrors.NewAlreadyExists(gvr.GroupResource(), "w")
	})
	dyn.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		patched = true
		return true, nil, nil
	})
	discovery := &ResourceDiscovery{
		resources: []APIResource{{
			Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
	}
	mgr := NewWorkloadManager(dyn, discovery)

	_, err := mgr.ApplyResource(context.Background(), ApplyResourceOptions{
		ExpectedResourceAbsent: true,
		YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: w
  namespace: ns
`,
	})
	if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("error = %v, want already exists", err)
	}
	if patched {
		t.Fatal("reviewed create reached PATCH")
	}
}

func TestApplyResource_ReviewedCreateReportsCreated(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
	)
	discovery := &ResourceDiscovery{
		resources: []APIResource{{
			Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
	}
	mgr := NewWorkloadManager(dyn, discovery)

	result, err := mgr.ApplyResource(context.Background(), ApplyResourceOptions{
		ExpectedResourceAbsent: true,
		YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: w
  namespace: ns
`,
	})
	if err != nil {
		t.Fatalf("ApplyResource failed: %v", err)
	}
	if !result.Created {
		t.Fatal("Created = false, want true")
	}
	if result.Action != "create" {
		t.Fatalf("Action = %q, want create", result.Action)
	}
}

func TestApplyResource_PreviewUsesCreateForAbsentResource(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
	)
	var captured clienttesting.CreateActionImpl
	patched := false
	dyn.PrependReactor("create", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		captured = action.(clienttesting.CreateActionImpl)
		return false, nil, nil
	})
	dyn.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		patched = true
		return true, nil, nil
	})
	discovery := &ResourceDiscovery{
		resources: []APIResource{{
			Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
	}
	mgr := NewWorkloadManager(dyn, discovery)

	result, err := mgr.ApplyResource(context.Background(), ApplyResourceOptions{
		DryRun:             true,
		UseCreateForAbsent: true,
		YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: w
  namespace: ns
`,
	})
	if err != nil {
		t.Fatalf("ApplyResource failed: %v", err)
	}
	if patched {
		t.Fatal("absent-resource preview reached PATCH")
	}
	if len(captured.CreateOptions.DryRun) != 1 || captured.CreateOptions.DryRun[0] != metav1.DryRunAll {
		t.Fatalf("DryRun = %v, want [%q]", captured.CreateOptions.DryRun, metav1.DryRunAll)
	}
	if captured.CreateOptions.FieldManager != "radar" {
		t.Fatalf("FieldManager = %q, want radar", captured.CreateOptions.FieldManager)
	}
	if captured.CreateOptions.FieldValidation != metav1.FieldValidationStrict {
		t.Fatalf("FieldValidation = %q, want %q", captured.CreateOptions.FieldValidation, metav1.FieldValidationStrict)
	}
	if !result.Created || result.Action != "create" {
		t.Fatalf("result = %+v, want created action", result)
	}
}

func TestApplyResource_RetriesOnlyRadarOwnershipConflictWithForce(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":            "w",
			"namespace":       "ns",
			"resourceVersion": "1",
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
		existing,
	)
	patches := make([]clienttesting.PatchActionImpl, 0, 3)
	dyn.PrependReactor("patch", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patch := action.(clienttesting.PatchActionImpl)
		patches = append(patches, patch)
		if patch.PatchOptions.Force == nil || !*patch.PatchOptions.Force {
			return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{
				Status: metav1.StatusFailure,
				Reason: metav1.StatusReasonConflict,
				Code:   409,
				Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
					Type:    metav1.CauseTypeFieldManagerConflict,
					Field:   ".spec.replicas",
					Message: `conflict with "radar" using apps/v1`,
				}}},
			}}
		}
		return true, existing.DeepCopy(), nil
	})
	discovery := &ResourceDiscovery{
		resources: []APIResource{{
			Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
	}
	mgr := NewWorkloadManager(dyn, discovery)

	result, err := mgr.ApplyResource(context.Background(), ApplyResourceOptions{
		YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: w
  namespace: ns
`,
	})
	if err != nil {
		t.Fatalf("ApplyResource failed: %v", err)
	}
	if len(patches) != 3 {
		t.Fatalf("patch count = %d, want 3", len(patches))
	}
	if patches[0].PatchOptions.Force == nil || *patches[0].PatchOptions.Force {
		t.Fatalf("first force = %v, want false", patches[0].PatchOptions.Force)
	}
	if patches[1].PatchOptions.Force == nil || *patches[1].PatchOptions.Force {
		t.Fatalf("verified retry force = %v, want false", patches[1].PatchOptions.Force)
	}
	if patches[2].PatchOptions.Force == nil || !*patches[2].PatchOptions.Force {
		t.Fatalf("reclaim force = %v, want true", patches[2].PatchOptions.Force)
	}
	for index, patch := range patches[1:] {
		var body unstructured.Unstructured
		if err := json.Unmarshal(patch.GetPatch(), &body.Object); err != nil {
			t.Fatalf("decode patch %d: %v", index+1, err)
		}
		if body.GetResourceVersion() != "1" {
			t.Fatalf("patch %d resourceVersion = %q, want 1", index+1, body.GetResourceVersion())
		}
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "Radar reclaimed field ownership") {
		t.Fatalf("warnings = %v, want Radar ownership warning", result.Warnings)
	}
}

func TestApplyResource_DoesNotReclaimWhenExternalConflictAppears(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":            "w",
			"namespace":       "ns",
			"resourceVersion": "1",
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
		existing,
	)
	patchCount := 0
	dyn.PrependReactor("patch", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patchCount++
		patch := action.(clienttesting.PatchActionImpl)
		if patch.PatchOptions.Force != nil && *patch.PatchOptions.Force {
			t.Fatal("external conflict reached forced retry")
		}
		manager := "radar"
		if patchCount == 2 {
			manager = "argocd"
		}
		return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure,
			Reason: metav1.StatusReasonConflict,
			Code:   409,
			Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
				Type:    metav1.CauseTypeFieldManagerConflict,
				Field:   ".spec.replicas",
				Message: fmt.Sprintf("conflict with %q using apps/v1", manager),
			}}},
		}}
	})
	discovery := &ResourceDiscovery{
		resources: []APIResource{{
			Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
	}
	mgr := NewWorkloadManager(dyn, discovery)

	_, err := mgr.ApplyResource(context.Background(), ApplyResourceOptions{
		YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: w
  namespace: ns
`,
	})
	if !apierrors.IsConflict(err) {
		t.Fatalf("error = %v, want external-manager conflict", err)
	}
	if patchCount != 2 {
		t.Fatalf("patch count = %d, want 2", patchCount)
	}
}

func TestPreviewUpdateResource_ReclaimsRadarOwnershipWithWarning(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":            "w",
			"namespace":       "ns",
			"resourceVersion": "1",
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
		existing,
	)
	patchCount := 0
	dyn.PrependReactor("patch", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patchCount++
		patch := action.(clienttesting.PatchActionImpl)
		if patch.PatchOptions.Force == nil || !*patch.PatchOptions.Force {
			return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{
				Status: metav1.StatusFailure,
				Reason: metav1.StatusReasonConflict,
				Code:   409,
				Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{
					Type:    metav1.CauseTypeFieldManagerConflict,
					Field:   ".spec.replicas",
					Message: `conflict with "radar" using apps/v1`,
				}}},
			}}
		}
		return true, existing.DeepCopy(), nil
	})
	discovery := &ResourceDiscovery{
		resources: []APIResource{{
			Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
	}
	mgr := NewWorkloadManager(dyn, discovery)

	result, err := mgr.PreviewUpdateResource(context.Background(), UpdateResourceOptions{
		Kind:      "Deployment",
		Namespace: "ns",
		Name:      "w",
		YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: w
  namespace: ns
`,
	})
	if err != nil {
		t.Fatalf("PreviewUpdateResource failed: %v", err)
	}
	if patchCount != 2 {
		t.Fatalf("patch count = %d, want 2", patchCount)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "Radar reclaimed field ownership") {
		t.Fatalf("warnings = %v, want Radar ownership warning", result.Warnings)
	}
}

func TestIsOnlyRadarFieldManagerConflict(t *testing.T) {
	statusConflict := func(messages ...string) error {
		causes := make([]metav1.StatusCause, 0, len(messages))
		for _, message := range messages {
			causes = append(causes, metav1.StatusCause{
				Type:    metav1.CauseTypeFieldManagerConflict,
				Message: message,
			})
		}
		return &apierrors.StatusError{ErrStatus: metav1.Status{
			Reason:  metav1.StatusReasonConflict,
			Details: &metav1.StatusDetails{Causes: causes},
		}}
	}

	if !isOnlyRadarFieldManagerConflict(statusConflict(`conflict with "radar" using apps/v1`)) {
		t.Fatal("Radar-only conflict was not recognized")
	}
	if isOnlyRadarFieldManagerConflict(statusConflict(`conflict with "helm" using apps/v1`)) {
		t.Fatal("external-manager conflict was recognized as Radar-only")
	}
	if isOnlyRadarFieldManagerConflict(statusConflict(
		`conflict with "radar" using apps/v1`,
		`conflict with "argocd" using apps/v1`,
	)) {
		t.Fatal("mixed-manager conflict was recognized as Radar-only")
	}
	if isOnlyRadarFieldManagerConflict(apierrors.NewConflict(
		schema.GroupResource{Group: "apps", Resource: "deployments"},
		"w",
		fmt.Errorf("resource version changed"),
	)) {
		t.Fatal("generic conflict was recognized as Radar field ownership")
	}
}

func TestApplyResource_DryRunWarningsUseAdmittedObject(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	pre := mkWorkload(t, "Deployment", map[string]any{
		"nodeSelector": map[string]any{"disk": "ssd"},
		"containers":   []any{map[string]any{"name": "app", "image": "example:v1"}},
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "DeploymentList"},
		pre,
	)
	var captured clienttesting.PatchActionImpl
	dyn.PrependReactor("patch", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		captured = action.(clienttesting.PatchActionImpl)
		return true, pre.DeepCopy(), nil
	})
	discovery := &ResourceDiscovery{
		resources: []APIResource{{
			Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
	}
	mgr := NewWorkloadManager(dyn, discovery)

	result, err := mgr.ApplyResource(context.Background(), ApplyResourceOptions{
		DryRun: true,
		YAML: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: w
  namespace: ns
spec:
  template:
    spec:
      containers:
      - name: app
        image: example:v2
`,
	})
	if err != nil {
		t.Fatalf("ApplyResource failed: %v", err)
	}
	if len(captured.PatchOptions.DryRun) != 1 || captured.PatchOptions.DryRun[0] != metav1.DryRunAll {
		t.Fatalf("DryRun = %v, want [%q]", captured.PatchOptions.DryRun, metav1.DryRunAll)
	}
	if captured.PatchOptions.FieldValidation != metav1.FieldValidationStrict {
		t.Fatalf("FieldValidation = %q, want %q", captured.PatchOptions.FieldValidation, metav1.FieldValidationStrict)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "nodeSelector") {
		t.Fatalf("Warnings = %v, want retained nodeSelector warning", result.Warnings)
	}
}
