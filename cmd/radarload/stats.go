package main

import (
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"text/tabwriter"
)

type distribution struct {
	Mean  float64 `json:"mean"`
	P95   float64 `json:"p95"`
	Peak  float64 `json:"peak"`
	Count int     `json:"count"`
}
type latencySummary struct {
	Count      int            `json:"count"`
	Errors     int            `json:"errors"`
	ErrorKinds map[string]int `json:"error_kinds"`
	P50        float64        `json:"p50_ms"`
	P95        float64        `json:"p95_ms"`
	P99        float64        `json:"p99_ms"`
	Bytes      int64          `json:"response_bytes"`
}
type summary struct {
	Seconds      float64                   `json:"measured_seconds"`
	RSS          distribution              `json:"rss_bytes"`
	HeapInuse    distribution              `json:"heap_inuse_bytes"`
	CPUSeconds   *float64                  `json:"cpu_seconds"`
	CPUPerMinute *float64                  `json:"cpu_seconds_per_minute"`
	GCCount      *uint64                   `json:"gc_count"`
	PauseNS      *uint64                   `json:"gc_pause_ns"`
	PauseError   string                    `json:"gc_pause_error,omitempty"`
	SSE          sseStats                  `json:"sse"`
	Offered      int                       `json:"offered_requests"`
	Dropped      int                       `json:"dropped_requests"`
	ActualQPS    float64                   `json:"actual_qps"`
	Endpoints    map[string]latencySummary `json:"endpoints"`
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[max(0, int(math.Ceil(float64(len(sorted))*p))-1)]
}
func describe(v []float64) distribution {
	if len(v) == 0 {
		return distribution{}
	}
	sort.Float64s(v)
	var sum float64
	for _, n := range v {
		sum += n
	}
	return distribution{sum / float64(len(v)), percentile(v, .95), v[len(v)-1], len(v)}
}

