package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseHeap(t *testing.T) {
	text := "heap profile: 1: 2 [3: 4] @ heap/1048576\n# HeapAlloc = 1024\n# HeapSys = 8192\n# HeapInuse = 4096\n# NumGC = 257\n# PauseNs = ["
	for i := range 256 {
		text += fmt.Sprintf("%d ", i+1)
	}
	text += "]\n"
	h, err := parseHeap(text)
	if err != nil {
		t.Fatal(err)
	}
	if h.Alloc != 1024 || h.Inuse != 4096 || h.Sys != 8192 || h.GC != 257 || h.PauseTotal != nil {
		t.Fatalf("bad heap: %+v", h)
	}
	d, err := pauseDelta(&heapStats{GC: 255}, h)
	if err != nil || d != 257 {
		t.Fatalf("wrap delta: %d, %v", d, err)
	}
	if _, err := pauseDelta(&heapStats{GC: 0}, h); err == nil {
		t.Fatal("overwritten ring reported as exact")
	}
	explicit, err := parseHeap(text + "# PauseTotalNs = 1000\n")
	if err != nil || explicit.PauseTotal == nil || *explicit.PauseTotal != 1000 {
		t.Fatalf("explicit total: %+v, %v", explicit, err)
	}
	for _, bad := range []string{"<html>SPA</html>", strings.Replace(text, "# HeapAlloc = 1024\n", "", 1), strings.Replace(text, "HeapInuse = 4096", "HeapInuse = bad", 1)} {
		if _, err := parseHeap(bad); err == nil {
			t.Fatal("accepted invalid/missing header")
		}
	}
}

func TestCompareMath(t *testing.T) {
	for _, tc := range []struct {
		a, b, d, p float64
		defined    bool
	}{{100, 75, -25, -25, true}, {100, 125, 25, 25, true}, {0, 0, 0, 0, true}, {0, 5, 5, 0, false}} {
		d, p := delta(tc.a, tc.b)
		if d != tc.d || (p != nil) != tc.defined || (p != nil && *p != tc.p) {
			t.Fatalf("delta(%v,%v): %v %v", tc.a, tc.b, d, p)
		}
	}
	v := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	d := describe(v)
	if d.Mean != 10.5 || d.P95 != 19 || d.Peak != 20 || percentile(v, .99) != 20 {
		t.Fatalf("bad distribution: %+v", d)
	}
	a := result{Summary: summary{RSS: distribution{Count: 1, Mean: 100 << 20, P95: 100 << 20, Peak: 100 << 20}}}
	b := a
	b.Summary.RSS.Mean = 75 << 20
	var out bytes.Buffer
	if err := compare(&out, a, b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "-25.00%") || !strings.Contains(out.String(), "n/a") {
		t.Fatal(out.String())
	}
}

