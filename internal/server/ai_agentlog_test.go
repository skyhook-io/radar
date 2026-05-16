package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// TestAIAgentLogMiddlewareEmitsLogLine verifies the middleware emits a
// structured log line with the expected fields after the handler runs.
func TestAIAgentLogMiddlewareEmitsLogLine(t *testing.T) {
	var buf bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&buf)

	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(aiAgentLogMiddleware)
			r.Get("/ai/resources/{kind}/{namespace}/{name}", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"kind":"Pod","ns":"prod"}`))
			})
		})
	})

	req := httptest.NewRequest("GET", "/api/ai/resources/Pod/prod/my-pod", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	line := buf.String()
	wants := []string{
		"component=rest",
		"handler=/api/ai/resources/{kind}/{namespace}/{name}",
		"kind=Pod",
		"ns=prod",
		"context_tier=none",
		"truncated=false",
		"omitted=0",
		"status=200",
	}
	for _, w := range wants {
		if !strings.Contains(line, w) {
			t.Errorf("log line missing %q\nfull line: %s", w, line)
		}
	}
}

// TestAIAgentLogMiddlewareClusterScopedNamespace verifies the "_" cluster-scoped
// placeholder gets normalized to an empty ns field for log scrapers.
func TestAIAgentLogMiddlewareClusterScopedNamespace(t *testing.T) {
	var buf bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&buf)

	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(aiAgentLogMiddleware)
			r.Get("/ai/resources/{kind}/{namespace}/{name}", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
		})
	})

	req := httptest.NewRequest("GET", "/api/ai/resources/Node/_/node-1", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	line := buf.String()
	if !strings.Contains(line, "kind=Node") {
		t.Errorf("expected kind=Node in log line: %s", line)
	}
	// Log line format ends with `... kind=<k> ns=<v> status=<n>`. For cluster-
	// scoped resources, ns is normalized from "_" to "" — so the substring
	// " ns= status=" deterministically pins an empty ns value followed by the
	// next field, with no spillover from other fields.
	if !strings.Contains(line, " ns= status=") {
		t.Errorf("expected empty ns followed by status= in log line for cluster-scoped resource: %s", line)
	}
}

// TestAIAgentLogMiddlewareSanitizesURLParams verifies that URL-param-derived
// kind/ns values containing newline / CR / control chars do NOT inject extra
// "log entries" into the structured line. A request like
// `/api/ai/resources/Pod%0Alevel=error fake=line/prod/x` would otherwise
// produce a forged log entry that downstream scrapers parse as a separate
// event.
func TestAIAgentLogMiddlewareSanitizesURLParams(t *testing.T) {
	var buf bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&buf)

	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(aiAgentLogMiddleware)
			r.Get("/ai/resources/{kind}/{namespace}/{name}", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
		})
	})

	// Build the request with raw control chars in the URL path. httptest
	// won't percent-decode for us, so we set the chi URL params via Path
	// after construction. Simpler: just include literal newline characters
	// in the path components — chi.RouteContext will surface them verbatim.
	req := httptest.NewRequest("GET", "/api/ai/resources/Pod%0Alevel=error/prod%0Dfake=ns/x", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	line := buf.String()
	structuredLines := 0
	for _, l := range strings.Split(line, "\n") {
		if strings.Contains(l, "component=rest") {
			structuredLines++
		}
	}
	if structuredLines != 1 {
		t.Errorf("expected exactly 1 structured log line, found %d (injection succeeded?)\nfull output:\n%s", structuredLines, line)
	}
	if strings.Contains(line, "level=error fake=") {
		t.Errorf("user-controlled control chars reached the log line and forged a kv pair\nfull output:\n%s", line)
	}
}

// TestAIAgentLogMiddlewareEmitsOnPanic verifies the middleware emits a
// log line even when the handler panics. Without the deferred logger
// the most-interesting failures (handler crashes) would silently miss
// the log line — chi's outer Recoverer would absorb the panic but our
// line would never run.
func TestAIAgentLogMiddlewareEmitsOnPanic(t *testing.T) {
	var buf bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&buf)

	r := chi.NewRouter()
	// Mirror production: Recoverer wraps the agent-log subrouter, so panics
	// unwind through agent-log middleware before being caught.
	r.Use(chimiddleware.Recoverer)
	r.Route("/api", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(aiAgentLogMiddleware)
			r.Get("/ai/resources/{kind}/{namespace}/{name}", func(_ http.ResponseWriter, _ *http.Request) {
				panic("synthetic handler panic for log-line test")
			})
		})
	})

	req := httptest.NewRequest("GET", "/api/ai/resources/Pod/prod/oops", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	line := buf.String()
	if !strings.Contains(line, "component=rest") {
		t.Errorf("log line missing on panic path; got:\n%s", line)
	}
	if !strings.Contains(line, "handler=/api/ai/resources/{kind}/{namespace}/{name}") {
		t.Errorf("log line missing route pattern on panic path; got:\n%s", line)
	}
	// Panic path MUST log status=500 and level=error, not the default 200 /
	// info. The wire response is 500 (chi.Recoverer writes it after the
	// middleware re-panics), so the structured line must agree — otherwise
	// scrapers tracking error-rate SLOs miss the failures.
	if !strings.Contains(line, "status=500") {
		t.Errorf("panic-path log line must report status=500 (not the default 200); got:\n%s", line)
	}
	if !strings.Contains(line, "level=error") {
		t.Errorf("panic-path log line must report level=error (not info); got:\n%s", line)
	}
	if !strings.Contains(line, "kind=Pod") {
		t.Errorf("log line missing kind on panic path; got:\n%s", line)
	}
}
