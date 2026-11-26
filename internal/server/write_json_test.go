package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"maps"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"slices"
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

type pointerJSONMarshaler int

func (*pointerJSONMarshaler) MarshalJSON() ([]byte, error) {
	return []byte(`"json method"`), nil
}

type pointerTextMarshaler int

func (*pointerTextMarshaler) MarshalText() ([]byte, error) {
	return []byte("text method"), nil
}

func TestWriteJSONMatchesStock(t *testing.T) {
	t.Setenv("RADAR_COMPRESS_LEVEL", "1")
	var server Server
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
	marshalers := struct {
		JSON pointerJSONMarshaler
		Text pointerTextMarshaler
	}{JSON: 1, Text: 2}

	largePods := slices.Repeat([]*corev1.Pod{pod}, 256)
	if size := len(stockJSONResponse(t, largePods)); size <= 64<<10 {
		t.Fatalf("large pod list is %d bytes, want more than 64 KiB", size)
	}
	tests := []struct {
		name  string
		value any
	}{
		{"pods", []corev1.Pod{*pod, {}}},
		{"large pod list", largePods},
		{"unstructured", &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.io/v1", "kind": "Widget",
			"spec": map[string]any{"z": []any{int64(1), false, nil}, "a": "<>&"},
		}}},
		{"empty slice", []string{}},
		{"nil map", map[string]string(nil)},
		{"empty map", map[string]string{}},
		{"html and UTF-8", map[string]string{"z": "<>&\u2028\u2029", "a\xff": "\xff\uFFFD"}},
		{"invalid UTF-8 probe port", intstr.FromString("<>&\xff")},
		{"kube-openapi marshaler", *schema},
		{"addressable marshaler fields", &marshalers},
		{"non-addressable marshaler fields", marshalers},
		{"netip address", netip.MustParseAddr("2001:db8::1")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/stock", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(tt.value); err != nil {
					t.Errorf("stock encoder: %v", err)
				}
			})
			mux.HandleFunc("/streamed", func(w http.ResponseWriter, _ *http.Request) {
				server.writeJSON(w, tt.value)
			})
			ts := httptest.NewServer(compressMiddleware()(mux))
			t.Cleanup(ts.Close)
			for _, encoding := range []string{"identity", "gzip"} {
				t.Run(encoding, func(t *testing.T) {
					stock, want := readWriteJSONResponse(t, ts, "/stock", encoding)
					streamed, got := readWriteJSONResponse(t, ts, "/streamed", encoding)
					checkWriteJSONResponse(t, stock, encoding)
					checkWriteJSONResponse(t, streamed, encoding)
					if !bytes.Equal(got, want) {
						t.Fatalf("response differs from stock: got %d bytes, want %d matching bytes", len(got), len(want))
					}
					stock.Header.Del("Date")
					streamed.Header.Del("Date")
					if !maps.EqualFunc(streamed.Header, stock.Header, slices.Equal[[]string]) ||
						streamed.ContentLength != stock.ContentLength ||
						!slices.Equal(streamed.TransferEncoding, stock.TransferEncoding) {
						t.Fatalf("headers or framing differ: streamed %v length=%d transfer=%v, stock %v length=%d transfer=%v",
							streamed.Header, streamed.ContentLength, streamed.TransferEncoding,
							stock.Header, stock.ContentLength, stock.TransferEncoding)
					}
				})
			}
		})
	}
}

func TestWriteJSONNilSlice(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
	}{
		{"nil", nil},
		{"typed nil slice", []string(nil)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			(&Server{}).writeJSON(rec, tt.value)
			const want = "[]\n"
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != want {
				t.Fatalf("response = %d %v %q, want 200 application/json %q", rec.Code, rec.Header(), rec.Body, want)
			}
		})
	}
}

func readWriteJSONResponse(t *testing.T, ts *httptest.Server, path, encoding string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", encoding)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		reader = zr
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func checkWriteJSONResponse(t *testing.T, resp *http.Response, encoding string) {
	t.Helper()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%s: got %s and headers %v, want 200 application/json", resp.Request.URL.Path, resp.Status, resp.Header)
	}
	if encoding == "identity" {
		encoding = ""
	}
	if got := resp.Header.Get("Content-Encoding"); got != encoding {
		t.Fatalf("%s: Content-Encoding = %q, want %q", resp.Request.URL.Path, got, encoding)
	}
}

func stockJSONResponse(t *testing.T, value any) []byte {
	t.Helper()
	var want bytes.Buffer
	if err := json.NewEncoder(&want).Encode(value); err != nil {
		t.Fatal(err)
	}
	return want.Bytes()
}

