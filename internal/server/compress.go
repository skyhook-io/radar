package server

import (
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	kgzip "github.com/klauspost/compress/gzip"
)

// defaultCompressLevel is the gzip level used when RADAR_COMPRESS_LEVEL is unset.
//
// Level 1 (BestSpeed) is deliberate: on large clusters Radar's informer +
// topology processing already runs CPU-hot, and that's exactly where response
// bodies are largest, so peak compression cost coincides with peak baseline
// load. gzip-1 on k8s JSON still yields ~90%+ size reduction at the highest
// throughput — the marginal bytes from higher levels aren't worth contending
// with the watch loop. Operators on bandwidth-bound / CPU-rich deployments can
// raise it (or disable with 0) via RADAR_COMPRESS_LEVEL.
const defaultCompressLevel = 1

// resolveCompressLevel parses RADAR_COMPRESS_LEVEL: empty → default, 0 →
// disabled, 1-9 → gzip level, anything else → default (logged).
func resolveCompressLevel(raw string) int {
	if raw == "" {
		return defaultCompressLevel
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 9 {
		log.Printf("[compress] invalid RADAR_COMPRESS_LEVEL %q; using default %d", raw, defaultCompressLevel)
		return defaultCompressLevel
	}
	return n
}

// compressMiddleware returns a gzip response-compression middleware, or nil when
// compression is disabled (RADAR_COMPRESS_LEVEL=0).
//
// k8s JSON (topology, resource lists, RBAC, audit) compresses 5-10x and today
// travels raw over the network — costly over the Radar Cloud tunnel's high-RTT
// hop. Compressing at the source means the bytes are small across both the
// tunnel and the browser hop, with the hub passing them through untouched and
// the browser decompressing transparently.
//
// gzip is overridden to use klauspost/compress, which is faster AND compresses
// slightly better than stdlib at the same level (it's already a transitive dep).
// chi's default content-type allowlist includes application/json but excludes
// text/event-stream, so SSE streams are never compressed (flush semantics
// intact), and chi forwards Hijack/Flush so pod-exec WebSocket upgrades are
// unaffected. Applied uniformly in local and cloud mode.
func compressMiddleware() func(http.Handler) http.Handler {
	level := resolveCompressLevel(os.Getenv("RADAR_COMPRESS_LEVEL"))
	if level == 0 {
		return nil
	}
	c := middleware.NewCompressor(level)
	c.SetEncoder("gzip", func(w io.Writer, lvl int) io.Writer {
		gw, _ := kgzip.NewWriterLevel(w, lvl)
		return gw
	})
	compress := c.Handler
	return func(next http.Handler) http.Handler {
		compressed := compress(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip the compressor for streaming + upgrade requests. chi acquires
			// a pooled gzip encoder per request *before* the handler sets its
			// Content-Type, releasing it only when the handler returns — so a
			// long-lived SSE (EventSource sends Accept-Encoding: gzip) or pod-exec
			// WebSocket would pin an unused encoder for the whole stream. These
			// responses aren't compressible anyway; bypass before any encoder is
			// taken from the pool.
			if isStreamingRequest(r) {
				next.ServeHTTP(w, r)
				return
			}
			compressed.ServeHTTP(w, r)
		})
	}
}

// isStreamingRequest reports requests that open a long-lived response (SSE) or
// switch protocols (WebSocket exec/terminal) and must not pass through the
// compressor's per-request encoder acquisition.
func isStreamingRequest(r *http.Request) bool {
	// Match by route, not just request headers: every SSE endpoint ends in
	// "/stream" (events, pod/workload logs, traffic flows). This holds even if a
	// future consumer reads the stream via fetch (Accept: */*) instead of
	// EventSource, which the header check below would miss.
	if strings.HasSuffix(r.URL.Path, "/stream") {
		return true
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
		return true
	}
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	for _, v := range strings.Split(strings.ToLower(r.Header.Get("Connection")), ",") {
		if strings.TrimSpace(v) == "upgrade" {
			return true
		}
	}
	return false
}
