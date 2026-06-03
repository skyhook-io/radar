package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestHandlePatchResourceValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   patchResourceInput
		wantErr string
	}{
		{
			name:    "missing kind",
			input:   patchResourceInput{Name: "frontend", Patch: `[]`},
			wantErr: "kind is required",
		},
		{
			name:    "missing name",
			input:   patchResourceInput{Kind: "Deployment", Patch: `[]`},
			wantErr: "name is required",
		},
		{
			name:    "missing patch",
			input:   patchResourceInput{Kind: "Deployment", Name: "frontend"},
			wantErr: "patch is required",
		},
		{
			name:    "invalid json",
			input:   patchResourceInput{Kind: "Deployment", Name: "frontend", Patch: `{`},
			wantErr: "patch must be valid JSON",
		},
		{
			name:    "missing namespace for namespaced kind",
			input:   patchResourceInput{Kind: "ConfigMap", Name: "cfg", Patch: `[]`},
			wantErr: "namespace is required for namespaced kind",
		},
		{
			name:    "namespace forbidden for cluster scoped kind",
			input:   patchResourceInput{Kind: "Node", Namespace: "prod", Name: "node-1", Patch: `[]`},
			wantErr: "namespace must be empty for cluster-scoped kind",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := handlePatchResource(context.Background(), nil, tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestHandlePatchResourceJSONPatchMutatesObject(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	cfg := &unstructured.Unstructured{Object: map[string]any{
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
		Verbs:      []string{"get", "list", "patch"},
	}, cfg)

	res, _, err := handlePatchResource(context.Background(), nil, patchResourceInput{
		Kind:      "ConfigMap",
		Namespace: "prod",
		Name:      "cfg",
		Patch:     `[{"op":"replace","path":"/data/key","value":"new"}]`,
	})
	if err != nil {
		t.Fatalf("handlePatchResource: %v", err)
	}
	got := decodeToolResult(t, res)
	if got["status"] != "ok" || got["patch_type"] != "json" {
		t.Fatalf("patch result = %+v, want ok json", got)
	}
	verification, ok := got["verification"].(map[string]any)
	if !ok || verification["mode"] != "post_mutation_state" {
		t.Fatalf("verification = %#v, want post_mutation_state envelope", got["verification"])
	}
	ops, ok := verification["operations"].([]any)
	if !ok || len(ops) != 1 {
		t.Fatalf("verification operations = %#v, want one operation", verification["operations"])
	}
	live, err := dyn.Resource(gvr).Namespace("prod").Get(context.Background(), "cfg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched configmap: %v", err)
	}
	if val, ok, _ := unstructured.NestedString(live.Object, "data", "key"); !ok || val != "new" {
		t.Fatalf("live data.key = (%q, %v), want new true", val, ok)
	}
}

func TestHandlePatchResourceDryRunIncludesPreviewDiff(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	cfg := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "cfg", "namespace": "prod"},
		"data":       map[string]any{"key": "old"},
	}}
	setupMCPDynamicResource(t, gvr, "ConfigMapList", k8s.APIResource{
		Version:    "v1",
		Kind:       "ConfigMap",
		Name:       "configmaps",
		Namespaced: true,
		Verbs:      []string{"get", "list", "patch"},
	}, cfg)

	res, _, err := handlePatchResource(context.Background(), nil, patchResourceInput{
		Kind:      "ConfigMap",
		Namespace: "prod",
		Name:      "cfg",
		PatchType: "merge",
		Patch:     `{"data":{"key":"new"}}`,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("handlePatchResource: %v", err)
	}
	got := decodeToolResult(t, res)
	if got["status"] != "ok" || got["dry_run"] != true {
		t.Fatalf("patch result = %+v, want ok dry_run", got)
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
	differences, ok := preview["differences"].([]any)
	if !ok || len(differences) == 0 {
		t.Fatalf("previewDiff differences = %#v, want non-empty", preview["differences"])
	}
}

func TestVerifyJSONPatchOperations(t *testing.T) {
	before := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"dnsConfig": map[string]any{"nameservers": []any{"8.8.8.8"}},
					"dnsPolicy": "None",
					"containers": []any{
						map[string]any{"name": "app"},
						map[string]any{"name": "sidecar"},
					},
				},
			},
		},
	}}
	after := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"dnsPolicy":  "ClusterFirst",
					"containers": []any{map[string]any{"name": "app"}},
				},
			},
		},
	}}

	ops := []jsonPatchOperation{
		{Op: "remove", Path: "/spec/template/spec/dnsConfig"},
		{Op: "replace", Path: "/spec/template/spec/dnsPolicy", Value: json.RawMessage(`"ClusterFirst"`)},
		{Op: "remove", Path: "/spec/template/spec/containers/1"},
	}

	got := verifyJSONPatchOperations(before, after, ops, "")
	if got[0]["status"] != "removed" {
		t.Fatalf("dnsConfig status = %v, want removed", got[0]["status"])
	}
	if got[1]["status"] != "present" || got[1]["value_matched"] != true {
		t.Fatalf("dnsPolicy verification = %#v, want present + value_matched", got[1])
	}
	if got[2]["status"] != "removed" {
		t.Fatalf("container status = %v, want removed", got[2]["status"])
	}
}

