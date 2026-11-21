package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

var deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

// fakeCluster is one cluster's dynamic client holding the same Deployment
// every test cluster has, so a write that lands on the wrong cluster succeeds
// there instead of failing loudly.
func fakeCluster(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "default"},
		"spec":     map[string]any{"replicas": int64(1)},
	}}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{deploymentsGVR: "DeploymentList"}, obj)
}

func appsDiscovery() *fakediscovery.FakeDiscovery {
	d := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	d.Resources = []*metav1.APIResourceList{{
		GroupVersion: "apps/v1",
		APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true,
			Verbs: metav1.Verbs{"get", "list", "watch", "patch", "update"}}},
	}}
	return d
}

func install(t *testing.T, name string, dyn *dynamicfake.FakeDynamicClient) string {
	t.Helper()
	id, err := k8s.InstallTestServingConnection(name, &rest.Config{Host: "https://" + name}, dyn, appsDiscovery())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func restartRequest(connection string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/default/web/restart", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("kind", "deployments")
	rctx.URLParams.Add("namespace", "default")
	rctx.URLParams.Add("name", "web")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	if connection != "" {
		req.Header.Set(connectionHeader, connection)
	}
	return req
}

func writes(c *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, a := range c.Actions() {
		if a.GetVerb() == "patch" || a.GetVerb() == "update" {
			n++
		}
	}
	return n
}

// A switch that lands after a write was admitted must not redirect it: the
// restart the user sent while looking at A runs on A, and B sees nothing.
func TestBoundRestartStaysOnAdmittedConnectionAcrossSwitch(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	a, b := fakeCluster(t), fakeCluster(t)
	idA := install(t, "cluster-a", a)
	s := &Server{}

	h := s.bindWrite(func(w http.ResponseWriter, r *http.Request) {
		install(t, "cluster-b", b) // the switch completes between admission and execution
		s.handleRestartWorkload(w, r)
	})
	rec := httptest.NewRecorder()
	h(rec, restartRequest(idA))

	if rec.Code != http.StatusOK {
		t.Fatalf("restart: %d %s", rec.Code, rec.Body)
	}
	if writes(b) != 0 {
		t.Fatalf("cluster B received %d writes from a restart admitted on A", writes(b))
	}
	if writes(a) == 0 {
		t.Fatal("the restart never reached cluster A")
	}
}

// A tab still showing A after the server moved to B is refused before any
// Kubernetes work, including A→B→A where the context name matches again.
func TestBoundWriteRefusesStaleConnection(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	a, b, a2 := fakeCluster(t), fakeCluster(t), fakeCluster(t)
	idA := install(t, "cluster-a", a)
	install(t, "cluster-b", b)
	install(t, "cluster-a", a2)
	s := &Server{}

	called := false
	rec := httptest.NewRecorder()
	s.bindWrite(func(http.ResponseWriter, *http.Request) { called = true })(rec, restartRequest(idA))
	if rec.Code != http.StatusConflict || called || !strings.Contains(rec.Body.String(), errCodeContextChanged) {
		t.Fatalf("stale connection: code=%d called=%v body=%s", rec.Code, called, rec.Body)
	}
	if writes(a)+writes(b)+writes(a2) != 0 {
		t.Fatal("a refused write reached a cluster")
	}
}

// Between the point of no return and the next admission nothing is serving:
// writes are refused as retryable, whether or not they name a connection.
func TestBoundWriteRefusedWhileNothingServes(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	idA := install(t, "cluster-a", fakeCluster(t))
	k8s.RetireServingConnection()
	s := &Server{}
	for _, header := range []string{idA, ""} {
		rec := httptest.NewRecorder()
		called := false
		s.bindWrite(func(http.ResponseWriter, *http.Request) { called = true })(rec, restartRequest(header))
		if rec.Code != http.StatusServiceUnavailable || called || !strings.Contains(rec.Body.String(), errCodeClusterUnavailable) {
			t.Fatalf("header %q: code=%d called=%v body=%s", header, rec.Code, called, rec.Body)
		}
	}
	// A switch or rescope holding the operation lock refuses capture too.
	install(t, "cluster-a", fakeCluster(t))
	restore := k8s.SetTestContextOperationInProgress(true)
	defer restore()
	rec := httptest.NewRecorder()
	s.bindWrite(func(http.ResponseWriter, *http.Request) {})(rec, restartRequest(""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("during an operation: code=%d", rec.Code)
	}
}

// Callers that name no connection (MCP, curl, an older UI) are admitted
// against the current connection, and still run on that one capture.
func TestBoundWriteAdmitsUnnamedCallerOnCurrentConnection(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	a, b := fakeCluster(t), fakeCluster(t)
	install(t, "cluster-a", a)
	s := &Server{}
	rec := httptest.NewRecorder()
	s.bindWrite(func(w http.ResponseWriter, r *http.Request) {
		install(t, "cluster-b", b)
		s.handleRestartWorkload(w, r)
	})(rec, restartRequest(""))
	if rec.Code != http.StatusOK || writes(b) != 0 || writes(a) == 0 {
		t.Fatalf("unnamed caller: code=%d writesA=%d writesB=%d", rec.Code, writes(a), writes(b))
	}
}

// An in-cluster Radar serves one cluster for its whole life, and during a
// rolling update two pods answer one browser: it publishes no identity and
// ignores a connection a request names.
func TestInClusterRadarBindsNoConnections(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	previous := k8s.ForceInCluster
	k8s.ForceInCluster = true
	t.Cleanup(func() { k8s.ForceInCluster = previous })
	a := fakeCluster(t)
	install(t, "in-cluster", a)
	s := &Server{}

	rec := httptest.NewRecorder()
	s.bindWrite(s.handleRestartWorkload)(rec, restartRequest("another-pods-id"))
	if rec.Code != http.StatusOK || writes(a) == 0 {
		t.Fatalf("write: code=%d writes=%d", rec.Code, writes(a))
	}
	fields := connectionIdentityFields(k8s.GetConnectionStatus(), map[string]any{})
	if len(fields) != 0 {
		t.Fatalf("in-cluster status publishes identity: %v", fields)
	}
	// Retry answers with the status struct itself.
	if st := publishedStatus(k8s.GetConnectionStatus()); st.ConnectionID != "" {
		t.Fatalf("in-cluster retry status publishes identity: %+v", st)
	}
}

// Control for TestBoundRestartStaysOnAdmittedConnectionAcrossSwitch: the same
// sequence without the binding lands on B, so the bound test can fail.
func TestUnboundRestartFollowsTheSwitch(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	a, b := fakeCluster(t), fakeCluster(t)
	install(t, "cluster-a", a)
	s := &Server{}
	install(t, "cluster-b", b)
	rec := httptest.NewRecorder()
	s.handleRestartWorkload(rec, restartRequest(""))
	if writes(b) == 0 || writes(a) != 0 {
		t.Fatalf("control: writesA=%d writesB=%d, want the unbound restart on B", writes(a), writes(b))
	}
}

// Every bound write route refuses a tab still naming a connection Radar no
// longer serves, before any Kubernetes work, through the real router.
func TestBoundWriteRoutesRefuseStaleConnection(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	a, b := fakeCluster(t), fakeCluster(t)
	idA := install(t, "cluster-a", a)
	install(t, "cluster-b", b)
	s := &Server{}
	router := chi.NewRouter()
	s.setupAppRoutes(router)

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/workloads/deployments/default/web/restart"},
		{http.MethodPost, "/api/workloads/deployments/default/web/scale"},
		{http.MethodPost, "/api/workloads/deployments/default/web/pause"},
		{http.MethodPost, "/api/workloads/deployments/default/web/resume"},
		{http.MethodPost, "/api/workloads/deployments/default/web/images"},
		{http.MethodPost, "/api/workloads/deployments/default/web/rollback"},
		{http.MethodPost, "/api/cronjobs/default/nightly/trigger"},
		{http.MethodPost, "/api/cronjobs/default/nightly/suspend"},
		{http.MethodPost, "/api/cronjobs/default/nightly/resume"},
		{http.MethodPost, "/api/rollouts/default/web/promote"},
		{http.MethodPost, "/api/resources/apply"},
		{http.MethodPut, "/api/resources/deployments/default/web"},
		{http.MethodDelete, "/api/resources/deployments/default/web"},
		{http.MethodPost, "/api/nodes/node-1/cordon"},
		{http.MethodPost, "/api/nodes/node-1/uncordon"},
		{http.MethodPost, "/api/nodes/node-1/drain"},
		{http.MethodPost, "/api/argo/applications/argocd/app/sync"},
		{http.MethodPost, "/api/argo/applications/argocd/app/refresh"},
		{http.MethodPost, "/api/argo/applications/argocd/app/rollback"},
		{http.MethodPost, "/api/argo/applications/argocd/app/terminate"},
		{http.MethodPost, "/api/argo/applications/argocd/app/suspend"},
		{http.MethodPost, "/api/argo/applications/argocd/app/resume"},
		{http.MethodPost, "/api/argo/applications/argocd/app/validate-resource"},
		{http.MethodPost, "/api/flux/kustomizations/flux-system/app/reconcile"},
		{http.MethodPost, "/api/flux/kustomizations/flux-system/app/sync-with-source"},
		{http.MethodPost, "/api/flux/kustomizations/flux-system/app/suspend"},
		{http.MethodPost, "/api/flux/kustomizations/flux-system/app/resume"},
		{http.MethodPost, "/api/velero/schedules/velero/daily/backup"},
		{http.MethodPost, "/api/velero/schedules/velero/daily/pause"},
		{http.MethodPost, "/api/velero/schedules/velero/daily/resume"},
		{http.MethodPost, "/api/velero/backups/velero/daily-1/delete"},
		{http.MethodPost, "/api/cnpg/clusters/db/main/actions/restart"},
		{http.MethodPost, "/api/cnpg/scheduledbackups/db/nightly/actions/backup-now"},
		{http.MethodPost, "/api/cnpg/poolers/db/rw/actions/pause"},
		{http.MethodPost, "/api/pods/default/web-1/files/upload"},
		{http.MethodPost, "/api/pods/default/web-1/debug"},
		{http.MethodPost, "/api/nodes/node-1/debug"},
		{http.MethodDelete, "/api/nodes/node-1/debug"},
		{http.MethodPost, "/api/portforwards"},
		{http.MethodPost, "/api/helm/releases"},
		{http.MethodPost, "/api/helm/releases/install-stream"},
		{http.MethodPost, "/api/helm/releases/default/web/rollback"},
		{http.MethodPost, "/api/helm/releases/default/web/rollback-stream"},
		{http.MethodPost, "/api/helm/releases/default/web/upgrade"},
		{http.MethodPost, "/api/helm/releases/default/web/upgrade-stream"},
		{http.MethodPost, "/api/helm/releases/default/web/source/upgrade"},
		{http.MethodPost, "/api/helm/releases/default/web/source/upgrade-stream"},
		{http.MethodPut, "/api/helm/releases/default/web/values"},
		{http.MethodDelete, "/api/helm/releases/default/web"},
		{http.MethodPut, "/api/helm/releases/default/web/source"},
		{http.MethodPost, "/api/capi/clusters/default/workload-1/connect"},
		{http.MethodPost, "/api/trace/services/default/web/in-cluster"},
		{http.MethodPost, "/api/cnpg/clusters/db/main/restore-validation"},
	} {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(connectionHeader, idA)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeContextChanged) {
			t.Errorf("%s %s: %d %s, want 409 %s", route.method, route.path, rec.Code, rec.Body, errCodeContextChanged)
		}
	}
	if writes(a)+writes(b) != 0 {
		t.Fatalf("refused writes reached a cluster: A=%d B=%d", writes(a), writes(b))
	}
}

