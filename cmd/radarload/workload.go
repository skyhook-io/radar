package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"strings"
	"sync"
	"time"
)

type requestJob struct {
	Endpoint string
	Path     string
}
type endpointData struct {
	latencies []float64
	Errors    map[string]int
	Bytes     int64
}
type sseStats struct {
	Events      int64          `json:"events"`
	Connections int64          `json:"connections"`
	Errors      int64          `json:"errors"`
	ErrorKinds  map[string]int `json:"error_kinds"`
}
type workloadCounters struct {
	mu        sync.Mutex
	Endpoints map[string]*endpointData
	SSE       sseStats
	Offered   int
	Dropped   int
}

func requestFor(slot int, s subject) requestJob {
	switch {
	case slot < 12:
		return requestJob{"resource_get", resourcePath("/api/resources", s)}
	case slot < 14:
		return requestJob{"ai_context", resourcePath("/api/ai/resources", s)}
	case slot < 16:
		return requestJob{"ai_neighborhood", resourcePath("/api/ai/neighborhood", s) + "&profile=auto&hops=1"}
	case slot < 18:
		return requestJob{"rest_neighborhood", resourcePath("/api/ai/neighborhood", s) + "&profile=all&hops=2"}
	case slot == 18:
		return requestJob{"applications", "/api/applications"}
	default:
		return requestJob{"list_summary", "/api/resources/" + url.PathEscape(s.Kind) + "?include=summary&maxItems=50000&group=" + url.QueryEscape(s.Group)}
	}
}

func startWorkload(ctx context.Context, c *radarClient, subjects, neighborhoods []subject) (*workloadCounters, <-chan struct{}) {
	stats := &workloadCounters{Endpoints: map[string]*endpointData{}, SSE: sseStats{ErrorKinds: map[string]int{}}}
	done := make(chan struct{})
	jobs := make(chan requestJob)
	var workers sync.WaitGroup
	for range c.c.Askers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
				start := time.Now()
				resp, err := c.get(reqCtx, job.Path)
				var bytes int64
				var reason string
				if err == nil {
					bytes, err = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if resp.StatusCode != 200 {
						reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
					} else if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
						reason = "non-JSON response"
					}
				}
				if err != nil {
					reason = err.Error()
					if errors.Is(err, context.DeadlineExceeded) {
						reason = "timeout"
					}
					if ctx.Err() != nil {
						reason = "run ended in flight"
					}
				}
				elapsed := time.Since(start).Seconds() * 1000
				cancel()
				stats.mu.Lock()
				d := stats.Endpoints[job.Endpoint]
				if d == nil {
					d = &endpointData{Errors: map[string]int{}}
					stats.Endpoints[job.Endpoint] = d
				}
				d.latencies = append(d.latencies, elapsed)
				d.Bytes += bytes
				if reason != "" {
					d.Errors[reason]++
				}
				stats.mu.Unlock()
			}
		}()
	}
	for range c.c.SSE {
		workers.Add(1)
		go func() { defer workers.Done(); c.stream(ctx, stats) }()
	}
	go func() {
		defer close(done)
		rng := rand.New(rand.NewSource(c.c.Seed))
		order := rng.Perm(20)
		start := time.Now()
		deadline, _ := ctx.Deadline()
		slot := 0
		for {
			target := start.Add(time.Duration(float64(slot) / c.c.QPS * float64(time.Second)))
			if !deadline.IsZero() && !target.Before(deadline) {
				<-ctx.Done()
				close(jobs)
				workers.Wait()
				return
			}
			t := time.NewTimer(time.Until(target))
			select {
			case <-ctx.Done():
				t.Stop()
				close(jobs)
				workers.Wait()
				return
			case <-t.C:
			}
			if ctx.Err() != nil {
				close(jobs)
				workers.Wait()
				return
			}
			endpointSlot := order[slot%20]
			pool := subjects
			if endpointSlot >= 14 && endpointSlot < 18 {
				pool = neighborhoods
			}
			job := requestFor(endpointSlot, pool[rng.Intn(len(pool))])
			stats.mu.Lock()
			stats.Offered++
			stats.mu.Unlock()
			select {
			case jobs <- job:
			default:
				stats.mu.Lock()
				stats.Dropped++
				stats.mu.Unlock()
			}
			slot++
		}
	}()
	return stats, done
}

func (c *radarClient) stream(ctx context.Context, stats *workloadCounters) {
	for ctx.Err() == nil {
		resp, err := c.get(ctx, "/api/events/stream")
		if err == nil {
			if resp.StatusCode != 200 {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			} else if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				err = errors.New("non-SSE response")
			}
			if err == nil {
				stats.mu.Lock()
				stats.SSE.Connections++
				stats.mu.Unlock()
				reader := bufio.NewReaderSize(resp.Body, 64*1024)
				hasData := false
				continuation := false
				for {
					line, readErr := reader.ReadSlice('\n')
					if !continuation {
						if strings.HasPrefix(string(line), "data:") {
							hasData = true
						}
						if len(line) == 1 && line[0] == '\n' || len(line) == 2 && line[0] == '\r' && line[1] == '\n' {
							if hasData {
								stats.mu.Lock()
								stats.SSE.Events++
								stats.mu.Unlock()
							}
							hasData = false
						}
					}
					continuation = errors.Is(readErr, bufio.ErrBufferFull)
					if readErr != nil && !continuation {
						err = readErr
						break
					}
				}
			}
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return
		}
		stats.mu.Lock()
		stats.SSE.Errors++
		stats.SSE.ErrorKinds[err.Error()]++
		stats.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
