package server

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// sanitizeLogValue strips newlines, carriage returns, and other control
// characters from user-controlled strings (URL params) before they go into
// a log line. Prevents log forgery via attacker-crafted requests like
// `/api/ai/resources/Pod%0Alevel=error fake=line/...` that would otherwise
// inject extra "log entries" into the stream and confuse log scrapers.
// Replaces dangerous runes with '_' rather than dropping them so the
// untrusted value is still visibly present.
func sanitizeLogValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, s)
}

// aiAgentLogResponseWriter wraps http.ResponseWriter to count bytes written
// to the wire. Status capture is a bonus — useful for debugging.
type aiAgentLogResponseWriter struct {
	http.ResponseWriter
	bytes  int
	status int
}

func (w *aiAgentLogResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *aiAgentLogResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// aiAgentLogMiddleware emits one structured log line per request on the
// `/api/ai/*` subrouter. Fields match the MCP agent-log line (same scraper
// can parse both) — `handler` replaces `tool` for the REST side.
//
// Format:
//
//	level=info component=rest handler=/api/ai/resources/{kind}/{namespace}/{name} \
//	  duration_ms=42 bytes=2156 est_tokens=539 truncated=false omitted=0 \
//	  context_tier=none kind=Pod ns=prod
//
// `truncated`, `omitted`, and `context_tier` are reserved fields that future
// agent-context enrichment work will populate; today they emit as zero /
// false / "none" so the line shape stays stable across releases.
func aiAgentLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &aiAgentLogResponseWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		// Emit the log line on the way out — even on panic. chi's
		// Recoverer middleware is mounted OUTERMOST (it converts panics into
		// 500s), so without this defer a panicking handler would unwind past
		// this middleware before any log.Printf ran, and the most-interesting
		// failures would silently miss the log line. Status remains 200 on the
		// wrapped writer in that case because WriteHeader was never called;
		// downstream scrapers can disambiguate via the duration outlier or by
		// cross-referencing the Recoverer's own log line.
		defer func() {
			dur := time.Since(start)

			// chi populates URL params after route matching. Read them inside
			// the defer so we capture the matched values (and the route pattern)
			// even on panic paths.
			var pattern, kind, ns string
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				pattern = rctx.RoutePattern()
				kind = rctx.URLParam("kind")
				ns = rctx.URLParam("namespace")
			}
			if pattern == "" {
				pattern = r.URL.Path
			}
			// "_" is the cluster-scoped placeholder used by the AI routes — surface
			// the empty-namespace meaning cleanly to log scrapers.
			if ns == "_" {
				ns = ""
			}

			level := "info"
			if tw.status >= 500 {
				level = "error"
			}

			log.Printf(
				"level=%s component=rest handler=%s duration_ms=%d bytes=%d est_tokens=%d truncated=%t omitted=%d context_tier=%s kind=%s ns=%s status=%d",
				level, pattern, dur.Milliseconds(), tw.bytes, tw.bytes/4,
				false, 0, "none", sanitizeLogValue(kind), sanitizeLogValue(ns), tw.status,
			)
		}()

		next.ServeHTTP(tw, r)
	})
}
