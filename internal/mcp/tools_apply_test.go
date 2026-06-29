package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/k8s"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestHandleApplyResourceValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   applyResourceInput
		wantErr string
	}{
		{
			name:    "empty yaml",
			input:   applyResourceInput{},
			wantErr: "yaml is required",
		},
		{
			name:    "invalid mode",
			input:   applyResourceInput{YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n", Mode: "replace"},
			wantErr: "mode must be 'apply' or 'create'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := handleApplyResource(context.Background(), nil, tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPreReadApplyMutationTargetReturnsExistingObject(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	before := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "frontend", "namespace": "prod"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"dnsPolicy":  "None",
			"containers": []any{map[string]any{"name": "app", "image": "frontend:v1"}},
		}}},
	}}
	dyn := setupMCPDynamicResource(t, gvr, "DeploymentList", k8s.APIResource{
		Group:      "apps",
		Version:    "v1",
		Kind:       "Deployment",
		Name:       "deployments",
		Namespaced: true,
		Verbs:      []string{"get", "list", "patch"},
	}, before)

	gotGVR, gotBefore, beforeErr := preReadApplyMutationTarget(context.Background(), dyn, applyMutationTarget{
		Kind:      "Deployment",
		Group:     "apps",
		Namespace: "prod",
		Name:      "frontend",
	})
	if beforeErr != "" {
		t.Fatalf("beforeErr = %q, want empty", beforeErr)
	}
	if gotGVR != gvr {
		t.Fatalf("gvr = %v, want %v", gotGVR, gvr)
	}
	if gotBefore == nil {
		t.Fatal("before = nil, want existing object")
	}
	if dnsPolicy, ok, _ := unstructured.NestedString(gotBefore.Object, "spec", "template", "spec", "dnsPolicy"); !ok || dnsPolicy != "None" {
		t.Fatalf("dnsPolicy = (%q, %v), want None true", dnsPolicy, ok)
	}
}

func TestHandleApplyResourceCreateUsesDynamicClient(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	setupMCPDynamicResource(t, gvr, "ConfigMapList", k8s.APIResource{
		Version:    "v1",
		Kind:       "ConfigMap",
		Name:       "configmaps",
		Namespaced: true,
		Verbs:      []string{"create", "get", "list", "patch"},
	})

	verify := false
	res, _, err := handleApplyResource(context.Background(), nil, applyResourceInput{
		Mode:   "create",
		Verify: &verify,
		YAML: `apiVersion: v1
kind: ConfigMap
metadata:
  name: created
  namespace: prod
data:
  key: value
`,
	})
	if err != nil {
		t.Fatalf("handleApplyResource: %v", err)
	}
	got := decodeToolResult(t, res)
	if got["status"] != "ok" || got["created"] != true || got["kind"] != "ConfigMap" || got["name"] != "created" || got["namespace"] != "prod" {
		t.Fatalf("apply result = %+v, want created ConfigMap prod/created", got)
	}
}

func TestHandleApplyResourceDryRunIncludesPreviewDiff(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "cfg", "namespace": "prod"},
		"data":       map[string]any{"key": "old"},
	}}
	dyn := setupMCPDynamicResource(t, gvr, "ConfigMapList", k8s.APIResource{
		Version:    "v1",
		Kind:       "ConfigMap",
		Name:       "configmaps",
		Namespaced: true,
		Verbs:      []string{"create", "get", "list", "patch"},
	}, existing)
	fakeDyn := dyn.(*dynamicfake.FakeDynamicClient)
	fakeDyn.PrependReactor("patch", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patch := action.(clienttesting.PatchAction)
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"name": patch.GetName(), "namespace": patch.GetNamespace()},
			"data":       map[string]any{"key": "new"},
		}}, nil
	})

	res, _, err := handleApplyResource(context.Background(), nil, applyResourceInput{
		DryRun: true,
		YAML: `apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
  namespace: prod
data:
  key: new
`,
	})
	if err != nil {
		t.Fatalf("handleApplyResource: %v", err)
	}
	got := decodeToolResult(t, res)
	if got["status"] != "ok" || got["dry_run"] != true {
		t.Fatalf("apply result = %+v, want ok dry_run", got)
	}
	verification, ok := got["verification"].(map[string]any)
	if !ok {
		t.Fatalf("verification = %#v, want object", got["verification"])
	}
	if verification["mode"] != "dry_run_preview" {
		t.Fatalf("verification mode = %v, want dry_run_preview", verification["mode"])
	}
	if _, ok := verification["pods"]; ok {
		t.Fatalf("dry-run verification should not include live pod snapshots: %#v", verification["pods"])
	}
	if _, ok := verification["currentIssues"]; ok {
		t.Fatalf("dry-run verification should not include current issues snapshots: %#v", verification["currentIssues"])
	}
	preview, ok := verification["previewDiff"].(map[string]any)
	if !ok || preview["mode"] != "before_after" {
		t.Fatalf("previewDiff = %#v, want before_after", verification["previewDiff"])
	}
}