// A WebSocket can't carry the header, so exec names the connection in its URL.
func TestBoundExecRefusesStaleConnectionFromQuery(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	idA := install(t, "cluster-a", fakeCluster(t))
	install(t, "cluster-b", fakeCluster(t))
	s := &Server{}
	router := chi.NewRouter()
	s.setupAppRoutes(router)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/pods/default/web-1/exec?container=app&connection="+idA, nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), errCodeContextChanged) {
		t.Fatalf("exec with a stale connection: %d %s", rec.Code, rec.Body)
	}
}

// Code that only has a context (MCP tools, reachability probes) gets the bound
// connection's clients too.
func TestContextClientsUseTheBoundConnection(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	a := fakeCluster(t)
	install(t, "cluster-a", a)
	bound, err := k8s.CaptureBoundConnection()
	if err != nil {
		t.Fatal(err)
	}
	ctx := k8s.WithBoundConnection(context.Background(), bound)
	b := fakeCluster(t)
	install(t, "cluster-b", b)
	if got := k8s.DynamicClientFromContext(ctx); got != a {
		t.Fatalf("dynamic client from a bound context = %p, want cluster A's %p", got, a)
	}
	if got := k8s.DynamicClientFromContext(context.Background()); got != b {
		t.Fatalf("dynamic client from an unbound context = %p, want the serving cluster B's %p", got, b)
	}
}
