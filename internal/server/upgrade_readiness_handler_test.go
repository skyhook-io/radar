package server

import (
	"encoding/json"
	"fmt"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
	"io"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
)

func TestUpgradeReadinessNamespacesIgnoresBrowsingFilter(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/upgrade-readiness?namespaces=payments", nil)
	if got := (&Server{}).upgradeReadinessNamespaces(req); got != nil {
		t.Fatalf("upgrade namespace scope = %v, want cluster-wide", got)
	}
}

func TestUpgradeReadinessNamespacesHonorsForcedScope(t *testing.T) {
	previousForce := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("tenant-a")
	t.Cleanup(func() {
		k8s.SetNamespaceScopeOverride("")
		k8s.ForceNamespaceScope = previousForce
	})

	req := httptest.NewRequest("GET", "/api/upgrade-readiness", nil)
	got := (&Server{}).upgradeReadinessNamespaces(req)
	if len(got) != 1 || got[0] != "tenant-a" {
		t.Fatalf("upgrade namespace scope = %v, want [tenant-a]", got)
	}
}

func TestUpgradeReadinessNamespacesIntersectsForcedScopeWithUserAccess(t *testing.T) {
	previousForce := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("tenant-a")
	t.Cleanup(func() {
		k8s.SetNamespaceScopeOverride("")
		k8s.ForceNamespaceScope = previousForce
	})

	s := &Server{permCache: auth.NewPermissionCache()}
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"tenant-b"}})
	req := requestWithUser("GET", "/api/upgrade-readiness", &auth.User{Username: "alice"})
	if got := s.upgradeReadinessNamespaces(req); !readscope.NoNamespaceAccess(got) {
		t.Fatalf("upgrade namespace scope = %v, want no access", got)
	}
}

