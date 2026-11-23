package k8s

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// The informer clients must not share a client-side rate limiter with the
// request clients: a cache start or relist spends the informer bucket, and a
// handler calling GetClient() must not wait behind it.
func TestInformerClientsHaveTheirOwnRateLimiters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"NamespaceList","apiVersion":"v1","metadata":{},"items":[]}`))
	}))
	defer srv.Close()

	clients, err := newSharedKubernetesClients(&rest.Config{Host: srv.URL, QPS: 1, Burst: 1})
	if err != nil {
		t.Fatalf("newSharedKubernetesClients: %v", err)
	}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

	// Spend the single token of each informer bucket.
	if _, err := clients.informerClientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("informer typed list: %v", err)
	}
	if _, err := clients.informerDynamic.Resource(nsGVR).List(context.Background(), metav1.ListOptions{}); err != nil {
		t.Fatalf("informer dynamic list: %v", err)
	}

	// With a shared bucket the next token is a second away, so the limiter
	// refuses a 300ms deadline up front.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := clients.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{}); err != nil {
		t.Errorf("request typed list waited behind the informer client: %v", err)
	}
	if _, err := clients.dynamic.Resource(nsGVR).List(ctx, metav1.ListOptions{}); err != nil {
		t.Errorf("request dynamic list waited behind the informer client: %v", err)
	}
}

func TestResourceCacheIsBuiltFromInformerClients(t *testing.T) {
	defer ResetTestState()

	requestClient, err := kubernetes.NewForConfig(&rest.Config{Host: "http://localhost:1"})
	if err != nil {
		t.Fatalf("request client: %v", err)
	}
	informerClient := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"}},
	)
	requestDyn := fakeDyn(t, func(gvr schema.GroupVersionResource, _ string) bool {
		t.Errorf("list probe for %s went through the request dynamic client", gvr.Resource)
		return false
	})
	informerDyn := fakeDyn(t, func(gvr schema.GroupVersionResource, _ string) bool {
		return gvr.Group == "" && gvr.Resource == "pods"
	})

	clientMu.Lock()
	installedConn.Store(installedConnectionLocked(nil, &sharedKubernetesClients{
		clientset:         requestClient,
		dynamic:           requestDyn,
		informerClientset: informerClient,
		informerDynamic:   informerDyn,
	}))
	clientMu.Unlock()

	// Observers live for the process, so stop recording when the test ends.
	sources := make(chan kubernetes.Interface, 1)
	var recording atomic.Bool
	recording.Store(true)
	defer recording.Store(false)
	OnResourceObserved(func(source kubernetes.Interface, _ ResourceChange, _, _ any) {
		if !recording.Load() {
			return
		}
		select {
		case sources <- source:
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := InitResourceCache(ctx); err != nil {
		t.Fatalf("InitResourceCache: %v", err)
	}
	if got := GetResourceCache().Client(); got != kubernetes.Interface(informerClient) {
		t.Fatalf("cache built from %T, want the informer client", got)
	}
	if got := InformerClientFor(requestClient); got != kubernetes.Interface(informerClient) {
		t.Errorf("InformerClientFor(request client) = %T, want the informer client", got)
	}
	// Observers compare source with GetClient() (Prometheus does), so the
	// cache must report the request client even though it lists through the
	// informer client.
	select {
	case got := <-sources:
		if got != kubernetes.Interface(requestClient) {
			t.Errorf("observer source = %T, want the request client", got)
		}
	case <-ctx.Done():
		t.Fatal("no resource observer callback during the initial sync")
	}
}

func TestContextSwitchReplacesInformerClients(t *testing.T) {
	restoreClientGlobals(t)
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "config", "alpha", []kubeEntry{
		{ctxName: "alpha", userName: "ua", clusterName: "cluster-alpha"},
		{ctxName: "beta", userName: "ub", clusterName: "cluster-beta"},
	})
	if err := doInit(InitOptions{KubeconfigPath: path}); err != nil {
		t.Fatalf("doInit: %v", err)
	}
	alphaInformer, alphaDyn := GetInformerClient(), GetInformerDynamicClient()
	if alphaInformer == nil || alphaInformer == kubernetes.Interface(GetClient()) {
		t.Fatal("informer client after init is nil or the request client, want its own client")
	}
	if alphaDyn == nil || alphaDyn == GetDynamicClient() {
		t.Fatal("informer dynamic client after init is nil or the request client, want its own client")
	}

	p, err := prepareContext("beta")
	if err != nil {
		t.Fatalf("prepareContext: %v", err)
	}
	installPreparedContext(p)
	if got := GetInformerClient(); got != p.clients.informerClientset || got == alphaInformer {
		t.Error("context switch kept the previous cluster's informer client")
	}
	if got := GetInformerDynamicClient(); got != p.clients.informerDynamic || got == alphaDyn {
		t.Error("context switch kept the previous cluster's informer dynamic client")
	}

	BlockClusterAccess(errors.New("blocked"))
	t.Cleanup(func() { contextOperationsBlocked.Store(nil) })
	if GetInformerClient() != nil || GetInformerDynamicClient() != nil {
		t.Error("BlockClusterAccess left informer clients in place")
	}
}

// Tests that install only the request clients must still get a cache client,
// and never an informer client left over from a different client set.
func TestInformerClientsFallBackToRequestClients(t *testing.T) {
	restoreClientGlobals(t)
	stale, err := newSharedKubernetesClients(&rest.Config{Host: "http://localhost:1"})
	if err != nil {
		t.Fatalf("newSharedKubernetesClients: %v", err)
	}
	other, err := kubernetes.NewForConfig(&rest.Config{Host: "http://localhost:2"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	otherDyn := fakeDyn(t, func(schema.GroupVersionResource, string) bool { return true })

	clientMu.Lock()
	installedConn.Store(installedConnectionLocked(nil, stale))
	clientMu.Unlock()
	SetTestClient(other)
	clientMu.Lock()
	installedConn.Store(copyTestConnectionLocked())
	installedConn.Load().clients.dynamic, installedConn.Load().clients.informerDynamic = otherDyn, nil
	clientMu.Unlock()

	if got := GetInformerClient(); got != kubernetes.Interface(other) {
		t.Errorf("GetInformerClient() = %v, want the request client", got)
	}
	if got := GetInformerDynamicClient(); got != otherDyn {
		t.Errorf("GetInformerDynamicClient() = %v, want the request dynamic client", got)
	}
}

// Request-triggered reads through the dynamic cache (direct lists, direct
// GETs, count probes) must not wait behind the informer bucket either.
func TestDynamicCacheDirectReadsUseTheRequestClient(t *testing.T) {
	restoreClientGlobals(t)
	defer ResetDynamicResourceCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/namespaces/default" {
			_, _ = w.Write([]byte(`{"kind":"Namespace","apiVersion":"v1","metadata":{"name":"default"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"kind":"NamespaceList","apiVersion":"v1","metadata":{},"items":[]}`))
	}))
	defer srv.Close()

	clients, err := newSharedKubernetesClients(&rest.Config{Host: srv.URL, QPS: 1, Burst: 4})
	if err != nil {
		t.Fatalf("newSharedKubernetesClients: %v", err)
	}
	clientMu.Lock()
	installedConn.Store(installedConnectionLocked(nil, clients))
	clientMu.Unlock()
	if err := InitDynamicResourceCache(nil); err != nil {
		t.Fatalf("InitDynamicResourceCache: %v", err)
	}
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	// Four request reads follow, so spend all four informer tokens.
	for range 4 {
		if _, err := clients.informerDynamic.Resource(nsGVR).List(context.Background(), metav1.ListOptions{}); err != nil {
			t.Fatalf("informer dynamic list: %v", err)
		}
	}

	dc := GetDynamicResourceCache()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := dc.ListDirect(ctx, nsGVR, ""); err != nil {
		t.Errorf("ListDirect waited behind the informer client: %v", err)
	}
	if _, err := dc.GetDirectPreserveLastApplied(ctx, nsGVR, "", "default"); err != nil {
		t.Errorf("GetDirectPreserveLastApplied waited behind the informer client: %v", err)
	}
	if _, err := dc.CountDirectProbe(ctx, nsGVR, nil, 0, 1); err != nil {
		t.Errorf("CountDirectProbe waited behind the informer client: %v", err)
	}
	if _, err := dc.ListDirectBounded(ctx, nsGVR, "", 10); err != nil {
		t.Errorf("ListDirectBounded waited behind the informer client: %v", err)
	}
}

// A permission check a request handler runs goes through the request client;
// only the startup probe inside InitResourceCache spends the informer bucket.
func TestRequestPermissionCheckUsesTheRequestClient(t *testing.T) {
	defer ResetTestState()
	requestClient, err := kubernetes.NewForConfig(&rest.Config{Host: "http://localhost:1"})
	if err != nil {
		t.Fatalf("request client: %v", err)
	}
	requestDyn := fakeDyn(t, func(gvr schema.GroupVersionResource, _ string) bool {
		return gvr.Group == "" && gvr.Resource == "pods"
	})
	informerDyn := fakeDyn(t, func(gvr schema.GroupVersionResource, _ string) bool {
		t.Errorf("request-triggered probe for %s went through the informer dynamic client", gvr.Resource)
		return false
	})
	clientMu.Lock()
	installedConn.Store(installedConnectionLocked(nil, &sharedKubernetesClients{clientset: requestClient, dynamic: requestDyn, informerDynamic: informerDyn}))
	clientMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result, _ := CheckResourcePermissions(ctx); result == nil || !result.Perms.Pods {
		t.Fatalf("CheckResourcePermissions = %+v, want pods allowed by the request client", result)
	}
}
