package main

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
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
