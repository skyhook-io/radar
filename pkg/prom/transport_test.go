package prom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSameOriginRedirectClientCanonicalOrigins(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		allowed  bool
	}{
		{"http://prom.example/start", "http://PROM.example:80/end", true},
		{"http://prom.example:080/start", "http://prom.example/end", true},
		{"https://prom.example:0443/start", "https://prom.example:443/end", true},
		{"http://prom.example:09090/start", "http://prom.example:9090/end", true},
		{"http://prom.example:65536/start", "http://prom.example:65536/end", false},
		{"https://prom.example/start", "https://prom.example:443/end", true},
		{"http://[::1]/start", "http://[::1]:80/end", true},
		{"https://prom.example/start", "https://sub.prom.example/end", false},
		{"http://prom.example/start", "https://prom.example/end", false},
		{"https://prom.example/start", "http://prom.example/end", false},
		{"http://prom.example/start", "http://prom.example:8080/end", false},
	} {
		t.Run(tc.to, func(t *testing.T) {
			first, _ := http.NewRequest(http.MethodGet, tc.from, nil)
			next, _ := http.NewRequest(http.MethodGet, tc.to, nil)
			err := SameOriginRedirectClient(&http.Client{}).CheckRedirect(next, []*http.Request{first})
			if (err == nil) != tc.allowed {
				t.Fatalf("redirect %s → %s = %v, allowed = %t", tc.from, tc.to, err, tc.allowed)
			}
			if err != nil && !strings.Contains(err.Error(), "configure the final backend URL directly") {
				t.Fatalf("redirect error is not actionable: %v", err)
			}
		})
	}
}

func TestHTTPTransportRejectsCrossOriginRedirects(t *testing.T) {
	for _, scenario := range []string{"default-client", "custom-policy", "policy-rewrites-url", "https-downgrade"} {
		t.Run(scenario, func(t *testing.T) {
			var received atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(target.Close)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				destination := target.URL
				if scenario == "policy-rewrites-url" {
					destination = "/same-origin"
				}
				http.Redirect(w, r, destination, http.StatusFound)
			})
			var source *httptest.Server
			if scenario == "https-downgrade" {
				source = httptest.NewTLSServer(handler)
			} else {
				source = httptest.NewServer(handler)
			}
			t.Cleanup(source.Close)
			client := source.Client()
			if scenario == "custom-policy" || scenario == "policy-rewrites-url" {
				client.CheckRedirect = func(r *http.Request, _ []*http.Request) error {
					if scenario == "policy-rewrites-url" {
						r.URL, _ = url.Parse(target.URL)
					}
					return nil
				}
			}
			transport := NewHTTPTransport(source.URL, "", client)
			transport.Headers = map[string]string{"Authorization": "Bearer secret", "X-Scope-OrgID": "tenant", "X-API-Key": "api-secret"}
			_, err := transport.Do(context.Background(), http.MethodGet, "/api/v1/query", nil)
			if err == nil || !strings.Contains(err.Error(), "cross-origin redirect refused") {
				t.Fatalf("error = %v, want refused redirect", err)
			}
			if received.Load() != 0 {
				t.Fatal("redirect target received a request")
			}
		})
	}
}

func TestHTTPTransportSameOriginRedirectPolicy(t *testing.T) {
	for _, scenario := range []string{"allow", "deny", "loop"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/start" || scenario == "loop" {
					http.Redirect(w, r, "/end", http.StatusFound)
					return
				}
				if r.Header.Get("X-API-Key") != "secret" {
					t.Error("same-origin redirect lost its header")
				}
				_, _ = w.Write([]byte("ok"))
			}))
			t.Cleanup(server.Close)
			client := server.Client()
			denied := errors.New("caller policy")
			if scenario == "deny" {
				client.CheckRedirect = func(*http.Request, []*http.Request) error { return denied }
			}
			transport := NewHTTPTransport(server.URL, "", client)
			transport.Headers = map[string]string{"X-API-Key": "secret"}
			_, err := transport.Do(context.Background(), http.MethodGet, "/start", nil)
			switch scenario {
			case "allow":
				if err != nil || requests.Load() != 2 || client.CheckRedirect != nil {
					t.Fatalf("same-origin redirect failed or changed caller client: %v", err)
				}
			case "deny":
				if !errors.Is(err, denied) || requests.Load() != 1 {
					t.Fatalf("caller policy not honored: %v", err)
				}
			case "loop":
				if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") || requests.Load() != 10 {
					t.Fatalf("redirect loop not bounded: %v (%d requests)", err, requests.Load())
				}
			}
		})
	}
}

func TestHTTPTransportRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", (10<<20)+1)))
	}))
	defer srv.Close()

	_, err := NewHTTPTransport(srv.URL, "", nil).Do(context.Background(), http.MethodGet, "/api/v1/query", nil)
	if err == nil || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want explicit response-size error", err)
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		t.Fatalf("oversized 200 response must not be classified as an HTTP status error: %v", err)
	}
}

func TestHTTPTransportHonorsCustomResponseLimit(t *testing.T) {
	body := strings.Repeat("x", 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	transport := NewHTTPTransport(srv.URL, "", nil)
	transport.MaxResponseBytes = 1024
	if _, err := transport.Do(context.Background(), http.MethodGet, "/allocation", nil); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want custom response-size error", err)
	}

	transport.MaxResponseBytes = 4096
	got, err := transport.Do(context.Background(), http.MethodGet, "/allocation", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("body length = %d, want %d", len(got), len(body))
	}
}

func TestHTTPErrorDiagnosticIsBoundedAndSingleLine(t *testing.T) {
	err := (&HTTPError{
		StatusCode: http.StatusBadGateway,
		URL:        "https://cost.example.com/allocation",
		Body:       []byte("first\nsecond\tthird\vfourth\ffifth\u2028sixth\u2029" + strings.Repeat("x", 600)),
	}).Error()
	if strings.ContainsAny(err, "\r\n\t\v\f\u2028\u2029") {
		t.Fatalf("error contains log control characters: %q", err)
	}
	if !strings.Contains(err, "first second third fourth fifth sixth ") || !strings.HasPrefix(err, "upstream returned 502") || !strings.HasSuffix(err, "…") {
		t.Fatalf("unexpected bounded diagnostic: %q", err)
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
