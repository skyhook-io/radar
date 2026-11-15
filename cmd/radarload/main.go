package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type config struct {
	URL      string        `json:"url"`
	PID      int           `json:"pid"`
	Duration time.Duration `json:"duration_ns"`
	// RuntimeEvery spaces the Go heap and goroutine reads: producing a heap
	// profile costs the target real CPU, so sampling it often skews CPU.
	RuntimeEvery time.Duration `json:"runtime_every_ns"`
	SSE          int           `json:"sse"`
	Askers       int           `json:"askers"`
	QPS          float64       `json:"qps"`
	PerKind      int           `json:"per_kind"`
	Seed         int64         `json:"seed"`
	User         string        `json:"user,omitempty"`
	Groups       string        `json:"groups,omitempty"`
	UserHeader   string        `json:"user_header"`
	GroupsHeader string        `json:"groups_header"`
	Timeout      time.Duration `json:"request_timeout_ns"`
	ReadyTimeout time.Duration `json:"ready_timeout_ns"`
	Out          string        `json:"out"`
	SubjectsFrom string        `json:"subjects_from,omitempty"`
	Label        string        `json:"label,omitempty"`
}

type result struct {
	SubjectProbeErrors      map[string]int           `json:"subject_probe_errors"`
	NeighborhoodSubjects    []subject                `json:"neighborhood_subjects"`
	NeighborhoodProbeErrors map[string]int           `json:"neighborhood_probe_errors"`
	Schema                  int                      `json:"schema"`
	Config                  config                   `json:"config"`
	Started                 time.Time                `json:"started"`
	Ended                   time.Time                `json:"ended"`
	Build                   map[string]string        `json:"radar_build"`
	Subjects                []subject                `json:"subjects"`
	Inventory               []inventory              `json:"inventory"`
	Notes                   []string                 `json:"notes"`
	Samples                 []sample                 `json:"samples"`
	Summary                 summary                  `json:"summary"`
	Profiles                map[string]profileResult `json:"profiles"`
}

type profileResult struct {
	Path    string    `json:"path,omitempty"`
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
}

func main() {
	if err := command(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "radarload:", err)
		os.Exit(1)
	}
}

func command(args []string) error {
	if len(args) > 0 && args[0] == "compare" {
		if len(args) != 3 {
			return errors.New("usage: radarload compare a.json b.json")
		}
		var a, b result
		if err := readResult(args[1], &a); err != nil {
			return err
		}
		if err := readResult(args[2], &b); err != nil {
			return err
		}
		return compare(os.Stdout, a, b)
	}
	c := config{}
	f := flag.NewFlagSet("radarload", flag.ContinueOnError)
	f.StringVar(&c.URL, "url", "http://localhost:9280", "Radar HTTP base URL (may include a base path)")
	f.IntVar(&c.PID, "pid", 0, "local Radar process PID (not the go run wrapper)")
	f.DurationVar(&c.Duration, "duration", 10*time.Minute, "measurement duration; minimum 35s for CPU profiling")
	f.DurationVar(&c.RuntimeEvery, "runtime-every", 30*time.Second, "how often to read Go heap and goroutine counts from pprof (RSS and CPU are read every 2s)")
	f.IntVar(&c.SSE, "sse", 3, "concurrent SSE connections")
	f.IntVar(&c.Askers, "askers", 4, "concurrent request workers")
	f.Float64Var(&c.QPS, "qps", 20, "combined offered requests/second; saturated slots are counted as dropped")
	f.IntVar(&c.PerKind, "per-kind", 100, "maximum sampled subjects per kind")
	f.Int64Var(&c.Seed, "seed", 1, "deterministic inventory and request seed")
	f.StringVar(&c.User, "user", "", "optional proxy identity")
	f.StringVar(&c.Groups, "groups", "", "optional comma-separated proxy groups")
	f.StringVar(&c.UserHeader, "user-header", "X-Forwarded-User", "proxy username header")
	f.StringVar(&c.GroupsHeader, "groups-header", "X-Forwarded-Groups", "proxy groups header")
	f.DurationVar(&c.Timeout, "request-timeout", 15*time.Second, "request timeout (stream/profile use separate limits)")
	f.DurationVar(&c.ReadyTimeout, "ready-timeout", 2*time.Minute, "wait for connected and synced caches")
	f.StringVar(&c.Out, "out", "result.json", "JSON output; profiles use the same filename stem")
	f.StringVar(&c.SubjectsFrom, "subjects-from", "", "reuse subjects from a prior result for exact replay")
	f.StringVar(&c.Label, "label", "", "build label, e.g. git SHA (server reports version, not git SHA)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if c.PID <= 0 || c.Duration < 35*time.Second || c.SSE < 0 || c.Askers < 1 || c.QPS <= 0 || c.QPS > 1e6 || math.IsNaN(c.QPS) || math.IsInf(c.QPS, 0) || c.PerKind < 1 || c.Timeout <= 0 || c.ReadyTimeout <= 0 || c.RuntimeEvery < 2*time.Second {
		return errors.New("require -pid > 0, -duration >= 35s, -sse >= 0, -askers >= 1, 0 < -qps <= 1000000, positive limits/timeouts")
	}
	if err := os.MkdirAll(filepath.Dir(c.Out), 0755); err != nil {
		return err
	}
	client, err := newClient(c)
	if err != nil {
		return err
	}
	defer client.http.CloseIdleConnections()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, c, client)
}

func readResult(path string, r *result) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, r); err != nil {
		return err
	}
	if r.Schema != 1 {
		return fmt.Errorf("%s: unsupported result schema %d", path, r.Schema)
	}
	return nil
}