func TestUpgradeReadinessEndpointLargeMetrics(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset())
	previousForce := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	t.Cleanup(func() { k8s.SetNamespaceScopeOverride(""); k8s.ForceNamespaceScope = previousForce })
	k8s.InvalidateServerVersionCache()
	t.Cleanup(k8s.InvalidateServerVersionCache)
	s := &Server{}
	endpoint := httptest.NewServer(http.HandlerFunc(s.handleUpgradeReadiness))
	defer endpoint.Close()
	for _, tc := range []struct {
		name         string
		bytes        int
		forbidden    bool
		invalidBody  string
		fillerLine   string
		noDeprecated bool
		chunkDelay   time.Duration
		timesOut     bool
		wantStatus   upgradereadiness.CheckStatus
		wantText     string
	}{
		{name: "normal", bytes: 1 << 20, wantStatus: upgradereadiness.CheckBlocked},
		{name: "large", bytes: 17 << 20, wantStatus: upgradereadiness.CheckBlocked},
		{name: "oversized", bytes: 257 << 20, fillerLine: "# unrelated metrics metadata\n", wantStatus: upgradereadiness.CheckUnknown, wantText: "size limit"},
		{name: "forbidden", forbidden: true, wantStatus: upgradereadiness.CheckUnknown, wantText: "Grant get"},
		{name: "html", invalidBody: "<html><body>Sign in</body></html>\n", wantStatus: upgradereadiness.CheckUnknown, wantText: "could not read or parse"},
		{name: "metric_then_html", invalidBody: "up 1\n<html><body>Sign in</body></html>\n", wantStatus: upgradereadiness.CheckUnknown, wantText: "could not read or parse"},
		{name: "process_then_html", invalidBody: "process_start_time_seconds 1721234567\n<html>Sign in</html>\n", wantStatus: upgradereadiness.CheckUnknown, wantText: "could not read or parse"},
		{name: "observed_empty", bytes: 1 << 20, noDeprecated: true, wantStatus: upgradereadiness.CheckPassed},
		{name: "large_observed_empty", bytes: 17 << 20, noDeprecated: true, wantStatus: upgradereadiness.CheckPassed},
		{name: "large_slow_complete", bytes: 17 << 20, chunkDelay: 150 * time.Millisecond, wantStatus: upgradereadiness.CheckBlocked},
		{name: "large_slow_timeout", bytes: 64 << 20, chunkDelay: 300 * time.Millisecond, timesOut: true, wantStatus: upgradereadiness.CheckUnknown, wantText: "could not read or parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A distinct scope prevents a previous case's memoized scan from serving this request.
			k8s.SetNamespaceScopeOverride(fmt.Sprintf("metrics-%d", time.Now().UnixNano()))
			var metricsRequests atomic.Int32
			var bytesWritten atomic.Int64
			var streamCanceled atomic.Bool
			metricsFinished := make(chan struct{})
			processStartedAt := time.Now().Add(-2 * time.Hour).Unix()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/version":
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"major":"1","minor":"35","gitVersion":"v1.35.0"}`)
				case "/metrics":
					metricsRequests.Add(1)
					defer close(metricsFinished)
					if tc.invalidBody != "" {
						w.Header().Set("Content-Type", "text/html")
						_, _ = io.WriteString(w, tc.invalidBody)
						return
					}
					if tc.forbidden {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"metrics access denied","reason":"Forbidden","code":403}`)
						return
					}
					w.Header().Set("Content-Type", "text/plain; version=0.0.4")
					filler := tc.fillerLine
					if filler == "" {
						filler = "unrelated_metric 1\n"
					}
					chunk := strings.Repeat(filler, 4096)
					if tc.chunkDelay > 0 {
						chunk = strings.Repeat(filler, (1<<20)/len(filler))
					}
					for written := 0; written < tc.bytes; written += len(chunk) {
						n, err := io.WriteString(w, chunk)
						bytesWritten.Add(int64(n))
						if err != nil {
							streamCanceled.Store(r.Context().Err() != nil)
							return
						}
						if tc.chunkDelay > 0 {
							if err := http.NewResponseController(w).Flush(); err != nil {
								streamCanceled.Store(r.Context().Err() != nil)
								return
							}
							select {
							case <-time.After(tc.chunkDelay):
							case <-r.Context().Done():
								streamCanceled.Store(true)
								return
							}
						}
					}
					_, _ = fmt.Fprintf(w, "# TYPE process_start_time_seconds gauge\nprocess_start_time_seconds %d\n", processStartedAt)
					if !tc.noDeprecated {
						_, _ = io.WriteString(w, "# TYPE apiserver_requested_deprecated_apis gauge\napiserver_requested_deprecated_apis{group=\"extensions\",version=\"v1beta1\",resource=\"ingresses\",removed_release=\"1.22\"} 1\n")
					}
				default:
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`)
				}
			}))
			defer api.Close()
			config := &rest.Config{Host: api.URL}
			client, err := kubernetes.NewForConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			previousClient := k8s.SetTestClient(client)
			defer k8s.SetTestClient(previousClient)
			previousConfig := k8s.SetTestConfig(config)
			defer k8s.SetTestConfig(previousConfig)
			startedAt := time.Now()
			httpClient := &http.Client{Timeout: 20 * time.Second}
			response, err := httpClient.Get(endpoint.URL + "/api/upgrade-readiness?target=1.36&refresh=true")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			raw, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("HTTP %d: %s", response.StatusCode, raw)
			}
			var result upgradeReadinessResponse
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			t.Logf("SCAN_JSON_%s=%s", tc.name, raw)
			if metricsRequests.Load() != 1 {
				t.Fatalf("metrics requests=%d, want1 fresh read", metricsRequests.Load())
			}
			elapsed := time.Since(startedAt)
			if tc.timesOut {
				select {
				case <-metricsFinished:
				case <-time.After(time.Second):
					t.Fatal("metrics server did not observe the canceled stream")
				}
				if elapsed < 9*time.Second || elapsed > 15*time.Second {
					t.Fatalf("scan took %s, expected the real 10-second collection deadline", elapsed)
				}
				if bytesWritten.Load() <= 16<<20 || bytesWritten.Load() >= int64(tc.bytes) || !streamCanceled.Load() {
					t.Fatalf("bytes sent=%d of %d, canceled=%v; want a large response interrupted while still streaming", bytesWritten.Load(), tc.bytes, streamCanceled.Load())
				}
			}
			t.Logf("elapsed=%s bytes_sent=%d planned_bytes=%d stream_canceled=%v", elapsed, bytesWritten.Load(), tc.bytes, streamCanceled.Load())
			for _, check := range result.Checks {
				if check.ID != "deprecated-api-requests" {
					continue
				}
				if check.Status != tc.wantStatus || !strings.Contains(check.Caveat, tc.wantText) {
					t.Fatalf("unexpected check: %+v", check)
				}
				if !tc.forbidden && strings.Contains(check.Caveat, "Grant") {
					t.Fatalf("wrong access advice: %+v", check)
				}
				if tc.wantStatus == upgradereadiness.CheckBlocked && (len(check.Findings) != 1 || !strings.Contains(check.EvidenceNote, "process lifetime")) {
					t.Fatalf("lost large-response evidence: %+v", check)
				}
				t.Logf("%s endpoint result: status=%s summary=%s caveat=%s", tc.name, check.Status, check.Summary, check.Caveat)
				return
			}
			t.Fatal("removed API usage check missing")
		})
	}
}
