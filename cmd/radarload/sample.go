package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type heapStats struct {
	Alloc      uint64  `json:"heap_alloc_bytes"`
	Inuse      uint64  `json:"heap_inuse_bytes"`
	Sys        uint64  `json:"heap_sys_bytes"`
	GC         uint64  `json:"num_gc"`
	PauseTotal *uint64 `json:"pause_total_ns"`
	pauses     []uint64
}
type sample struct {
	At         time.Time  `json:"at"`
	Elapsed    float64    `json:"elapsed_seconds"`
	RSS        *uint64    `json:"rss_bytes"`
	CPU        *float64   `json:"cpu_seconds"`
	Heap       *heapStats `json:"heap"`
	Goroutines *int       `json:"goroutines"`
	GCPauseNS  *uint64    `json:"gc_pause_since_previous_ns"`
	Errors     []string   `json:"errors,omitempty"`
}

func parseHeap(text string) (*heapStats, error) {
	h := &heapStats{}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "# ") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "# "), " = ")
		if !ok {
			continue
		}
		if key == "PauseNs" {
			value = strings.TrimSpace(value)
			if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
				return nil, errors.New("invalid PauseNs array")
			}
			for _, v := range strings.Fields(strings.Trim(value, "[]")) {
				n, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("PauseNs: %w", err)
				}
				h.pauses = append(h.pauses, n)
			}
			continue
		}
		var target *uint64
		switch key {
		case "HeapAlloc":
			target = &h.Alloc
		case "HeapInuse":
			target = &h.Inuse
		case "HeapSys":
			target = &h.Sys
		case "NumGC":
			target = &h.GC
		case "PauseTotalNs":
			h.PauseTotal = new(uint64)
			target = h.PauseTotal
		default:
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		*target = n
		seen[key] = true
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for _, key := range []string{"HeapAlloc", "HeapInuse", "HeapSys", "NumGC"} {
		if !seen[key] {
			return nil, fmt.Errorf("pprof heap text missing %s", key)
		}
	}
	return h, nil
}

func parseGoroutines(text string) (int, error) {
	first, _, _ := strings.Cut(text, "\n")
	var n int
	if _, err := fmt.Sscanf(first, "goroutine profile: total %d", &n); err != nil || n < 1 {
		return 0, fmt.Errorf("invalid goroutine profile first line %q", first)
	}
	return n, nil
}

func parseCPU(s string) (float64, error) {
	days := 0.0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.ParseUint(d, 10, 32)
		if err != nil {
			return 0, err
		}
		days = float64(n)
		s = rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid ps CPU time %q", s)
	}
	total := 0.0
	for _, p := range parts {
		n, err := strconv.ParseFloat(p, 64)
		if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, fmt.Errorf("invalid ps CPU component %q", p)
		}
		total = total*60 + n
	}
	return days*86400 + total, nil
}

func processStats(ctx context.Context, pid int) (uint64, float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "ps", "-o", "rss=,time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("ps for PID %d: %w", pid, err)
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected ps output %q", b)
	}
	rss, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	cpu, err := parseCPU(fields[1])
	return rss * 1024, cpu, err
}

func (c *radarClient) sample(ctx context.Context, start time.Time, pid int) sample {
	s := sample{At: time.Now()}
	if !start.IsZero() {
		s.Elapsed = s.At.Sub(start).Seconds()
	}
	rss, cpu, err := processStats(ctx, pid)
	if err != nil {
		s.Errors = append(s.Errors, err.Error())
	} else {
		s.RSS = &rss
		s.CPU = &cpu
	}
	b, err := c.bytes(ctx, "/debug/pprof/heap?debug=1")
	if err == nil {
		s.Heap, err = parseHeap(string(b))
	}
	if err != nil {
		s.Errors = append(s.Errors, "heap: "+err.Error())
	}
	b, err = c.bytes(ctx, "/debug/pprof/goroutine?debug=1")
	if err == nil {
		var n int
		n, err = parseGoroutines(string(b))
		if err == nil {
			s.Goroutines = &n
		}
	}
	if err != nil {
		s.Errors = append(s.Errors, "goroutines: "+err.Error())
	}
	return s
}

func pauseDelta(a, b *heapStats) (uint64, error) {
	if b.GC < a.GC {
		return 0, errors.New("GC counter decreased")
	}
	if a.PauseTotal != nil && b.PauseTotal != nil {
		if *b.PauseTotal < *a.PauseTotal {
			return 0, errors.New("GC pause counter decreased")
		}
		return *b.PauseTotal - *a.PauseTotal, nil
	}
	n := b.GC - a.GC
	if n == 0 {
		return 0, nil
	}
	if len(b.pauses) != 256 || n > 256 {
		return 0, errors.New("GC pause ring unavailable or overwritten between samples")
	}
	var total uint64
	for gc := a.GC; gc < b.GC; gc++ {
		total += b.pauses[gc%256]
	}
	return total, nil
}