func run(ctx context.Context, c config, client *radarClient) error {
	build, err := client.waitReady(ctx)
	if err != nil {
		return err
	}
	subjects, inv, notes, err := client.subjects(ctx)
	if err != nil {
		return err
	}
	var neighborhoods []subject
	if c.SubjectsFrom != "" {
		var old result
		if err = readResult(c.SubjectsFrom, &old); err != nil {
			return err
		}
		subjects = old.Subjects
		neighborhoods = old.NeighborhoodSubjects
		notes = append(notes, "Subjects replayed from "+c.SubjectsFrom)
	}
	subjects, subjectProbeErrors := client.readableSubjects(ctx, subjects)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.SubjectsFrom != "" {
		readable := make(map[subject]bool, len(subjects))
		for _, s := range subjects {
			readable[s] = true
		}
		var retained []subject
		for _, s := range neighborhoods {
			if readable[s] {
				retained = append(retained, s)
			}
		}
		neighborhoods = retained
	}
	if len(subjects) == 0 {
		return errors.New("no readable subjects; inspect identity, inventory permissions and cache readiness")
	}
	probed, probeErrors := client.neighborhoodSubjects(ctx, subjects)
	if c.SubjectsFrom == "" {
		neighborhoods = probed
	} else {
		valid := make(map[subject]bool, len(probed))
		for _, s := range probed {
			valid[s] = true
		}
		for _, s := range neighborhoods {
			if !valid[s] {
				return fmt.Errorf("replayed neighborhood root is no longer readable: %s", s.Key())
			}
		}
	}
	if len(neighborhoods) == 0 {
		return errors.New("no usable neighborhood roots; cannot exercise required neighborhood workload")
	}
	initial := client.sample(ctx, time.Time{}, c.PID, true)
	if len(initial.Errors) > 0 {
		return fmt.Errorf("sampling preflight failed: %v; pprofEnabled in internal/server/server.go requires a local listener, auth, or RADAR_PPROF=true (never Cloud)", initial.Errors)
	}
	r := result{SubjectProbeErrors: subjectProbeErrors, Schema: 1, Config: c, Build: build, Subjects: subjects, NeighborhoodSubjects: neighborhoods, NeighborhoodProbeErrors: probeErrors, Inventory: inv, Notes: append(notes,
		"PauseTotalNs is absent from standard Go heap text; GC pause deltas use the PauseNs ring (unavailable if more than 256 GCs occur between samples).",
		"REST and AI neighborhoods share /api/ai/neighborhood; REST uses profile=all&hops=2, AI uses profile=auto&hops=1.",
		"Read-only load does not generate cluster changes; SSE broadcasts depend on the target's activity.",
	), Profiles: map[string]profileResult{}}
	r.Started = initial.At
	initial.Elapsed = 0
	r.Samples = append(r.Samples, initial)
	fmt.Fprintf(os.Stderr, "Ready: %d subjects (%d neighborhood roots), %d SSE, %d workers, %.2f QPS for %s\n", len(subjects), len(neighborhoods), c.SSE, c.Askers, c.QPS, c.Duration)
	loadCtx, cancel := context.WithTimeout(ctx, c.Duration)
	counters, done := startWorkload(loadCtx, client, subjects, neighborhoods)
	profileDone := make(chan profileResult, 1)
	go func() {
		t := time.NewTimer((c.Duration - 30*time.Second) / 2)
		defer t.Stop()
		select {
		case <-loadCtx.Done():
			profileDone <- profileResult{Error: loadCtx.Err().Error()}
			return
		case <-t.C:
		}
		profileDone <- client.profile(loadCtx, c.Out, "cpu", "/debug/pprof/profile?seconds=30")
	}()
	ticker := time.NewTicker(2 * time.Second)
	var lastRuntime time.Time
	r.Samples = append(r.Samples, sampleWindow(loadCtx, ticker.C, func(ctx context.Context) sample {
		runtime := time.Since(lastRuntime) >= c.RuntimeEvery
		if runtime {
			lastRuntime = time.Now()
		}
		return client.sample(ctx, r.Started, c.PID, runtime)
	})...)
	r.Ended = time.Now()
	ticker.Stop()
	cancel()
	<-done
	r.Profiles["cpu"] = <-profileDone
	finalCtx, finalCancel := context.WithTimeout(ctx, c.Timeout)
	defer finalCancel()
	r.Profiles["heap"] = client.profile(finalCtx, c.Out, "heap", "/debug/pprof/heap")
	r.Summary = summarize(r.Samples, counters, r.Ended.Sub(r.Started).Seconds())
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(c.Out, append(b, '\n'), 0644); err != nil {
		return err
	}
	sb, _ := json.MarshalIndent(r.Summary, "", "  ")
	fmt.Println(string(sb))
	fmt.Fprintln(os.Stderr, "Saved", c.Out)
	var failures []error
	for _, s := range r.Samples {
		if len(s.Errors) > 0 {
			failures = append(failures, fmt.Errorf("sample at %.1fs: %v", s.Elapsed, s.Errors))
		}
	}
	for k, p := range r.Profiles {
		if p.Error != "" {
			failures = append(failures, fmt.Errorf("%s profile: %s", k, p.Error))
		}
	}
	if r.Summary.SSE.Errors > 0 {
		failures = append(failures, errors.New("SSE connections failed; inspect summary.sse"))
	}
	if ctx.Err() != nil {
		failures = append(failures, ctx.Err())
	}
	return errors.Join(failures...)
}
