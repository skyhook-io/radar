package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

// setupPrometheusIntegrationTest isolates the on-disk config and installs a
// bare Prometheus client whose headers stand in for whatever flags, env and
// file resolved to at startup.
func setupPrometheusIntegrationTest(t *testing.T, liveHeaders map[string]string) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetHeaders(liveHeaders)
	t.Cleanup(func() { prometheuspkg.Initialize(nil, nil, "") })
	return &Server{}
}

func TestApplyPrometheusURL_ManagedHeaders(t *testing.T) {
	for _, source := range []string{"environment", "flags"} {
		for _, edit := range []string{`{}`, `{"authorization":"replacement"}`} {
			t.Run(source+edit, func(t *testing.T) {
				headers := map[string]string{"Authorization": "Bearer original"}
				s := setupPrometheusIntegrationTest(t, headers)
				prometheuspkg.Configure("https://original.example", headers)
				s.promHeaderFlags = source == "flags"
				if _, err := config.Update(func(c *config.Config) {
					c.PrometheusURL = "https://original.example"
					if source == "environment" {
						c.PrometheusHeadersFromEnv = map[string]string{"Authorization": "PROM_TOKEN"}
					}
				}); err != nil {
					t.Fatal(err)
				}
				before := config.Load()
				rec := applyPrometheus(s, `{"prometheusUrl":"https://original.example","headers":`+edit+`}`)
				if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no changes were saved") {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				if !reflect.DeepEqual(before, config.Load()) || !reflect.DeepEqual(headers, prometheuspkg.CurrentHeaders()) {
					t.Fatal("managed credential edit changed live or saved configuration")
				}
			})
		}
	}
}

