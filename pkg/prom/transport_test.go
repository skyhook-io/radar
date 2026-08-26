package prom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPTransportRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", (10<<20)+1)))
	}))
	defer srv.Close()

	_, err := NewHTTPTransport(srv.URL, "", nil).Do(context.Background(), http.MethodGet, "/api/v1/query", nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds 10 MiB") {
		t.Fatalf("error = %v, want explicit response-size error", err)
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		t.Fatalf("oversized 200 response must not be classified as an HTTP status error: %v", err)
	}
}

func TestHTTPTransport_AppliesHeaders(t *testing.T) {
	var gotAuth, gotTenant, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTenant = r.Header.Get("X-Scope-OrgID")
		gotAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(srv.Close)

	tr := NewHTTPTransport(srv.URL, "", nil)
	tr.Headers = map[string]string{
		"Authorization": "Bearer secret",
		"X-Scope-OrgID": "tenant-a",
	}

	if _, err := NewClient(tr).Query(context.Background(), "up"); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret")
	}
	if gotTenant != "tenant-a" {
		t.Errorf("X-Scope-OrgID = %q, want %q", gotTenant, "tenant-a")
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
}

func TestHTTPTransport_HeadersOverrideAccept(t *testing.T) {
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(srv.Close)

	tr := NewHTTPTransport(srv.URL, "", nil)
	tr.Headers = map[string]string{"Accept": "application/vnd.custom+json"}

	if _, err := NewClient(tr).Query(context.Background(), "up"); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if gotAccept != "application/vnd.custom+json" {
		t.Errorf("Accept = %q, want override", gotAccept)
	}
}