func summarize(samples []sample, c *workloadCounters, seconds float64) summary {
	s := summary{Seconds: seconds, Endpoints: map[string]latencySummary{}}
	var rss, heap []float64
	for _, v := range samples {
		if v.RSS != nil {
			rss = append(rss, float64(*v.RSS))
		}
		if v.Heap != nil {
			heap = append(heap, float64(v.Heap.Inuse))
		}
	}
	s.RSS = describe(rss)
	s.HeapInuse = describe(heap)
	if len(samples) >= 2 {
		a, b := samples[0], samples[len(samples)-1]
		if a.CPU != nil && b.CPU != nil && *b.CPU >= *a.CPU {
			delta := *b.CPU - *a.CPU
			s.CPUSeconds = &delta
			rate := delta * 60 / b.At.Sub(a.At).Seconds()
			s.CPUPerMinute = &rate
		}
		if a.Heap != nil && b.Heap != nil && b.Heap.GC >= a.Heap.GC {
			delta := b.Heap.GC - a.Heap.GC
			s.GCCount = &delta
		}
		var pause uint64
		for i := 1; i < len(samples); i++ {
			if samples[i-1].Heap == nil || samples[i].Heap == nil {
				s.PauseError = "heap sample missing"
				continue
			}
			delta, err := pauseDelta(samples[i-1].Heap, samples[i].Heap)
			if err != nil {
				s.PauseError = err.Error()
				continue
			}
			samples[i].GCPauseNS = &delta
			pause += delta
		}
		if s.PauseError == "" {
			s.PauseNS = &pause
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s.SSE = c.SSE
	s.Offered = c.Offered
	s.Dropped = c.Dropped
	var requests int
	for name, d := range c.Endpoints {
		sort.Float64s(d.latencies)
		n := 0
		for _, v := range d.Errors {
			n += v
		}
		s.Endpoints[name] = latencySummary{len(d.latencies), n, d.Errors, percentile(d.latencies, .5), percentile(d.latencies, .95), percentile(d.latencies, .99), d.Bytes}
		requests += len(d.latencies)
	}
	if seconds > 0 {
		s.ActualQPS = float64(requests) / seconds
	}
	return s
}

func delta(a, b float64) (float64, *float64) {
	d := b - a
	if a == 0 {
		if b == 0 {
			z := 0.0
			return d, &z
		}
		return d, nil
	}
	p := d / math.Abs(a) * 100
	return d, &p
}

func compare(out io.Writer, a, b result) error {
	if a.Config.Timeout != b.Config.Timeout || a.Config.UserHeader != b.Config.UserHeader || a.Config.GroupsHeader != b.Config.GroupsHeader || a.Config.Duration != b.Config.Duration || a.Config.SSE != b.Config.SSE || a.Config.Askers != b.Config.Askers || a.Config.QPS != b.Config.QPS || a.Config.Seed != b.Config.Seed || a.Config.User != b.Config.User || a.Config.Groups != b.Config.Groups || !reflect.DeepEqual(a.Subjects, b.Subjects) || !reflect.DeepEqual(a.NeighborhoodSubjects, b.NeighborhoodSubjects) {
		fmt.Fprintln(out, "WARNING: workload configuration or sampled subjects differ")
	}
	fmt.Fprintf(out, "A: %s (%s)\nB: %s (%s)\n", a.Config.Label, a.Build["version"], b.Config.Label, b.Build["version"])
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "Metric\tA\tB\tDelta (B-A)\tChange")
	row := func(name string, a, b *float64) {
		if a == nil || b == nil {
			av, bv := "n/a", "n/a"
			if a != nil {
				av = fmt.Sprintf("%.3f", *a)
			}
			if b != nil {
				bv = fmt.Sprintf("%.3f", *b)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\tn/a\tn/a\n", name, av, bv)
			return
		}
		d, p := delta(*a, *b)
		pct := "n/a"
		if p != nil {
			pct = fmt.Sprintf("%+.2f%%", *p)
		}
		fmt.Fprintf(w, "%s\t%.3f\t%.3f\t%+.3f\t%s\n", name, *a, *b, d, pct)
	}
	value := func(name string, a, b float64) { row(name, &a, &b) }
	dist := func(name string, a, b distribution) {
		if a.Count == 0 || b.Count == 0 {
			row(name, nil, nil)
			return
		}
		value(name+" mean MiB", a.Mean/(1<<20), b.Mean/(1<<20))
		value(name+" p95 MiB", a.P95/(1<<20), b.P95/(1<<20))
		value(name+" peak MiB", a.Peak/(1<<20), b.Peak/(1<<20))
	}
	dist("RSS", a.Summary.RSS, b.Summary.RSS)
	dist("HeapInuse", a.Summary.HeapInuse, b.Summary.HeapInuse)
	row("CPU seconds", a.Summary.CPUSeconds, b.Summary.CPUSeconds)
	row("CPU seconds/minute", a.Summary.CPUPerMinute, b.Summary.CPUPerMinute)
	uintFloat := func(v *uint64) *float64 {
		if v == nil {
			return nil
		}
		n := float64(*v)
		return &n
	}
	row("GC count", uintFloat(a.Summary.GCCount), uintFloat(b.Summary.GCCount))
	pause := func(v *uint64) *float64 {
		n := uintFloat(v)
		if n != nil {
			*n /= 1e6
		}
		return n
	}
	row("GC pause ms", pause(a.Summary.PauseNS), pause(b.Summary.PauseNS))
	value("SSE events", float64(a.Summary.SSE.Events), float64(b.Summary.SSE.Events))
	value("SSE errors", float64(a.Summary.SSE.Errors), float64(b.Summary.SSE.Errors))
	value("Actual QPS", a.Summary.ActualQPS, b.Summary.ActualQPS)
	value("Dropped slots", float64(a.Summary.Dropped), float64(b.Summary.Dropped))
	names := map[string]bool{}
	for n := range a.Summary.Endpoints {
		names[n] = true
	}
	for n := range b.Summary.Endpoints {
		names[n] = true
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)
	for _, n := range ordered {
		av, aok := a.Summary.Endpoints[n]
		bv, bok := b.Summary.Endpoints[n]
		if !aok || !bok {
			row(n+" (missing endpoint)", nil, nil)
			continue
		}
		value(n+" count", float64(av.Count), float64(bv.Count))
		value(n+" errors", float64(av.Errors), float64(bv.Errors))
		value(n+" p50 ms", av.P50, bv.P50)
		value(n+" p95 ms", av.P95, bv.P95)
		value(n+" p99 ms", av.P99, bv.P99)
	}
	return w.Flush()
}