func TestParseCPU(t *testing.T) {
	for s, want := range map[string]float64{"00:01.23": 1.23, "12:34": 754, "02:03:04": 7384, "1-02:03:04": 93784} {
		got, err := parseCPU(s)
		if err != nil || math.Abs(got-want) > 1e-6 {
			t.Fatalf("%s: %v, %v", s, got, err)
		}
	}
	for _, s := range []string{"oops", "00:NaN", "-1:00", "00:Inf"} {
		if _, err := parseCPU(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}

type subjectProbeTransport func(*http.Request) (*http.Response, error)

func (f subjectProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadableSubjectsRejectsListedAndReplayedSubjects(t *testing.T) {
	pod := subject{Kind: "Pod", Namespace: "default", Name: "readable"}
	secret := subject{Kind: "Secret", Namespace: "default", Name: "denied"}
	node := subject{Kind: "Node", Name: "node"}
	deleted := subject{Kind: "Widget", Group: "example.io", Namespace: "default", Name: "deleted"}
	c, err := newClient(config{URL: "http://radar.invalid/prefix", Timeout: time.Second, PerKind: 10, User: "reader", Groups: "viewers", UserHeader: "X-User", GroupsHeader: "X-Groups"})
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = subjectProbeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-User") != "reader" || r.Header.Get("X-Groups") != "viewers" {
			t.Fatal("configured identity missing from request")
		}
		status, body := 200, `[]`
		switch r.URL.Path {
		case "/prefix/api/resources/Pod":
			body = `{"items":[{"apiVersion":"v1","metadata":{"namespace":"default","name":"readable"}}]}`
		case "/prefix/api/resources/Secret":
			body = `{"items":[{"apiVersion":"v1","metadata":{"namespace":"default","name":"denied"}}]}`
		case "/prefix/api/resources/Node":
			body = `{"items":[{"apiVersion":"v1","metadata":{"name":"node"}}]}`
		case "/prefix/api/resources/Secret/default/denied":
			status, body = 403, `{"error":"forbidden"}`
		case "/prefix/api/resources/Widget/default/deleted":
			if r.URL.Query().Get("group") != "example.io" {
				t.Fatal("group lost from resource GET")
			}
			status, body = 404, `{"error":"not found"}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	listed, _, _, err := c.subjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 {
		t.Fatalf("expected listed subjects including the denied Secret: %+v", listed)
	}
	readable, rejected := c.readableSubjects(context.Background(), listed)
	if !reflect.DeepEqual(rejected, map[string]int{"Secret.": 1}) || len(readable) != 2 {
		t.Fatalf("unreadable listed subject kept: %+v rejected=%v", readable, rejected)
	}
	old := result{Schema: 1, Subjects: []subject{pod, secret, node, deleted}}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "replay.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var replay result
	if err := readResult(path, &replay); err != nil {
		t.Fatal(err)
	}
	readable, rejected = c.readableSubjects(context.Background(), replay.Subjects)
	if !reflect.DeepEqual(readable, []subject{pod, node}) || !reflect.DeepEqual(rejected, map[string]int{"Secret.": 1, "Widget.example.io": 1}) {
		t.Fatalf("replay rejections not counted: %+v rejected=%v", readable, rejected)
	}
	data, err = json.Marshal(result{SubjectProbeErrors: rejected})
	if err != nil || !strings.Contains(string(data), `"subject_probe_errors":{"Secret.":1,"Widget.example.io":1}`) {
		t.Fatalf("rejections missing from result: %s err=%v", data, err)
	}
}

func TestRunPreflightsFreshAndReplayedSubjectsBeforeSampling(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprint("replay=", replay), func(t *testing.T) {
			cfg := config{URL: "http://radar.invalid", PID: os.Getpid(), Timeout: time.Second, ReadyTimeout: time.Second, PerKind: 10}
			if replay {
				cfg.SubjectsFrom = filepath.Join(t.TempDir(), "prior.json")
				old := result{Schema: 1, Subjects: []subject{{Kind: "Pod", Name: "ok", Namespace: "default"}, {Kind: "Secret", Name: "denied", Namespace: "default"}}}
				old.NeighborhoodSubjects = old.Subjects
				data, err := json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cfg.SubjectsFrom, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := newClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			gets := map[string]int{}
			c.http.Transport = subjectProbeTransport(func(r *http.Request) (*http.Response, error) {
				status, body := 200, `[]`
				path := r.URL.Path
				gets[path]++
				switch path {
				case "/api/connection":
					body = `{"state":"connected"}`
				case "/api/diagnostics":
					body = `{"informers":{"syncStatus":{"phase":"complete"}}}`
				case "/api/resources/Pod":
					body = `{"items":[{"metadata":{"namespace":"default","name":"ok"}}]}`
				case "/api/resources/Secret":
					body = `{"items":[{"metadata":{"namespace":"default","name":"denied"}}]}`
				case "/api/resources/Secret/default/denied":
					status = 403
				case "/api/ai/neighborhood/Pod/default/ok":
					body = `{"subgraph":{"nodes":[{}]}}`
				case "/debug/pprof/heap", "/debug/pprof/goroutine":
					if gets["/api/resources/Secret/default/denied"] != 1 || gets["/api/resources/Pod/default/ok"] != 1 {
						t.Fatal("sampling started before all subject GET preflights")
					}
					status = 503
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			if err := run(context.Background(), cfg, c); err == nil || !strings.Contains(err.Error(), "sampling preflight failed") {
				t.Fatalf("failed before sampling preflight: %v", err)
			}
			if gets["/api/ai/neighborhood/Secret/default/denied"] != 0 {
				t.Fatal("unreadable subject entered neighborhood preflight")
			}
		})
	}
}

func TestSampleWindowExcludesSampleCompletedAfterCancellation(t *testing.T) {
	start := time.Now()
	makeSample := func(seconds int, value uint64) sample {
		rss, cpu, pause := value, float64(value), value*10
		return sample{At: start.Add(time.Duration(seconds) * time.Second), RSS: &rss, CPU: &cpu, Heap: &heapStats{Inuse: value, GC: value, PauseTotal: &pause}}
	}
	sequence := []sample{makeSample(2, 12), makeSample(4, 14), makeSample(7, 9999)}
	ticks := make(chan time.Time, len(sequence))
	for range sequence {
		ticks <- start
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	samples := []sample{makeSample(0, 10)}
	samples = append(samples, sampleWindow(ctx, ticks, func(context.Context) sample {
		s := sequence[calls]
		calls++
		if calls == len(sequence) {
			cancel()
		}
		return s
	})...)
	if calls != 3 || len(samples) != 3 || !samples[len(samples)-1].At.Equal(start.Add(4*time.Second)) {
		t.Fatalf("post-window sample kept: calls=%d samples=%+v", calls, samples)
	}
	s := summarize(samples, &workloadCounters{}, 6)
	if s.RSS.Count != 3 || s.RSS.Mean != 12 || s.RSS.Peak != 14 || s.RSS.P95 != 14 || s.HeapInuse != s.RSS || s.CPUSeconds == nil || *s.CPUSeconds != 4 || s.CPUPerMinute == nil || *s.CPUPerMinute != 60 || s.GCCount == nil || *s.GCCount != 4 || s.PauseNS == nil || *s.PauseNS != 40 {
		t.Fatalf("summary includes post-window values: %+v", s)
	}
}