func TestApplyPrometheusURL_InvalidHeaders(t *testing.T) {
	for _, headers := range []map[string]string{
		{"Bad Header": "value"}, {"": "value"}, {"Authorization": "bad\r\nvalue"},
		{"Authorization": "first", "authorization": "second"},
	} {
		s := setupPrometheusIntegrationTest(t, nil)
		before := config.Load()
		payload, _ := json.Marshal(map[string]any{"prometheusUrl": "https://original.example", "headers": headers})
		rec := applyPrometheus(s, string(payload))
		if rec.Code != http.StatusBadRequest || !reflect.DeepEqual(before, config.Load()) || len(prometheuspkg.CurrentHeaders()) != 0 {
			t.Fatalf("invalid header edit not refused safely: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestGetConfigPrometheusLiveState(t *testing.T) {
	for _, source := range []string{"settings", "url-flag", "header-flag", "environment"} {
		t.Run(source, func(t *testing.T) {
			s := setupPrometheusIntegrationTest(t, nil)
			s.effectiveConfig = &config.Config{PrometheusURL: "https://startup.example"}
			s.promURLFlag = source == "url-flag"
			s.promHeaderFlags = source == "header-flag"
			if _, err := config.Update(func(c *config.Config) {
				c.PrometheusURL = "https://saved.example"
				c.PrometheusHeaders = map[string]string{"X-Saved": "saved-secret"}
				if source == "environment" {
					c.PrometheusHeadersFromEnv = map[string]string{"Authorization": "PROM_TOKEN"}
				}
			}); err != nil {
				t.Fatal(err)
			}
			prometheuspkg.Configure("https://live.example/path", map[string]string{"Authorization": "live-secret"})
			w := httptest.NewRecorder()
			s.handleGetConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
			var got configResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Effective.PrometheusURL != "https://live.example/path" || !reflect.DeepEqual(got.PrometheusHeaderKeys, []string{"Authorization"}) || got.PrometheusServerManaged != (source != "settings") || got.PrometheusHeadersManaged != (source == "header-flag" || source == "environment") || got.PrometheusURLFromFlag != (source == "url-flag") {
				t.Fatalf("incorrect live metadata: %+v", got)
			}
			if strings.Contains(w.Body.String(), "live-secret") || strings.Contains(w.Body.String(), "saved-secret") || s.effectiveConfig.PrometheusURL != "https://startup.example" {
				t.Fatal("config leaked credentials or mutated immutable startup state")
			}
		})
	}
}

func TestApplyPrometheusURL_StartupFlags(t *testing.T) {
	for _, scenario := range []string{"omitted", "replace", "clear", "cleared-live", "url-flag-only", "auto-discovery", "missing-startup-config", "same-origin"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			}))
			t.Cleanup(target.Close)
			headers := map[string]string{"X-API-Key": "launch-key"}
			s := setupPrometheusIntegrationTest(t, headers)
			originalURL := "https://original.example"
			if scenario == "same-origin" {
				originalURL = target.URL + "/old-path"
			}
			s.promURLFlag = true
			s.effectiveConfig = &config.Config{PrometheusURL: originalURL}
			prometheuspkg.Configure(originalURL, headers)
			if _, err := config.Update(func(c *config.Config) { c.PrometheusURL = originalURL }); err != nil {
				t.Fatal(err)
			}
			body := map[string]any{"prometheusUrl": target.URL}
			switch scenario {
			case "replace":
				body["headers"] = map[string]string{"X-API-Key": "new-key"}
			case "clear", "auto-discovery", "same-origin":
				body["headers"] = map[string]string{}
			case "cleared-live":
				// A same-origin edit may already have cleared or rotated live headers.
				prometheuspkg.Configure(originalURL, nil)
			case "url-flag-only":
				prometheuspkg.Configure(originalURL, nil)
				body["headers"] = map[string]string{"X-API-Key": "new-server-key"}
			case "missing-startup-config":
				s.effectiveConfig = nil
			}
			if scenario == "auto-discovery" {
				body["prometheusUrl"] = ""
			}
			before := config.Load()
			beforeURL, beforeHeaders := prometheuspkg.CurrentConfig()
			payload, _ := json.Marshal(body)
			rec := applyPrometheus(s, string(payload))
			if scenario == "same-origin" {
				if rec.Code != http.StatusOK || requests.Load() == 0 {
					t.Fatalf("same-origin edit rejected: %d %s", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "startup flags") {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			currentURL, currentHeaders := prometheuspkg.CurrentConfig()
			if requests.Load() != 0 || !reflect.DeepEqual(config.Load(), before) || currentURL != beforeURL || !reflect.DeepEqual(currentHeaders, beforeHeaders) {
				t.Fatal("rejected update probed or changed live/saved configuration")
			}
		})
	}
}

func TestApplyPrometheusURL_ConcurrentCredentialUpdates(t *testing.T) {
	backend := func(key string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-API-Key") != key {
				t.Errorf("backend received another server's credential")
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		}))
		t.Cleanup(server.Close)
		return server
	}
	a, b := backend("key-a"), backend("key-b")
	s := setupPrometheusIntegrationTest(t, nil)
	for range 20 {
		prometheuspkg.Configure(b.URL, map[string]string{"X-API-Key": "key-b"})
		if _, err := config.Update(func(c *config.Config) {
			c.PrometheusURL = b.URL
			c.PrometheusHeaders = map[string]string{"X-API-Key": "key-b"}
		}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, update := range []map[string]any{
			{"prometheusUrl": a.URL, "headers": map[string]string{"X-API-Key": "key-a"}},
			{"prometheusUrl": b.URL},
		} {
			wg.Go(func() {
				payload, _ := json.Marshal(update)
				<-start
				rec := applyPrometheus(s, string(payload))
				if rec.Code != http.StatusOK && (update["headers"] != nil || rec.Code != http.StatusBadRequest) {
					t.Errorf("unexpected apply status %d: %s", rec.Code, rec.Body.String())
				}
			})
		}
		close(start)
		wg.Wait()
		currentURL, headers := prometheuspkg.CurrentConfig()
		saved := config.Load()
		if currentURL != a.URL || headers["X-API-Key"] != "key-a" || saved.PrometheusURL != currentURL || !reflect.DeepEqual(saved.PrometheusHeaders, headers) {
			t.Fatal("concurrent update mixed live and saved endpoint/credential pairs")
		}
	}
}

