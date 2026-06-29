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
// kind/ns values can't inject log structure. Two attack vectors:
//
//  1. Multi-line: a request like `/api/ai/resources/Pod%0Alevel=error/...`
//     decodes to a literal newline that would otherwise split the log entry.
//  2. Same-line logfmt injection: even without control chars, spaces and
//     `=` in URL values introduce new key=value tokens on the SAME line
//     that scrapers parse as legitimate fields.
//
// Both vectors must be neutralized via logsafe.Sanitize.
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

	// %0A = newline (multi-line attack); %20 = space + literal `=` (same-line
	// attack). Combined into one request to exercise both vectors.
	req := httptest.NewRequest("GET", "/api/ai/resources/Pod%0Alevel=error%20fake=ns/prod%0Dfake=ns2/x", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	line := buf.String()

	// Multi-line vector: exactly ONE structured line in the output.
	structuredLines := 0
	for _, l := range strings.Split(line, "\n") {
		if strings.Contains(l, "component=rest") {
			structuredLines++
		}
	}
	if structuredLines != 1 {
		t.Errorf("expected exactly 1 structured log line, found %d (multi-line injection succeeded?)\nfull output:\n%s", structuredLines, line)
	}

	// Same-line vector: forged "level=error" / "fake=ns" / "fake=ns2"
	// fields must NOT appear as standalone kv tokens. Sanitizer collapses
	// space+`=` to underscores, so these key= patterns can't form.
	for _, forged := range []string{" level=error", " fake=ns", " fake=ns2"} {
		if strings.Contains(line, forged) {
			t.Errorf("same-line logfmt injection reached the wire (substring %q present)\nfull output:\n%s", forged, line)
		}
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
