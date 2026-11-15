package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/clusterreplay"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

func TestRunReportsCaptureGapsAndScopesNoiseByKind(t *testing.T) {
	dir := t.TempDir()
	capture, baseline, output := filepath.Join(dir, "capture.gz"), filepath.Join(dir, "baseline.json"), filepath.Join(dir, "report.json")
	header := clusterreplay.Header{
		CapturedAt: time.Now().UTC(), Discovery: clusterreplay.Discovery{Resources: []*metav1.APIResourceList{}},
		FailedGroups: []string{"broken.io"}, Skipped: []clusterreplay.Skipped{{Resource: "secrets", Error: "forbidden"}},
	}
	f, err := os.Create(capture)
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	enc := json.NewEncoder(z)
	if err := enc.Encode(header); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Pod", "Service"} {
		if err := enc.Encode(map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"namespace": "default", "name": "one"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	base, err := json.Marshal(report{Deltas: []*delta{{Reader: "relationships", Kind: "Pod.", Field: "/owner/name"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseline, base, 0600); err != nil {
		t.Fatal(err)
	}
	server := func(side string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.URL.Path == "/loadtest/status":
				_, _ = io.WriteString(w, `{"ready":true}`)
			case r.URL.Path == "/mcp":
				_, _ = io.WriteString(w, `{"result":{"content":[]}}`)
			case strings.HasPrefix(r.URL.Path, "/api/resources/") && !strings.Contains(r.URL.RawQuery, "include=summary"):
				_ = json.NewEncoder(w).Encode(map[string]any{"relationships": map[string]any{"owner": map[string]any{"name": side}}})
			default:
				_, _ = io.WriteString(w, `{}`)
			}
		}))
	}
	a, b := server("a"), server("b")
	defer a.Close()
	defer b.Close()
	stderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	defer func() { os.Stderr = stderr }()
	os.Stderr = w
	if err := run(a.URL, b.URL, capture, output, 0, a.URL, b.URL, baseline); err != nil {
		t.Fatal(err)
	}
	os.Stderr = stderr
	w.Close()
	warning, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(warning), "1 failed groups, 1 skipped resources") || strings.Count(string(warning), "\n") != 1 {
		t.Fatalf("missing one-line capture warning: %s", warning)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.CaptureGaps, captureGaps{header.FailedGroups, header.Skipped}) {
		t.Fatalf("capture gaps lost: %+v", got.CaptureGaps)
	}
	if got.NoiseCount != 1 || len(got.Noise) != 1 || got.Noise[0].Kind != "Pod." || got.DeltaCount != 1 || len(got.Deltas) != 1 || got.Deltas[0].Kind != "Service." {
		t.Fatalf("noise crossed kinds: noise=%+v deltas=%+v", got.Noise, got.Deltas)
	}
}

func TestLoadNoiseGeneralizesFieldsWithinKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte(`{"deltas":[{"reader":"relationships","kind":"Pod.","field":"/children/[ref|Pod|one]/name"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	noise, err := loadNoise(path)
	if err != nil {
		t.Fatal(err)
	}
	if !noise["relationships\x00Pod.\x00"+fieldPattern("/children/[ref|Pod|two]/name")] || noise["relationships\x00Service.\x00"+fieldPattern("/children/[ref|Pod|two]/name")] {
		t.Fatal(noise)
	}
}