func TestVerifyJSONPatchOperationsUnknownBefore(t *testing.T) {
	after := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{}}},
	}}
	ops := []jsonPatchOperation{{
		Op:   "remove",
		Path: "/spec/template/spec/dnsConfig",
	}}

	got := verifyJSONPatchOperations(nil, after, ops, "forbidden")
	if got[0]["status"] != "unknown_before" || got[0]["before_error"] != "forbidden" {
		t.Fatalf("remove verification = %#v, want unknown_before with error", got[0])
	}
}

func TestVerifyJSONPatchOperationsRemoveChangedOrUnchanged(t *testing.T) {
	before := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"dnsPolicy": "None"},
	}}
	changedAfter := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"dnsPolicy": "ClusterFirst"},
	}}
	unchangedAfter := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"dnsPolicy": "None"},
	}}
	ops := []jsonPatchOperation{{
		Op:   "remove",
		Path: "/spec/dnsPolicy",
	}}

	changed := verifyJSONPatchOperations(before, changedAfter, ops, "")
	if changed[0]["status"] != "changed_at_path" {
		t.Fatalf("changed remove verification = %#v, want changed_at_path", changed[0])
	}
	unchanged := verifyJSONPatchOperations(before, unchangedAfter, ops, "")
	if unchanged[0]["status"] != "unchanged" {
		t.Fatalf("unchanged remove verification = %#v, want unchanged", unchanged[0])
	}
}

func TestVerifyJSONPatchOperationsArrayAppend(t *testing.T) {
	after := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"ports": []any{map[string]any{"port": int64(80)}}},
	}}
	ops := []jsonPatchOperation{{
		Op:    "add",
		Path:  "/spec/ports/-",
		Value: json.RawMessage(`{"port":80}`),
	}}

	got := verifyJSONPatchOperations(nil, after, ops, "")
	if got[0]["status"] != "array_append_not_checked" {
		t.Fatalf("append verification = %#v, want array_append_not_checked", got[0])
	}
}

func TestVerifyJSONPatchOperationsNestedNumbers(t *testing.T) {
	after := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"ports": []any{map[string]any{
			"port":       int64(80),
			"targetPort": int64(8080),
		}}},
	}}
	ops := []jsonPatchOperation{{
		Op:    "replace",
		Path:  "/spec/ports/0",
		Value: json.RawMessage(`{"port":80,"targetPort":8080}`),
	}}

	got := verifyJSONPatchOperations(nil, after, ops, "")
	if got[0]["status"] != "present" || got[0]["value_matched"] != true {
		t.Fatalf("nested number verification = %#v, want present + value_matched", got[0])
	}
}

func TestVerifyJSONPatchOperationsPresentValueMismatch(t *testing.T) {
	after := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"dnsPolicy": "None"},
	}}
	ops := []jsonPatchOperation{{
		Op:    "replace",
		Path:  "/spec/dnsPolicy",
		Value: json.RawMessage(`"ClusterFirst"`),
	}}

	got := verifyJSONPatchOperations(nil, after, ops, "")
	if got[0]["status"] != "present_value_mismatch" || got[0]["value_matched"] != false {
		t.Fatalf("replace verification = %#v, want present_value_mismatch + value_matched=false", got[0])
	}
}

func TestGetJSONPointerValueEscaping(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"labels": map[string]any{
				"app.kubernetes.io/name": "frontend",
				"a~b":                    "tilde",
			},
		},
	}}

	if got, ok := getJSONPointerValue(obj, "/metadata/labels/app.kubernetes.io~1name"); !ok || got != "frontend" {
		t.Fatalf("slash-escaped pointer = (%v, %v), want frontend true", got, ok)
	}
	if got, ok := getJSONPointerValue(obj, "/metadata/labels/a~0b"); !ok || got != "tilde" {
		t.Fatalf("tilde-escaped pointer = (%v, %v), want tilde true", got, ok)
	}
}

func TestParsePatchType(t *testing.T) {
	if _, err := parsePatchType("json"); err != nil {
		t.Fatalf("json patch type: %v", err)
	}
	if _, err := parsePatchType("merge"); err != nil {
		t.Fatalf("merge patch type: %v", err)
	}
	if _, err := parsePatchType("strategic"); err != nil {
		t.Fatalf("strategic patch type: %v", err)
	}
	if _, err := parsePatchType("bogus"); err == nil {
		t.Fatal("unknown patch type should be rejected")
	}
}

func TestStrategicPatchSupportedOnlyForBuiltins(t *testing.T) {
	deployGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	if !strategicPatchSupported("Deployment", "apps", deployGVR) {
		t.Fatal("Deployment/apps should support strategic patch")
	}
	crdGVR := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	if strategicPatchSupported("Widget", "example.com", crdGVR) {
		t.Fatal("CRD should not support strategic patch")
	}
}

func TestResourceDisplayName(t *testing.T) {
	if got := resourceDisplayName("prod", "cfg"); got != "prod/cfg" {
		t.Fatalf("namespaced display = %q, want prod/cfg", got)
	}
	if got := resourceDisplayName("", "node-1"); got != "node-1" {
		t.Fatalf("cluster-scoped display = %q, want node-1", got)
	}
}
