package diagnosecli

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"k8s.io/klog/v2"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/cliui"
)

// bootEphemeral starts a temporary in-process Radar for one investigation:
// headless, random port, no ~/.radar/mcp-port claim (a real instance may own
// it), timeline in memory — but the SHARED ai-runs history, so a cold run's
// transcript still shows up in the UI later. Radar's boot logging is captured
// to a tail buffer and surfaced only on failure; the terminal shows a single
// connecting spinner instead.
func bootEphemeral(kubeconfig string) (base string, shutdown func(), err error) {
	tail := &tailBuffer{limit: 64 << 10}
	log.SetOutput(tail) // for the whole process — request logs would drown the transcript
	// client-go logs through klog, which writes DIRECTLY to stderr by default
	// (bypassing stdlib log) — e.g. apiserver deprecation warnings fired by the
	// agent's list calls would stomp the transcript mid-spinner. The server
	// path tames this in main(); the subcommand exits before reaching it, so
	// repeat it here, pointed at the tail buffer.
	klog.InitFlags(nil)
	_ = flag.Set("v", "0")
	_ = flag.Set("logtostderr", "false")
	_ = flag.Set("alsologtostderr", "false")
	klog.SetOutput(tail)
	// chi's request logger holds its OWN logger bound to os.Stdout at package
	// init — redirect it too, or every API call the CLI makes prints a line
	// into the transcript. Must happen before CreateServer builds the router.
	chimiddleware.DefaultLogger = chimiddleware.RequestLogger(&chimiddleware.DefaultLogFormatter{
		Logger: log.New(tail, "", log.LstdFlags), NoColor: true,
	})

	app.DisableMCPPortFile()
	cfg := app.AppConfig{
		Kubeconfig:      kubeconfig,
		Port:            0, // random free port
		NoBrowser:       true,
		HistoryLimit:    1000, // in-memory timeline floor; this instance lives minutes
		MCPEnabled:      true,
		AIHistory:       true,
		TimelineStorage: "memory",
	}
	app.SetGlobals(cfg)
	if err := app.InitializeK8s(cfg); err != nil {
		return "", nil, fmt.Errorf("kubeconfig: %w", err)
	}
	app.RegisterCallbacks(cfg, app.BuildTimelineStoreConfig(cfg))
	srv := app.CreateServer(cfg)

	ready := make(chan struct{})
	go func() {
		if err := srv.StartWithReady(ready); err != nil && !strings.Contains(err.Error(), "closed") {
			log.Printf("ephemeral server error: %v", err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		return "", nil, fmt.Errorf("temporary Radar didn't start listening\n%s", tail.String())
	}
	base = fmt.Sprintf("http://localhost:%d", srv.ActualPort())

	stopSpin := make(chan struct{})
	go bootSpinner(stopSpin) // self-gates on TTY + NO_COLOR
	app.InitializeCluster()

	// Connected = the diagnose endpoints stop returning 503 (requireConnected).
	deadline := time.Now().Add(2 * time.Minute)
	for {
		resp, err := http.Get(base + "/api/diagnose/runs")
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code == http.StatusOK {
				break
			}
			if code == http.StatusNotImplemented {
				close(stopSpin)
				return "", nil, fmt.Errorf("no supported agent CLI found — install Claude Code, Codex, or Cursor")
			}
		}
		if time.Now().After(deadline) {
			close(stopSpin)
			return "", nil, fmt.Errorf("couldn't connect to the cluster\n--- radar log tail ---\n%s", tail.String())
		}
		time.Sleep(500 * time.Millisecond)
	}
	close(stopSpin)

	return base, func() { app.Shutdown(srv) }, nil
}

// bootSpinner is the pre-run wait line ("starting a temporary Radar —
// connecting to cluster… 12s") on stderr; the renderer's own spinner takes
// over once the investigation streams.
func bootSpinner(stop <-chan struct{}) {
	if !cliui.ColorEnabled(os.Stderr) {
		return
	}
	start := time.Now()
	t := time.NewTicker(120 * time.Millisecond)
	defer t.Stop()
	frame := 0
	for {
		select {
		case <-stop:
			fmt.Fprint(os.Stderr, clearLine)
			return
		case <-t.C:
			fmt.Fprintf(os.Stderr, "%s%s%s starting a temporary Radar — connecting to cluster… %ds%s",
				clearLine, cAmber+spinnerFrames[frame%len(spinnerFrames)]+cReset, cDim,
				int(time.Since(start).Seconds()), cReset)
			frame++
		}
	}
}

// tailBuffer keeps the last `limit` bytes written — enough boot log to debug a
// failed cluster connect without ever holding the whole request log.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// probeListening reports whether anything answers on the discovered base —
// used to distinguish "stale port file" from "no Radar at all".
func probeListening(base string) bool {
	u := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	conn, err := net.DialTimeout("tcp", u, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
