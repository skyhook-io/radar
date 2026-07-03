package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func TestVitals_HappyPath(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/vitals")
	if err != nil {
		t.Fatalf("GET /api/vitals: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got VitalsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The smoke fixture seeds one Running pod and no nodes.
	if got.Pods.Total != 1 || got.Pods.Running != 1 {
		t.Errorf("pods = %+v, want total 1 running 1", got.Pods)
	}
	if got.Nodes.Total != 0 {
		t.Errorf("nodes.total = %d, want 0 (fixture seeds none)", got.Nodes.Total)
	}
	if !got.Completeness.Complete {
		t.Errorf("completeness = %+v, want complete", got.Completeness)
	}
	if got.MetricsServerAvailable {
		t.Errorf("metricsServerAvailable = true, want false (fake client has no metrics API)")
	}
}

func TestVitals_NodeRBACDeniedSurfacesRestricted(t *testing.T) {
	if testServerSrv.permCache == nil {
		testServerSrv.permCache = pkgauth.NewPermissionCache()
	}
	const username = "vitals-node-denied"
	perms := &pkgauth.UserPermissions{}
	perms.SetCanI("list", "", "nodes", "", false)
	testServerSrv.permCache.Set(username, perms)

	req := httptest.NewRequest(http.MethodGet, "/api/vitals", nil)
	req = req.WithContext(pkgauth.ContextWithUser(req.Context(), &pkgauth.User{Username: username}))
	rec := httptest.NewRecorder()
	testServerSrv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", rec.Code, rec.Body.String())
	}
	var got VitalsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	foundNodes := false
	for _, k := range got.Completeness.Restricted {
		if k == "Node" {
			foundNodes = true
		}
	}
	if !foundNodes {
		t.Errorf("restricted = %v, want to contain \"nodes\" (RBAC-denied node list)", got.Completeness.Restricted)
	}
	if got.Completeness.Complete {
		t.Errorf("complete = true despite restricted nodes")
	}
	if got.Nodes.Total != 0 {
		t.Errorf("nodes.total = %d, want 0 when restricted", got.Nodes.Total)
	}
	if got.CPU != nil || got.Memory != nil || got.MetricsServerAvailable {
		t.Errorf("metrics leaked past node RBAC denial: cpu=%v mem=%v msa=%v", got.CPU, got.Memory, got.MetricsServerAvailable)
	}
}

func TestVitals_DeploymentsOnlyUserGetsNoPodData(t *testing.T) {
	if testServerSrv.permCache == nil {
		testServerSrv.permCache = pkgauth.NewPermissionCache()
	}
	const username = "vitals-deploys-only"
	perms := &pkgauth.UserPermissions{}
	// Namespace visibility can come from deployments; pods are denied.
	perms.SetCanI("list", "", "pods", "", false)
	perms.SetCanI("list", "apps", "deployments", "", true)
	perms.SetCanI("list", "", "nodes", "", true)
	testServerSrv.permCache.Set(username, perms)

	req := httptest.NewRequest(http.MethodGet, "/api/vitals", nil)
	req = req.WithContext(pkgauth.ContextWithUser(req.Context(), &pkgauth.User{Username: username}))
	rec := httptest.NewRecorder()
	testServerSrv.Handler().ServeHTTP(rec, req)

	var got VitalsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, rec.Body.String())
	}
	if got.Pods.Total != 0 {
		t.Errorf("pods leaked to a deployments-only user: %+v", got.Pods)
	}
	foundPods := false
	for _, k := range got.Completeness.Restricted {
		if k == "Pod" {
			foundPods = true
		}
	}
	if !foundPods {
		t.Errorf("restricted = %v, want to contain \"pods\"", got.Completeness.Restricted)
	}
	if got.CPU != nil && got.CPU.RequestsMillis != 0 {
		t.Errorf("pod request totals leaked: %+v", got.CPU)
	}
}

func TestVitals_MixedNamespacePodAccessMarksPartial(t *testing.T) {
	if testServerSrv.permCache == nil {
		testServerSrv.permCache = pkgauth.NewPermissionCache()
	}
	const username = "vitals-mixed-ns"
	perms := &pkgauth.UserPermissions{}
	// Visible in two namespaces (via deployments), pod-readable in one.
	perms.SetCanI("list", "", "pods", "default", true)
	perms.SetCanI("list", "", "pods", "kube-system", false)
	perms.SetCanI("list", "apps", "deployments", "default", true)
	perms.SetCanI("list", "apps", "deployments", "kube-system", true)
	perms.SetCanI("list", "", "nodes", "", true)
	perms.AllowedNamespaces = []string{"default", "kube-system"}
	testServerSrv.permCache.Set(username, perms)

	req := httptest.NewRequest(http.MethodGet, "/api/vitals", nil)
	req = req.WithContext(pkgauth.ContextWithUser(req.Context(), &pkgauth.User{Username: username}))
	rec := httptest.NewRecorder()
	testServerSrv.Handler().ServeHTTP(rec, req)

	var got VitalsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, rec.Body.String())
	}
	foundPods := false
	for _, k := range got.Completeness.Restricted {
		if k == "Pod" {
			foundPods = true
		}
	}
	if !foundPods || got.Completeness.Complete {
		t.Errorf("partial pod scope not flagged: completeness=%+v", got.Completeness)
	}
}

func TestVitalsMetricsMemo_SingleFlightCollapsesConcurrentMisses(t *testing.T) {
	var m vitalsMetricsMemo
	var calls int32
	fetch := func() vitalsMetricsEntry {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond) // widen the race window
		return vitalsMetricsEntry{cpuMillis: 100, ok: true}
	}
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() { defer wg.Done(); m.loadOrFetch("ctx\x00user", fetch) }()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fetch ran %d times, want 1 (concurrent misses must share one probe)", got)
	}
}