func TestWriteJSONEncodeError(t *testing.T) {
	t.Setenv("RADAR_COMPRESS_LEVEL", "1")
	// The prefix exceeds the HTTP/1.1 buffering threshold even with gzip.
	prefix := make([]byte, 256<<10)
	_, _ = rand.NewChaCha8([32]byte{1}).Read(prefix)
	value := []any{prefix, math.NaN()}
	want := stockJSONResponse(t, []any{prefix, nil})
	var server Server
	ts := httptest.NewServer(compressMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		server.writeJSON(w, value)
	})))
	t.Cleanup(ts.Close)
	for _, encoding := range []string{"identity", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previous) })

			resp, body := readWriteJSONResponse(t, ts, "/", encoding)
			checkWriteJSONResponse(t, resp, encoding)
			if !slices.Equal(resp.TransferEncoding, []string{"chunked"}) {
				t.Fatalf("Transfer-Encoding = %v, want chunked", resp.TransferEncoding)
			}
			if len(body) < 64<<10 || !bytes.HasPrefix(want, body) || json.Valid(body) {
				t.Fatalf("got %d bytes, want a truncated JSON prefix", len(body))
			}
			log.SetOutput(previous)
			if !strings.Contains(logs.String(), "Failed to encode JSON response:") || !strings.Contains(logs.String(), "NaN") {
				t.Fatalf("missing encode error in log: %q", logs.String())
			}
		})
	}
}

func TestWriteJSONEarlyEncodeError(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).writeJSON(rec, []any{"prefix", math.NaN()})
	result := rec.Result()
	defer result.Body.Close()
	body, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK || result.Header.Get("Content-Type") != "application/json" || len(body) != 0 {
		t.Fatalf("got %s, headers %v, and %d body bytes, want 200 application/json with an empty body", result.Status, result.Header, len(body))
	}
}

func TestWriteJSONEarlyEncodeErrorAfterWriteHeader(t *testing.T) {
	t.Setenv("RADAR_COMPRESS_LEVEL", "1")
	var server Server
	ts := httptest.NewServer(compressMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		server.writeJSON(w, []any{"prefix", math.NaN()})
	})))
	t.Cleanup(ts.Close)
	for _, encoding := range []string{"identity", "gzip"} {
		t.Run(encoding, func(t *testing.T) {
			resp, body := readWriteJSONResponse(t, ts, "/", encoding)
			if resp.StatusCode != http.StatusAccepted || resp.ContentLength != 0 || len(body) != 0 || resp.Header.Get("Content-Encoding") != "" {
				t.Fatalf("got %s, headers %v, and %d body bytes, want 202 with an empty identity body", resp.Status, resp.Header, len(body))
			}
		})
	}
}

type writeJSONFailingWriter struct {
	*httptest.ResponseRecorder
	writes       int
	writeHeaders int
}

func (w *writeJSONFailingWriter) WriteHeader(int) {
	w.writeHeaders++
}

func (w *writeJSONFailingWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, io.ErrShortWrite
}

func TestWriteJSONWriteError(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value []string
	}{
		{"final flush", []string{"value"}},
		// This response fills the buffer while MarshalWrite is still encoding.
		{"during encoding", slices.Repeat([]string{strings.Repeat("x", 1<<10)}, 256)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &writeJSONFailingWriter{ResponseRecorder: httptest.NewRecorder()}
			(&Server{}).writeJSON(w, tt.value)
			if w.writes != 1 || w.writeHeaders != 0 {
				t.Errorf("got %d Write calls and %d WriteHeader calls, want 1 Write and 0 WriteHeader", w.writes, w.writeHeaders)
			}
		})
	}
}

func TestWriteJSONConcurrent(t *testing.T) {
	var server Server
	var wg sync.WaitGroup
	start := make(chan struct{})
	for worker := range 12 {
		large := slices.Repeat([]string{strings.Repeat("payload<", 128) + string(rune('a'+worker))}, 256)
		small := map[string]int{"worker": worker}
		bad := []any{"prefix", math.Inf(1)}
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
	w.size += len(p)
	return len(p), nil
}

func TestWriteJSONColdHeap(t *testing.T) {
	var server Server
	values := slices.Repeat([]string{strings.Repeat("x", 1021)}, 8<<10)
	measure := func(count int) uint64 {
		data := any(values[:count])
		sink := &writeJSONSink{header: make(http.Header)}
		// Two collections also clear the victim cache of the encoder's sync.Pool.
		runtime.GC()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		server.writeJSON(sink, data)
		runtime.ReadMemStats(&after)
		if want := count*1024 + 2; sink.size != want {
			t.Fatalf("wrote %d bytes, want %d", sink.size, want)
		}
		// TotalAlloc bounds the incremental heap even if a collection occurs
		// during encoding. The sink counts bytes without storing the response.
		return after.TotalAlloc - before.TotalAlloc
	}
	small, large := measure(1<<10), measure(8<<10)
	t.Logf("cold allocations: 1 MiB response %d bytes, 8 MiB response %d bytes", small, large)
	const maxColdBytes, maxGrowth = 512 << 10, 256 << 10
	if max(small, large) > maxColdBytes || large > small+maxGrowth {
		t.Fatalf("cold allocations exceeded %d bytes per response or grew by more than %d bytes", maxColdBytes, maxGrowth)
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
		t.Fatalf("allocation fixture wrote only %d bytes", sink.size)
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
