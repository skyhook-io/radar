package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

func decodeNamespacesResponse(t *testing.T, resp *http.Response) (int, any) {
	t.Helper()
	defer resp.Body.Close()
	var body any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, body
}

// A caller whose namespace access couldn't be determined is told to retry,
// not handed an empty list that reads as "no namespaces".
func TestNamespacesReportsFailedAccessCheck(t *testing.T) {
	cache := k8s.GetResourceCache()
	fakeNamespaceAPIServer(t, http.StatusOK, http.StatusInternalServerError, "default")
	// The helper drops the cache; without one the handler never reaches the access check.
	t.Cleanup(k8s.SwapResourceCacheForTest(cache))
	env := newAuthTestServer(t)
	status, body := decodeNamespacesResponse(t, env.authGet(t, "/api/namespaces", "alice", ""))
	if m, _ := body.(map[string]any); status != http.StatusServiceUnavailable || m["error_code"] != string(readreason.UserPermissionCheckFailed) {
		t.Fatalf("got %d %v, want 503 %s", status, body, readreason.UserPermissionCheckFailed)
	}
}

// Without a Namespaces informer the list comes from Radar's own LIST. A LIST
// that failed is reported; one that was denied still answers with the
// namespaces Radar was configured with.
func TestNamespacesWithoutInformerReportsFailedList(t *testing.T) {
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client:              fake.NewClientset(),
		ResourceTypes:       map[string]bool{"configmaps": true},
		ResourceScopes:      map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "default"}},
		DeferredSyncTimeout: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetResourceCache()
	if cache.Namespaces() != nil {
		t.Fatal("fixture must have no Namespaces informer")
	}
	prevNs := k8s.SetTestContextNamespace("default")
	t.Cleanup(func() { k8s.SetTestContextNamespace(prevNs) })

	srv := New(Config{DevMode: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Stop()
	})

	for _, tc := range []struct {
		name       string
		listStatus int
		wantStatus int
		wantCode   string
	}{
		{"list fails", http.StatusInternalServerError, http.StatusInternalServerError, string(readreason.ReadError)},
		{"list denied", http.StatusForbidden, http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeNamespaceAPIServer(t, tc.listStatus, http.StatusForbidden)
			// The helper drops the cache; this case is a cache without a Namespaces informer.
			t.Cleanup(k8s.SwapResourceCacheForTest(cache))
			resp, err := http.Get(ts.URL + "/api/namespaces")
			if err != nil {
				t.Fatal(err)
			}
			status, body := decodeNamespacesResponse(t, resp)
			if status != tc.wantStatus {
				t.Fatalf("status = %d (%v), want %d", status, body, tc.wantStatus)
			}
			if tc.wantCode != "" {
				if m, _ := body.(map[string]any); m["error_code"] != tc.wantCode {
					t.Errorf("body = %v, want error_code %s", body, tc.wantCode)
				}
			} else if list, _ := body.([]any); len(list) != 1 {
				t.Errorf("denied LIST body = %v, want the context namespace", body)
			}
		})
	}
}
