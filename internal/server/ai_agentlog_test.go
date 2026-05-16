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
	if !strings.Contains(line, "kind=Pod") {
		t.Errorf("log line missing kind on panic path; got:\n%s", line)
	}
}
