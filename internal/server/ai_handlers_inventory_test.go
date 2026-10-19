package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAIResourceInventoryReadiness(t *testing.T) {
	old := k8s.GetConnectionStatus()
	t.Cleanup(func() {
		k8s.SetConnectionStatus(old)
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitTestPromotedSyncingCache(fake.NewClientset(), 5*time.Second, 500*time.Millisecond,
		map[string]time.Duration{"replicasets": 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnecting})
	getPath := "/api/ai/resources/ReplicaSet/default/missing?group=apps&context=none"
	listPath := "/api/ai/resources/replicasets?group=apps&namespace=default&context=none"
	for _, path := range []string{getPath, listPath} {
		assertGateResponse(t, path, http.StatusServiceUnavailable, "kind_sync_pending")
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	for _, path := range []string{getPath, listPath} {
		assertGateResponse(t, path, http.StatusServiceUnavailable, "kind_sync_pending")
	}
	for _, expected := range []struct {
		getStatus  int
		listStatus int
		code       string
	}{
		{http.StatusServiceUnavailable, http.StatusServiceUnavailable, "kind_sync_failed"},
		{http.StatusNotFound, http.StatusOK, ""},
	} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			getStatus, getCode := gateProbe(t, getPath)
			listStatus, listCode := gateProbe(t, listPath)
			if getStatus == expected.getStatus && listStatus == expected.listStatus && getCode == expected.code && listCode == expected.code {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("want %+v; get=%d/%s list=%d/%s", expected, getStatus, getCode, listStatus, listCode)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestAIResourceContextWaitsForFullConnection(t *testing.T) {
	old := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(old) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnecting})
	for _, path := range []string{
		"/api/ai/resources/configmaps/default/does-not-exist",
		"/api/ai/resources/configmaps?namespace=default",
	} {
		assertGateResponse(t, path, http.StatusServiceUnavailable, "cluster_connecting")
	}
	assertGateResponse(t, "/api/ai/resources/configmaps?namespace=default&context=none", http.StatusOK, "")
	assertGateResponse(t, "/api/ai/resources/configmaps/default/does-not-exist?context=none", http.StatusNotFound, "")
}

func TestAIResourceInventoryCoveragePreservesReadyReadsAndAuthorization(t *testing.T) {
	old := k8s.GetConnectionStatus()
	t.Cleanup(func() {
		k8s.SetConnectionStatus(old)
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "known", Namespace: "a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unseen", Namespace: "b"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}},
	), map[string]k8score.ResourceScope{
		k8score.Pods:        {Enabled: true, Namespace: "a"},
		k8score.Nodes:       {Enabled: true},
		k8score.Secrets:     {Enabled: true, Namespace: "a"},
		k8score.LimitRanges: {Enabled: true, Namespace: "a"},
	}); err != nil {
		t.Fatal(err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"a"}}
	perms.SetCanI("get", "", "nodes", "", true)
	perms.SetCanI("list", "", "nodes", "", true)
	perms.SetCanI("get", "", "limitranges", "a", true)
	perms.SetCanI("list", "", "limitranges", "a", true)
	env.srv.permCache.Set("bob", nil, perms)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/ai/resources/pods/a/known", http.StatusOK},
		{"/api/ai/resources/pods/a/known?context=none", http.StatusOK},
		{"/api/ai/resources/pods/a/missing?context=none", http.StatusNotFound},
		{"/api/ai/resources/pods?namespace=a", http.StatusOK},
		{"/api/ai/resources/pods?namespace=b", http.StatusForbidden},
		{"/api/ai/resources/pods/b/unseen", http.StatusForbidden},
		{"/api/ai/resources/nodes/_/worker?context=none", http.StatusOK},
		{"/api/ai/resources/nodes?namespace=a&context=none", http.StatusOK},
		{"/api/ai/resources/limitranges?namespace=a&context=none", http.StatusOK},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp := env.authGet(t, tc.path, "bob", "")
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("got %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.path == "/api/ai/resources/pods?namespace=a" {
				var rows []map[string]any
				if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 || rows[0]["name"] != "known" || rows[0]["summaryContext"] == nil {
					t.Fatalf("covered list lost resource or context: %+v", rows)
				}
			}
		})
	}
	perms = &auth.UserPermissions{AllowedNamespaces: []string{"a", "b"}}
	perms.SetCanI("get", "", "limitranges", "b", true)
	perms.SetCanI("list", "", "limitranges", "b", true)
	perms.SetCanI("get", "", "secrets", "b", true)
	perms.SetCanI("list", "", "secrets", "b", true)
	env.srv.permCache.Set("bob", nil, perms)
	for _, path := range []string{"/api/ai/resources/limitranges/b/missing", "/api/ai/resources/limitranges?namespace=b"} {
		resp := env.authGet(t, path, "bob", "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: got %d, want 403", path, resp.StatusCode)
		}
	}
	resp := env.authGet(t, "/api/secrets/certificate-expiry?namespace=b", "bob", "")
	var expiryError struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&expiryError); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || expiryError.ErrorCode != "kind_namespace_uncovered" {
		t.Fatalf("certificate expiry scope conflict: %d %+v", resp.StatusCode, expiryError)
	}
	env.srv.permCache.Set("bob", nil, &auth.UserPermissions{AllowedNamespaces: nil})
	resp = env.authGet(t, "/api/ai/resources/pods?context=none", "bob", "")
	defer resp.Body.Close()
	var body struct {
		ErrorCode string `json:"error_code"`
		Error     string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusConflict || body.ErrorCode != "kind_namespace_uncovered" {
		t.Fatalf("cluster-wide list claimed completeness: %d %+v", resp.StatusCode, body)
	}
	if !strings.Contains(body.Error, "Watched namespaces within this authorized scope: a.") || !strings.Contains(body.Error, "Request one of these namespaces") {
		t.Fatalf("unfiltered scope error provided no usable recovery: %+v", body)
	}
}
