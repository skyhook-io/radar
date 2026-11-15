package main

import (
	"encoding/json"
	"github.com/skyhook-io/radar/internal/clusterreplay"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestNormalizationAndDeltas(t *testing.T) {
	a := map[string]any{"owner": map[string]any{"name": "x", "generation": 1}, "children": []any{map[string]any{"kind": "Pod", "name": "b"}, map[string]any{"kind": "Pod", "name": "a"}}, "conditions": []any{"first", "second"}}
	b := map[string]any{"owner": map[string]any{"name": "x", "generation": 2}, "children": []any{map[string]any{"kind": "Pod", "name": "a"}, map[string]any{"kind": "Pod", "name": "b"}}, "conditions": []any{"first", "second"}}
	if !reflect.DeepEqual(normalize(a), normalize(b)) {
		t.Fatal("ref ordering/generation changed equality")
	}
	b["owner"].(map[string]any)["name"] = "y"
	out := map[string][2]any{}
	compare("", normalize(a), normalize(b), out)
	if len(out) != 1 || out["/owner/name"] != [2]any{"x", "y"} {
		t.Fatal(out)
	}
	b["conditions"] = []any{"second", "first"}
	out = map[string][2]any{}
	compare("", normalize(a), normalize(b), out)
	if len(out) != 3 {
		t.Fatal("ordered arrays must retain order", out)
	}
}

func TestMCPTextDecodedAndErrorsRecorded(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("MCP-Protocol-Version") == "" {
			t.Error("bad MCP request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"{\\\"owner\\\":{\\\"name\\\":\\\"a\\\"}}\"}]}}\n\n"))
	}))
	defer s.Close()
	value, err := fetch(s.Client(), s.URL, request{Tool: "get_resource"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(value)
	if string(b) != "{\"content\":[{\"text\":{\"owner\":{\"name\":\"a\"}},\"type\":\"text\"}]}" {
		t.Fatalf("not decoded: %s", b)
	}
}

func TestPlanIncludesAllReadersAndDoesNotCapTrees(t *testing.T) {
	objects := []*unstructured.Unstructured{}
	for _, name := range []string{"first", "second"} {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": name, "namespace": "default"}}})
	}
	requests := plan(objects, clusterreplay.Header{}, 1)
	counts := map[string]int{}
	for _, r := range requests {
		counts[r.Reader]++
	}
	for _, reader := range []string{"relationships", "neighborhood", "ai_context", "mcp_get_resource", "mcp_get_neighborhood", "summary", "applications"} {
		if counts[reader] != 1 {
			t.Fatalf("%s calls=%d", reader, counts[reader])
		}
	}
	if counts["gitops_tree"] != 2 {
		t.Fatal("trees capped", counts)
	}
}

func TestMissingAndNullRemainDifferent(t *testing.T) {
	out := map[string][2]any{}
	compare("", map[string]any{}, map[string]any{"owner": nil}, out)
	if len(out) != 1 || out["/owner"][0] == nil {
		t.Fatal(out)
	}
}

func TestContainerOrderIsNotNormalizedAway(t *testing.T) {
	a := []any{map[string]any{"name": "first", "image": "a"}, map[string]any{"name": "second", "image": "b"}}
	b := []any{a[1], a[0]}
	if reflect.DeepEqual(normalize(a), normalize(b)) {
		t.Fatal("ordered container array sorted as references")
	}
}