func TestApplyPrometheusURL_CredentialDestination(t *testing.T) {
	for _, mode := range []string{"unauthenticated", "authenticated-without-cloud-role", "cloud-owner", "cloud-viewer"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(target.Close)
			headers := map[string]string{"Authorization": "Bearer original", "X-Scope-OrgID": "tenant-original", "X-API-Key": "key-original"}
			s := setupPrometheusIntegrationTest(t, headers)
			prometheuspkg.Configure("https://original.example", headers)
			if _, err := config.Update(func(c *config.Config) {
				c.PrometheusURL = "https://original.example"
				c.PrometheusHeaders = headers
			}); err != nil {
				t.Fatal(err)
			}
			before := config.Load()
			payload, _ := json.Marshal(map[string]string{"prometheusUrl": target.URL})
			r := httptest.NewRequest(http.MethodPut, "/api/integrations/prometheus", strings.NewReader(string(payload)))
			wantStatus := http.StatusBadRequest
			if mode != "unauthenticated" {
				user := &auth.User{Username: "tester"}
				if mode == "cloud-owner" {
					user.Groups = []string{"radar:owner"}
				} else if mode == "cloud-viewer" {
					user.Groups = []string{"radar:viewer"}
					wantStatus = http.StatusForbidden
				}
				r = r.WithContext(auth.ContextWithUser(r.Context(), user))
			}
			rec := httptest.NewRecorder()
			s.handleApplyPrometheusURL(rec, r)
			if rec.Code != wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
			}
			if requests.Load() != 0 {
				t.Fatal("rejected destination received a request")
			}
			currentURL, currentHeaders := prometheuspkg.CurrentConfig()
			if currentURL != "https://original.example" || !reflect.DeepEqual(currentHeaders, headers) || !reflect.DeepEqual(config.Load(), before) {
				t.Fatal("rejected update changed live or saved configuration")
			}
		})
	}
}

func TestApplyPrometheusURL_HeaderUpdates(t *testing.T) {
	for _, scenario := range []string{"same-origin", "flag-url-file-headers", "unbound-file-headers", "replace", "clear", "disk-only", "env-reference", "env-reference-clear", "env-reference-replace"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			var gotHeader atomic.Value
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				gotHeader.Store(r.Header.Get("X-API-Key"))
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			}))
			t.Cleanup(target.Close)
			headers := map[string]string{"X-API-Key": "old-key"}
			s := setupPrometheusIntegrationTest(t, headers)
			originalURL := "https://original.example"
			if scenario == "same-origin" || scenario == "flag-url-file-headers" || scenario == "unbound-file-headers" {
				originalURL = target.URL + "/old-path"
			}
			if scenario == "flag-url-file-headers" {
				s.promURLFlag = true
				s.effectiveConfig = &config.Config{PrometheusURL: originalURL}
			}
			prometheuspkg.Configure(originalURL, headers)
			if _, err := config.Update(func(c *config.Config) {
				c.PrometheusURL = originalURL
				if scenario == "flag-url-file-headers" || scenario == "unbound-file-headers" {
					c.PrometheusURL = ""
				}
				c.PrometheusHeaders = headers
				if strings.HasPrefix(scenario, "env-reference") {
					c.PrometheusHeadersFromEnv = map[string]string{"X-API-Key": "PROM_KEY"}
				}
			}); err != nil {
				t.Fatal(err)
			}
			if scenario == "disk-only" {
				prometheuspkg.Configure(originalURL, nil)
			}
			body := map[string]any{"prometheusUrl": target.URL}
			wantHeader := "old-key"
			if scenario == "replace" || scenario == "env-reference-replace" {
				body["headers"] = map[string]string{"X-API-Key": "new-key"}
				wantHeader = "new-key"
			} else if scenario == "clear" || scenario == "env-reference-clear" {
				body["headers"] = map[string]string{}
				wantHeader = ""
			}
			payload, _ := json.Marshal(body)
			before := config.Load()
			rec := applyPrometheus(s, string(payload))
			wantStatus := http.StatusOK
			if strings.HasPrefix(scenario, "env-reference") {
				wantStatus = http.StatusConflict
			} else if scenario == "disk-only" || scenario == "unbound-file-headers" {
				wantStatus = http.StatusBadRequest
			}
			if rec.Code != wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, wantStatus, rec.Body.String())
			}
			if wantStatus != http.StatusOK {
				if requests.Load() != 0 || !reflect.DeepEqual(config.Load(), before) {
					t.Fatal("rejected update sent a request or modified persisted credentials")
				}
				return
			}
			if requests.Load() == 0 || gotHeader.Load() != wantHeader {
				t.Fatalf("probe header = %v, want %q", gotHeader.Load(), wantHeader)
			}
			if config.Load().PrometheusHeaders["X-API-Key"] != wantHeader || prometheuspkg.CurrentHeaders()["X-API-Key"] != wantHeader {
				t.Fatal("saved and running headers do not match the explicit update")
			}
		})
	}
}

