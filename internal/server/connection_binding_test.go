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
	t.Cleanup(k8s.ResetTestConnectionIdentity)
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
	if got := rec.Header().Get(connectionHeader); got != idA {
		t.Fatalf("response names connection %q, want %q", got, idA)
	}
}

// A tab still showing A after the server moved to B is refused before any
// Kubernetes work, including A→B→A where the context name matches again.
func TestBoundWriteRefusesStaleConnection(t *testing.T) {
	t.Cleanup(k8s.ResetTestConnectionIdentity)
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
	// A tab that knows the server binds but has no ID yet sends "unknown".
	rec = httptest.NewRecorder()
	s.bindWrite(func(http.ResponseWriter, *http.Request) { called = true })(rec, restartRequest("unknown"))
	if rec.Code != http.StatusConflict || called {
		t.Fatalf("unknown connection: code=%d called=%v", rec.Code, called)
	}
	if writes(a)+writes(b)+writes(a2) != 0 {
		t.Fatal("a refused write reached a cluster")
	}
}

// Between the point of no return and the next admission nothing is serving:
// writes are refused as retryable, whether or not they name a connection.
func TestBoundWriteRefusedWhileNothingServes(t *testing.T) {
	t.Cleanup(k8s.ResetTestConnectionIdentity)
	idA := install(t, "cluster-a", fakeCluster(t))
	k8s.RetireServingConnection()
	s := &Server{}
	for _, header := range []string{idA, ""} {
		rec := httptest.NewRecorder()
		called := false
		s.bindWrite(func(http.ResponseWriter, *http.Request) { called = true })(rec, restartRequest(header))
		if rec.Code != http.StatusServiceUnavailable || called || !strings.Contains(rec.Body.String(), errCodeClusterChanging) {
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
	t.Cleanup(k8s.ResetTestConnectionIdentity)
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

// A read is labelled with the connection that served it, or "none" when the
// serving connection changed while the handler built it.
func TestStampReadLabelsTheServingConnection(t *testing.T) {
	t.Cleanup(k8s.ResetTestConnectionIdentity)
	idA := install(t, "cluster-a", fakeCluster(t))
	s := &Server{}

	rec := httptest.NewRecorder()
	s.stampRead(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) })(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get(connectionHeader); got != idA {
		t.Fatalf("stamp %q, want %q", got, idA)
	}

	rec = httptest.NewRecorder()
	s.stampRead(func(w http.ResponseWriter, _ *http.Request) {
		install(t, "cluster-b", fakeCluster(t))
		_, _ = w.Write([]byte("{}"))
	})(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get(connectionHeader); got != noConnection {
		t.Fatalf("stamp across a switch %q, want %q", got, noConnection)
	}
}

// Control for TestBoundRestartStaysOnAdmittedConnectionAcrossSwitch: the same
// sequence without the binding lands on B, so the bound test can fail.
func TestUnboundRestartFollowsTheSwitch(t *testing.T) {
	t.Cleanup(k8s.ResetTestConnectionIdentity)
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
