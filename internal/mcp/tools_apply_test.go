package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