func TestApplyPrometheusURL_RejectsInvalidBaseURL(t *testing.T) {
	for _, value := range []string{
		"http:", "http:///path", "https://user:password@example.com", "https://example.com?token=secret", "https://example.com#secret", "file:///metrics",
		"http://localhost:65536", "https://localhost:99999999999999999999", "http://[::1]:65536", "http://localhost:not-a-port", "http://localhost:-1",
	} {
		t.Run(value, func(t *testing.T) {
			headers := map[string]string{"Authorization": "Bearer original"}
			s := setupPrometheusIntegrationTest(t, headers)
			prometheuspkg.Configure("https://original.example", headers)
			if _, err := config.Update(func(c *config.Config) {
				c.PrometheusURL = "https://original.example"
				c.PrometheusHeaders = headers
			}); err != nil {
				t.Fatal(err)
			}
			before := config.Load()
			body, _ := json.Marshal(map[string]any{
				"prometheusUrl": value,
				"headers":       map[string]string{"Authorization": "Bearer replacement"},
			})
			if rec := applyPrometheus(s, string(body)); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			currentURL, currentHeaders := prometheuspkg.CurrentConfig()
			if !reflect.DeepEqual(before, config.Load()) || currentURL != before.PrometheusURL || !reflect.DeepEqual(currentHeaders, headers) {
				t.Fatal("invalid URL changed saved or running credentials")
			}
		})
	}
}

func applyPrometheus(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleApplyPrometheusURL(rec, httptest.NewRequest(http.MethodPut, "/api/integrations/prometheus", strings.NewReader(body)))
	return rec
}

func TestApplyPrometheusURL_HeadersRequireURL(t *testing.T) {
	tests := []struct {
		name        string
		liveHeaders map[string]string
		diskEnv     map[string]string
		body        string
		wantStatus  int
	}{
		{
			name:       "url with headers is accepted",
			body:       `{"prometheusUrl":"http://prom.example:9090","headers":{"X-Scope-OrgID":"acme"}}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "no url with submitted headers is refused",
			body:       `{"prometheusUrl":"","headers":{"X-Scope-OrgID":"acme"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "no url keeping live headers is refused",
			liveHeaders: map[string]string{"Authorization": "Bearer flag-sourced"},
			body:        `{"prometheusUrl":""}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:       "no url with env-sourced headers on disk is refused even when clearing submitted headers",
			diskEnv:    map[string]string{"Authorization": "PROMETHEUS_TOKEN"},
			body:       `{"prometheusUrl":"","headers":{}}`,
			wantStatus: http.StatusConflict,
		},
		{
			name:        "no url clearing headers with nothing on disk reverts to auto-discovery",
			liveHeaders: map[string]string{"Authorization": "Bearer flag-sourced"},
			body:        `{"prometheusUrl":"","headers":{}}`,
			wantStatus:  http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := setupPrometheusIntegrationTest(t, tt.liveHeaders)
			if len(tt.diskEnv) > 0 {
				if _, err := config.Update(func(c *config.Config) { c.PrometheusHeadersFromEnv = tt.diskEnv }); err != nil {
					t.Fatal(err)
				}
			}
			before := config.Load()

			rec := applyPrometheus(s, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusBadRequest {
				if after := config.Load(); after.PrometheusURL != before.PrometheusURL || len(after.PrometheusHeaders) != len(before.PrometheusHeaders) {
					t.Fatalf("refused request changed the on-disk config: before %#v after %#v", before, after)
				}
				if !strings.Contains(rec.Body.String(), "require a Prometheus URL") {
					t.Fatalf("error body does not name the rule: %s", rec.Body.String())
				}
			}
		})
	}
}
