package server

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

func TestWriteJSONMatchesStock(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 654000000, time.FixedZone("east", 3600))),
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0.125"), corev1.ResourceMemory: resource.MustParse("1.5Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2e3"), corev1.ResourceMemory: resource.MustParse("9223372036854775808")},
			},
			LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromInt32(8080)}}},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromString("http")}}},
		}}},
	}
	schema := spec.StringProperty()
	schema.Description = "<OpenAPI>"
	schema.Default = "string"
	schema.Extensions = spec.Extensions{"x-z": true, "x-a": "a"}

	tests := []struct {
		name  string
		value any
	}{
		{"pods", []corev1.Pod{*pod, {}}},
		{"unstructured", &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.io/v1", "kind": "Widget",
			"spec": map[string]any{"z": []any{int64(1), false, nil}, "a": "<>&"},
		}}},
		{"nil", nil},
		{"nil slice", []string(nil)},
		{"empty slice", []string{}},
		{"nil map", map[string]string(nil)},
		{"empty map", map[string]string{}},
		{"html and UTF-8", map[string]string{"z": "<>&\u2028\u2029", "a\xff": "\xff\uFFFD"}},
		{"invalid UTF-8 probe port", intstr.FromString("<>&\xff")},
		{"kube-openapi marshaler", *schema},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			(&Server{}).writeJSON(rec, tt.value)
			want := stockJSONResponse(t, tt.value)
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || !bytes.Equal(rec.Body.Bytes(), want) {
				t.Fatalf("response = %d %v %q, want 200 application/json %q", rec.Code, rec.Header(), rec.Body, want)
			}
		})
	}
}

func stockJSONResponse(t *testing.T, value any) []byte {
	t.Helper()
	var want bytes.Buffer
	if err := json.NewEncoder(&want).Encode(normalizeNilSlice(value)); err != nil {
		t.Fatal(err)
	}
	return want.Bytes()
}

func TestWriteJSONEncodeError(t *testing.T) {
	value := []any{strings.Repeat("prefix", 1<<16), math.NaN()}
	var stock bytes.Buffer
	if err := json.NewEncoder(&stock).Encode(value); err == nil || stock.Len() != 0 {
		t.Fatalf("stock encode error = %v, wrote %d bytes", err, stock.Len())
	}
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusAccepted)
	(&Server{}).writeJSON(rec, value)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Content-Type") != "application/json" || rec.Body.Len() != 0 {
		t.Fatalf("encode error response = %d %v, %d body bytes", rec.Code, rec.Header(), rec.Body.Len())
	}
}

func TestWriteJSONConcurrent(t *testing.T) {
	var server Server
	var wg sync.WaitGroup
	start := make(chan struct{})
	for worker := range 12 {
		large := []any{worker, strings.Repeat("payload<", 1<<16)}
		small := map[string]int{"worker": worker}
		bad := []any{large, math.Inf(1)}
		values := []any{large, small, bad}
		want := [][]byte{stockJSONResponse(t, large), stockJSONResponse(t, small), nil}
		wg.Go(func() {
			<-start
			for i := range 6 {
				n := (worker + i) % len(values)
				rec := httptest.NewRecorder()
				server.writeJSON(rec, values[n])
				if !bytes.Equal(rec.Body.Bytes(), want[n]) {
					t.Errorf("worker %d case %d: wrote %d bytes, expected %d matching bytes", worker, n, rec.Body.Len(), len(want[n]))
				}
			}
		})
	}
	close(start)
	wg.Wait()
}

type writeJSONSink struct {
	header http.Header
	size   int
}

func (w *writeJSONSink) Header() http.Header { return w.header }
func (w *writeJSONSink) WriteHeader(int)     {}
func (w *writeJSONSink) Write(p []byte) (int, error) {
	w.size = len(p)
	return len(p), nil
}

func TestWriteJSONRetainedMemory(t *testing.T) {
	const slots, retainedCap = 4, 16 << 20
	if cap(jsonResponseBuffers) != slots {
		t.Fatalf("pool holds up to %d buffers, want %d", cap(jsonResponseBuffers), slots)
	}
	for range len(jsonResponseBuffers) {
		<-jsonResponseBuffers
	}
	var server Server
	sink := &writeJSONSink{header: make(http.Header)}
	server.writeJSON(sink, strings.Repeat("x", retainedCap))
	if sink.size != retainedCap+3 || len(jsonResponseBuffers) != 0 {
		t.Fatalf("oversized payload wrote %d bytes and retained %d buffers", sink.size, len(jsonResponseBuffers))
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	payload := []string{strings.Repeat("x", 1<<20)}
	for range 2 * slots {
		wg.Go(func() {
			<-start
			server.writeJSON(&writeJSONSink{header: make(http.Header)}, payload)
		})
	}
	close(start)
	wg.Wait()
	retained := len(jsonResponseBuffers)
	if retained == 0 || retained > slots {
		t.Fatalf("pool retained %d buffers after concurrent responses", retained)
	}
	for range retained {
		if capacity := (<-jsonResponseBuffers).Cap(); capacity > retainedCap {
			t.Errorf("pool retained a %d-byte buffer, want at most %d", capacity, retainedCap)
		}
	}
}

func TestWriteJSONAllocations(t *testing.T) {
	type row struct {
		Message string
		Ports   [6]intstr.IntOrString
	}
	rows := make([]row, 1024)
	item := row{Message: strings.Repeat("payload-", 384)}
	for i := range item.Ports {
		item.Ports[i] = intstr.FromInt32(int32(8080 + i))
	}
	for i := range rows {
		rows[i] = item
	}
	var server Server
	sink := &writeJSONSink{header: make(http.Header)}
	server.writeJSON(sink, rows)
	if sink.size < 3<<20 {
		t.Fatalf("eviction fixture wrote only %d bytes", sink.size)
	}
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			server.writeJSON(sink, rows)
		}
	})
	const maxBytesPerOp = 2 << 20
	t.Logf("large response allocated %d B/op", result.AllocedBytesPerOp())
	if got := result.AllocedBytesPerOp(); got > maxBytesPerOp {
		t.Fatalf("large response allocated %d B/op, want at most %d", got, maxBytesPerOp)
	}
}
