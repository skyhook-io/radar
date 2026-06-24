package server

import (
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

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
	return c.Handler
}
