package k8score

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
		},
		gvrMap: map[string]schema.GroupVersionResource{strings.ToLower(kind): gvr},
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
		Kind: "ClusterPolicy",
		Name: "restrict-image-registries",
		YAML: clusterPolicyYAML,
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
