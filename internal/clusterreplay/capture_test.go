package clusterreplay

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestCapturePagedSanitizedReplay(t *testing.T) {
	resources := []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Name: "events", Kind: "Event", Verbs: []string{"list"}},
		{Name: "pods/status", Kind: "Pod", Verbs: []string{"list"}},
	}}, {GroupVersion: "coordination.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "leases", Kind: "Lease", Verbs: []string{"list"}}}}, {GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"list", "watch"}}}}}
	d := &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{}}
	d.Resources = resources
	c := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "secrets"}: "SecretList", {Version: "v1", Resource: "configmaps"}: "ConfigMapList", {Group: "example.io", Version: "v1", Resource: "widgets"}: "WidgetList"})
	calls := map[string]int{}
	recorder := &recordingDynamic{Interface: c, options: map[string][]metav1.ListOptions{}}
	c.PrependReactor("list", "*", func(a clienttesting.Action) (bool, runtime.Object, error) {

		name := a.GetResource().Resource
		calls[name]++
		options := recorder.options[name][len(recorder.options[name])-1]
		if options.Limit != 500 || a.GetNamespace() != "" {
			t.Fatalf("not a paged cluster-wide list: %#v", a)
		}
		obj := unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "one", "managedFields": []any{}, "annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "TOKEN_SENTINEL"}}, "data": map[string]any{"credential": "TOKEN_SENTINEL"}, "stringData": map[string]any{"key": "TOKEN_SENTINEL"}, "spec": map[string]any{"token": "TOKEN_SENTINEL", "env": []any{map[string]any{"name": "KEY", "value": "TOKEN_SENTINEL"}}}}}
		if name == "widgets" {
			delete(obj.Object, "data")
			delete(obj.Object, "stringData")
		}
		page := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{obj}}
		if name == "secrets" && calls[name] == 1 {
			page.SetContinue("page-2")
		} else if name == "secrets" && options.Continue != "page-2" {
			t.Fatal("missing continue")
		}
		return true, page, nil
	})
	var buf bytes.Buffer
	n, _, err := Capture(context.Background(), d, recorder, "PRIVATE_CONTEXT_SENTINEL", &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("count=%d", n)
	}
	count := 0
	h, err := Read(&buf, func(o *unstructured.Unstructured) error {
		count++
		data, _ := json.Marshal(o)
		if strings.Contains(string(data), "TOKEN_SENTINEL") {
			t.Fatalf("credential leaked: %s", data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 || len(h.ContextHash) != 64 || h.ContextHash == "PRIVATE_CONTEXT_SENTINEL" {
		t.Fatalf("bad header/count: %+v %d", h, count)
	}
	if calls["events"] != 0 || calls["leases"] != 0 || calls["pods/status"] != 0 {
		t.Fatal(calls)
	}
}

func TestSanitizeConfigMapKeepsKeysAndInputStructure(t *testing.T) {
	o := map[string]any{"kind": "ConfigMap", "data": map[string]any{"token": "abc", "foo": "xyz", "managedFields": "sensitive", "kubectl.kubernetes.io/last-applied-configuration": "sensitive"}, "binaryData": map[string]any{"bin": "aA=="}, "spec": map[string]any{"automountServiceAccountToken": false, "tokenSecretName": "my-token-secret", "serviceAccountToken": map[string]any{"expirationSeconds": int64(3600)}}}
	Sanitize(o)
	if len(o["data"].(map[string]any)) != 4 {
		t.Fatal("data keys lost")
	}
	for _, field := range []string{"data", "binaryData"} {
		for k, v := range o[field].(map[string]any) {
			if v != "" {
				t.Fatalf("%s/%s not cleared", field, k)
			}
		}
	}
	if o["spec"].(map[string]any)["automountServiceAccountToken"] != false {
		t.Fatal("boolean token option corrupted")
	}
	if o["spec"].(map[string]any)["tokenSecretName"] != "my-token-secret" {
		t.Fatal("token secret reference corrupted")
	}
	if _, ok := o["spec"].(map[string]any)["serviceAccountToken"].(map[string]any); !ok {
		t.Fatal("structured reference corrupted")
	}
}

func TestCaptureSkipsAndReportsAnUnreadableKind(t *testing.T) {
	d := &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{}}
	d.Resources = []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "secrets", Kind: "Secret", Verbs: []string{"list"}}}}}
	c := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "secrets"}: "SecretList"})
	c.PrependReactor("list", "*", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, context.DeadlineExceeded })
	_, skipped, err := Capture(context.Background(), d, c, "ctx", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0].Resource != "/v1, Resource=secrets" {
		t.Fatalf("unreadable list not reported: %+v", skipped)
	}
}

// The dynamic fake discards list options when constructing reactor actions.
// Record at its interface boundary to verify the real caller's paging contract.
type recordingDynamic struct {
	dynamic.Interface
	options map[string][]metav1.ListOptions
}
type recordingResource struct {
	dynamic.NamespaceableResourceInterface
	parent   *recordingDynamic
	resource string
}

func (c *recordingDynamic) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &recordingResource{c.Interface.Resource(gvr), c, gvr.Resource}
}
func (c *recordingResource) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	c.parent.options[c.resource] = append(c.parent.options[c.resource], opts)
	return c.NamespaceableResourceInterface.List(ctx, opts)
}