func TestHandleApplyResourceMultiDocPartialFailureEnvelope(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "already", "namespace": "prod"},
	}}
	setupMCPDynamicResource(t, gvr, "ConfigMapList", k8s.APIResource{
		Version:    "v1",
		Kind:       "ConfigMap",
		Name:       "configmaps",
		Namespaced: true,
		Verbs:      []string{"create", "get", "list", "patch"},
	}, existing)

	verify := false
	res, _, err := handleApplyResource(context.Background(), nil, applyResourceInput{
		Mode:   "create",
		Verify: &verify,
		YAML: `apiVersion: v1
kind: ConfigMap
metadata:
  name: first
  namespace: prod
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: already
  namespace: prod
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: third
  namespace: prod
`,
	})
	if err != nil {
		t.Fatalf("handleApplyResource returned hard error for multi-doc partial failure: %v", err)
	}
	got := decodeToolResult(t, res)
	if got["status"] != "partial_failure" {
		t.Fatalf("status = %v, want partial_failure; result=%+v", got["status"], got)
	}
	resources, ok := got["resources"].([]any)
	if !ok || len(resources) != 3 {
		t.Fatalf("resources = %#v, want three entries", got["resources"])
	}
	first := resources[0].(map[string]any)
	if first["name"] != "first" || first["created"] != true {
		t.Fatalf("first resource = %+v, want created first", first)
	}
	failed := resources[1].(map[string]any)
	if failed["status"] != "failed" || failed["document"] != float64(2) || failed["error"] == "" {
		t.Fatalf("failed resource = %+v, want document 2 failure", failed)
	}
	third := resources[2].(map[string]any)
	if third["name"] != "third" || third["created"] != true {
		t.Fatalf("third resource = %+v, want created third after continuing past failure", third)
	}
}

func TestHandleApplyResourceMultiDocSurfacesSSAConflict(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	dyn := setupMCPDynamicResource(t, gvr, "ConfigMapList", k8s.APIResource{
		Version:    "v1",
		Kind:       "ConfigMap",
		Name:       "configmaps",
		Namespaced: true,
		Verbs:      []string{"get", "list", "patch"},
	})
	fakeDyn := dyn.(*dynamicfake.FakeDynamicClient)
	var sawDefaultForce bool
	fakeDyn.PrependReactor("patch", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patch := action.(clienttesting.PatchAction)
		if patch.GetName() == "conflict" {
			if patchImpl, ok := action.(clienttesting.PatchActionImpl); ok {
				opts := patchImpl.GetPatchOptions()
				sawDefaultForce = opts.Force != nil && !*opts.Force
			}
			return true, nil, &apierrors.StatusError{ErrStatus: metav1.Status{
				Status:  metav1.StatusFailure,
				Reason:  metav1.StatusReasonConflict,
				Message: `Apply failed with 1 conflict: conflict with "helm": .data.key`,
				Code:    409,
				Details: &metav1.StatusDetails{
					Group: "core",
					Kind:  "configmaps",
					Name:  "conflict",
					Causes: []metav1.StatusCause{{
						Type:    metav1.CauseTypeFieldManagerConflict,
						Field:   ".data.key",
						Message: `conflict with "helm"`,
					}},
				},
			}}
		}
		return true, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"name": patch.GetName(), "namespace": patch.GetNamespace()},
		}}, nil
	})

	verify := false
	res, _, err := handleApplyResource(context.Background(), nil, applyResourceInput{
		Verify: &verify,
		YAML: `apiVersion: v1
kind: ConfigMap
metadata:
  name: conflict
  namespace: prod
data:
  key: new
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ok
  namespace: prod
`,
	})
	if err != nil {
		t.Fatalf("handleApplyResource returned hard error for multi-doc conflict: %v", err)
	}
	if !sawDefaultForce {
		t.Fatal("apply_resource default must send Force=false so SSA ownership conflicts are surfaced")
	}
	got := decodeToolResult(t, res)
	if got["status"] != "partial_failure" {
		t.Fatalf("status = %v, want partial_failure; result=%+v", got["status"], got)
	}
	resources := got["resources"].([]any)
	failed := resources[0].(map[string]any)
	if failed["error_type"] != "ssa_field_ownership_conflict" {
		t.Fatalf("failed resource = %+v, want SSA conflict type", failed)
	}
	conflict := failed["conflict"].(map[string]any)
	causes := conflict["causes"].([]any)
	cause := causes[0].(map[string]any)
	if cause["field"] != ".data.key" || !strings.Contains(cause["message"].(string), "helm") {
		t.Fatalf("conflict cause = %+v, want field manager conflict", cause)
	}
	second := resources[1].(map[string]any)
	if second["name"] != "ok" || second["status"] != "applied" {
		t.Fatalf("second resource = %+v, want continued applied ok", second)
	}
}

func TestApplyConflictDetailsKeepsGenericConflictsGeneric(t *testing.T) {
	gr := schema.GroupResource{Group: "", Resource: "configmaps"}
	err := apierrors.NewConflict(gr, "cfg", errors.New("resourceVersion changed"))

	conflict, conflictType, ok := applyConflictDetails(err)
	if !ok {
		t.Fatal("applyConflictDetails did not recognize generic conflict")
	}
	if conflictType != "conflict" || conflict["kind"] != "conflict" {
		t.Fatalf("conflict = %+v type=%s, want generic conflict", conflict, conflictType)
	}
	if strings.Contains(formatApplyResourceError(err).Error(), "field ownership") {
		t.Fatalf("generic conflict was mislabeled as SSA ownership: %v", formatApplyResourceError(err))
	}
}

func setupMCPDynamicResource(t *testing.T, gvr schema.GroupVersionResource, listKind string, resource k8s.APIResource, objs ...runtime.Object) dynamic.Interface {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr: listKind,
	}, objs...)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{resource}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	return dyn
}

func decodeToolResult(t *testing.T, res *mcpsdk.CallToolResult) map[string]any {
	t.Helper()
	if res == nil || len(res.Content) != 1 {
		t.Fatalf("result content = %+v, want one text item", res)
	}
	text, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("result content type = %T, want TextContent", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("unmarshal result %q: %v", text.Text, err)
	}
	return out
}
