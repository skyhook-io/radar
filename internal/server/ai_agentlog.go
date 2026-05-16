package server

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/logsafe"
)

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

		// Emit the log line on the way out — even on panic. chi's Recoverer
		// middleware is mounted OUTERMOST (it converts panics into 500s).
		// Without this defer, a panicking handler would unwind past this
		// middleware before log.Printf ran, and the most-interesting failures
		// would silently miss the log line.
		//
		// recover() is used here NOT to swallow the panic — we re-panic so
		// the outer Recoverer still writes the 500 and logs the trace — but
		// to (a) update tw.status to 500 before the line is emitted, so
		// scrapers tracking error-rate SLOs see the correct status, and (b)
		// flip the level field to "error" via the existing tw.status >= 500
		// branch. Without this, the line would say status=200 while the
		// wire response is 500, breaking observability for the exact failure
		// mode the defer was meant to cover.
		defer func() {
			rec := recover()
			if rec != nil {
				tw.status = http.StatusInternalServerError
			}

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

			// `pattern` comes from chi.RouteContext().RoutePattern() — which is
			// the static route template like "/api/ai/resources/{kind}/...",
			// safe by construction — but we fall back to r.URL.Path when the
			// route didn't match (e.g. middleware misfiring on a 404). The
			// fallback path IS user-controlled, so sanitize it the same way
			// we sanitize kind/ns to stay consistent.
			log.Printf(
				"level=%s component=rest handler=%s duration_ms=%d bytes=%d est_tokens=%d truncated=%t omitted=%d context_tier=%s kind=%s ns=%s status=%d",
				level, logsafe.Sanitize(pattern), dur.Milliseconds(), tw.bytes, logsafe.EstimateTokens(tw.bytes),
				false, 0, "none", logsafe.Sanitize(kind), logsafe.Sanitize(ns), tw.status,
			)

			if rec != nil {
				panic(rec) // let outer Recoverer write the 500 + log the trace
			}
		}()

		next.ServeHTTP(tw, r)
	})
}
