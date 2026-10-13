package k8score

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestApplyResourceUIDComesFromPersistedResult(t *testing.T) {
	for _, mode := range []string{"create", "apply", "reviewed-create"} {
		for _, dryRun := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/write", true: "/dry-run"}[dryRun], func(t *testing.T) {
				gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
				dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
				resultObject := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "settings", "uid": "server-uid"}}}
				for _, verb := range []string{"create", "patch"} {
					dyn.PrependReactor(verb, "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) { return true, resultObject.DeepCopy(), nil })
				}
				opts := ApplyResourceOptions{Mode: mode, DryRun: dryRun, YAML: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings\n  uid: submitted-uid\n"}
				if mode == "reviewed-create" {
					opts.Mode = "apply"
					opts.ExpectedResourceAbsent = true
				}
				result, err := NewWorkloadManager(dyn, stubDiscovery(t, "ConfigMap", gvr)).ApplyResource(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				want := types.UID("server-uid")
				if dryRun {
					want = ""
				}
				if result.UID != want {
					t.Fatalf("UID=%q, want %q", result.UID, want)
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				if dryRun && strings.Contains(string(encoded), "\"uid\"") {
					t.Fatalf("dry-run leaked unpersisted identity: %s", encoded)
				}
				if !dryRun && !strings.Contains(string(encoded), "\"uid\":\"server-uid\"") {
					t.Fatalf("persisted identity missing: %s", encoded)
				}
			})
		}
	}
}
