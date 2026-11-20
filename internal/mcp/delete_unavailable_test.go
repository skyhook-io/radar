package mcp

import (
	"context"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"strings"
	"testing"
)

func TestDeleteUnavailableImpactCannotBeConfirmed(t *testing.T) {
	setConnectedForTest(t)
	k8s.ResetResourceCache()
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "held", "namespace": "demo", "uid": "u", "resourceVersion": "1"}}}
	dyn := setupMCPDynamicResource(t, gvr, "ConfigMapList", k8s.APIResource{Version: "v1", Kind: "ConfigMap", Name: "configmaps", Namespaced: true}, obj).(*dynamicfake.FakeDynamicClient)
	calls := 0
	dyn.PrependReactor("delete", "configmaps", func(a clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if len(a.(clienttesting.DeleteAction).GetDeleteOptions().DryRun) == 0 {
			t.Fatal("persisting delete without impact")
		}
		return true, nil, nil
	})
	input := deleteResourceInput{Kind: "ConfigMap", Namespace: "demo", Name: "held"}
	res, _, err := handleDeleteResource(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if token, _ := got["confirm"].(string); token != "" {
		t.Fatal("unavailable preview minted token")
	}
	cascade := got["cascade"].(map[string]any)
	if cascade["unavailableReason"] == nil || cascade["recoveryAction"] == nil || strings.Contains(got["message"].(string), "Retry once") {
		t.Fatalf("missing actionable unavailability: %+v", got)
	}
	no := false
	input.DryRun = &no
	_, _, err = handleDeleteResource(context.Background(), nil, input)
	if err == nil || !strings.Contains(err.Error(), "confirm is required") || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
