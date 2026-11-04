package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/kueuevisibility"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// fakeVisibility stands in for Kueue's aggregated visibility API server.
type fakeVisibility struct {
	mu           sync.Mutex
	requests     []*http.Request
	status       int
	body         string
	impersonated []string
	onRead       func()
}

func (f *fakeVisibility) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.impersonated = append(f.impersonated, r.Header.Get("Impersonate-User"))
	status, body := f.status, f.body
	f.mu.Unlock()
	if f.onRead != nil {
		f.onRead()
	}
	w.Header().Set("Content-Type", "application/json")
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (f *fakeVisibility) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func positionWorkload(name string, conditions ...map[string]any) *unstructured.Unstructured {
	w := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "Workload",
		"spec": map[string]any{"queueName": "pos"},
	}}
	if len(conditions) > 0 {
		list := make([]any, len(conditions))
		for i, c := range conditions {
			list[i] = c
		}
		w.Object["status"] = map[string]any{"conditions": list}
	}
	w.SetNamespace("default")
	w.SetName(name)
	w.SetUID(types.UID(name + "-uid"))
	w.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 11, 3, 4, 22, 3, 0, time.UTC)))
	return w
}

const pendingRow = `{"kind":"PendingWorkloadsSummary","apiVersion":"visibility.kueue.x-k8s.io/v1beta2","metadata":{},"items":[
 {"metadata":{"name":"pending","namespace":"default","creationTimestamp":"2026-11-03T04:22:03Z"},"priority":0,"localQueueName":"pos","positionInClusterQueue":2,"positionInLocalQueue":0}]}`

// setupKueuePosition caches two Workloads (one pending, one admitted),
// optionally serves the visibility API in discovery, and points Radar's
// cluster config and client at a fake visibility server.
func setupKueuePosition(t *testing.T, visibilityServed bool) *fakeVisibility {
	t.Helper()
	pending := positionWorkload("pending", map[string]any{"type": "QuotaReserved", "status": "False", "reason": "Pending"})
	admitted := positionWorkload("admitted", map[string]any{"type": "QuotaReserved", "status": "True"}, map[string]any{"type": "Admitted", "status": "True"})
	workloadGVR := schema.GroupVersionResource{Group: kueueGroup, Version: "v1beta2", Resource: "workloads"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{workloadGVR: "WorkloadList"}, pending, admitted)
	resources := []k8s.APIResource{{Group: kueueGroup, Version: "v1beta2", Name: "workloads", Kind: "Workload", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}
	if visibilityServed {
		resources = append(resources,
			k8s.APIResource{Group: kueuevisibility.Group, Version: "v1beta2", Name: "localqueues", Kind: "PendingWorkloadsSummary", Namespaced: true, IsCRD: true},
			k8s.APIResource{Group: kueuevisibility.Group, Version: "v1beta2", Name: "clusterqueues", Kind: "PendingWorkloadsSummary", IsCRD: true},
		)
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	discovered := make(chan struct{})
	cache.OnCRDDiscoveryComplete(func() { close(discovered) })
	cache.DiscoverAllCRDs()
	select {
	case <-discovered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not complete")
	}
	if err := cache.EnsureWatching(workloadGVR); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(workloadGVR, 5*time.Second) {
		t.Fatal("Workload cache did not sync")
	}

	fake := &fakeVisibility{body: pendingRow}
	ts := httptest.NewServer(fake)
	t.Cleanup(ts.Close)
	config := &rest.Config{Host: ts.URL}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	prevConfig := k8s.SetTestConfig(config)
	prevClient := k8s.SetTestClient(clientset)
	t.Cleanup(func() {
		k8s.SetTestConfig(prevConfig)
		k8s.SetTestClient(prevClient)
	})
	// Answers are reused per identity for a few seconds; each test starts clean.
	prevReader := kueuevisibility.Positions
	kueuevisibility.Positions = kueuevisibility.NewReader()
	t.Cleanup(func() { kueuevisibility.Positions = prevReader })
	return fake
}

func decodePosition(t *testing.T, response *http.Response) KueuePositionResponse {
	t.Helper()
	var got KueuePositionResponse
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestProxyAuth_KueuePositionGatesBeforeAnyLiveRead(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
		canGet    bool
		status    int
		code      readreason.Code
	}{
		{"namespace not visible", "private", true, http.StatusForbidden, readreason.UserNamespaceNotVisible},
		{"cannot get the Workload", "default", false, http.StatusForbidden, readreason.UserForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := setupKueuePosition(t, true)
			env := newAuthTestServer(t)
			permissions := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
			permissions.SetCanI("get", kueueGroup, "workloads", tc.namespace, tc.canGet)
			env.srv.permCache.Set("alice", nil, permissions)
			response := env.authGet(t, "/api/kueue/position/"+tc.namespace+"/pending", "alice", "")
			defer response.Body.Close()
			var body map[string]string
			_ = json.NewDecoder(response.Body).Decode(&body)
			if response.StatusCode != tc.status || body["error_code"] != string(tc.code) {
				t.Fatalf("got %d %v", response.StatusCode, body)
			}
			if fake.count() != 0 {
				t.Fatal("a refused request must not reach Kueue")
			}
		})
	}
}

func TestProxyAuth_KueuePositionReadsAsTheUser(t *testing.T) {
	fake := setupKueuePosition(t, true)
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	permissions.SetCanI("get", kueueGroup, "workloads", "default", true)
	env.srv.permCache.Set("alice", nil, permissions)

	response := env.authGet(t, "/api/kueue/position/default/pending", "alice", "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	got := decodePosition(t, response)
	if !got.Eligible || got.UID != "pending-uid" || got.Position == nil || got.Position.Status != resourcecontext.KueuePositionListed {
		t.Fatalf("got %+v", got)
	}
	if *got.Position.AheadInLocalQueue != 0 || *got.Position.AheadInClusterQueue != 2 || got.Position.LocalQueue != "pos" {
		t.Fatalf("position %+v", got.Position)
	}
	if fake.count() != 1 || fake.impersonated[0] != "alice" {
		t.Fatalf("expected one read impersonating alice, got %v", fake.impersonated)
	}
	if path := fake.requests[0].URL.Path; path != "/apis/visibility.kueue.x-k8s.io/v1beta2/namespaces/default/localqueues/pos/pendingworkloads" {
		t.Fatalf("path %s", path)
	}

	// Kueue's own RBAC refusal is the answer, attributed to the user.
	fake.mu.Lock()
	fake.status, fake.body = http.StatusForbidden, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"forbidden","details":{"name":"pos","group":"visibility.kueue.x-k8s.io","kind":"localqueues"}}`
	fake.mu.Unlock()
	permissionsBob := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	permissionsBob.SetCanI("get", kueueGroup, "workloads", "default", true)
	env.srv.permCache.Set("bob", nil, permissionsBob)
	response = env.authGet(t, "/api/kueue/position/default/pending", "bob", "")
	defer response.Body.Close()
	got = decodePosition(t, response)
	if got.Position == nil || got.Position.Status != resourcecontext.KueuePositionUnavailable || got.Position.Code != readreason.UserForbidden {
		t.Fatalf("bob: %+v", got.Position)
	}
	if fake.impersonated[len(fake.impersonated)-1] != "bob" {
		t.Fatal("bob's read must not reuse alice's answer")
	}
}

func TestProxyAuth_KueuePositionSkipsWorkloadsKueueDoesNotHoldPending(t *testing.T) {
	fake := setupKueuePosition(t, true)
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	permissions.SetCanI("get", kueueGroup, "workloads", "default", true)
	env.srv.permCache.Set("alice", nil, permissions)
	response := env.authGet(t, "/api/kueue/position/default/admitted", "alice", "")
	defer response.Body.Close()
	got := decodePosition(t, response)
	if got.Eligible || got.Position != nil || got.UID != "admitted-uid" {
		t.Fatalf("got %+v", got)
	}
	if fake.count() != 0 {
		t.Fatal("an admitted Workload must not be looked up")
	}
}

func TestKueuePositionNotServedWithoutAsking(t *testing.T) {
	fake := setupKueuePosition(t, false)
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	permissions.SetCanI("get", kueueGroup, "workloads", "default", true)
	env.srv.permCache.Set("alice", nil, permissions)
	response := env.authGet(t, "/api/kueue/position/default/pending", "alice", "")
	defer response.Body.Close()
	got := decodePosition(t, response)
	if got.Position == nil || got.Position.Status != resourcecontext.KueuePositionNotServed {
		t.Fatalf("got %+v", got.Position)
	}
	if fake.count() != 0 {
		t.Fatal("no read when the API is not served")
	}
}

func TestKueuePositionWithoutAuthReadsAsRadar(t *testing.T) {
	fake := setupKueuePosition(t, true)
	fake.status, fake.body = http.StatusForbidden, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"forbidden","details":{"name":"pos","group":"visibility.kueue.x-k8s.io","kind":"localqueues"}}`
	srv := New(Config{DevMode: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Stop() })
	response, err := http.Get(ts.URL + "/api/kueue/position/default/pending")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got := decodePosition(t, response)
	if got.Position == nil || got.Position.Code != readreason.RadarForbidden {
		t.Fatalf("got %+v", got.Position)
	}
	if fake.count() != 1 || fake.impersonated[0] != "" {
		t.Fatalf("without a user Radar reads as itself, never impersonating: %v", fake.impersonated)
	}

	// A web page in the viewer's browser gets nothing, even without auth.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/kueue/position/default/pending", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSite, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer crossSite.Body.Close()
	if crossSite.StatusCode != http.StatusForbidden || fake.count() != 1 {
		t.Fatalf("cross-site read: %d, %d reads", crossSite.StatusCode, fake.count())
	}
}

func TestKueuePositionCapabilityAdvertised(t *testing.T) {
	srv := New(Config{DevMode: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Stop() })
	response, err := http.Get(ts.URL + "/api/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var caps struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.NewDecoder(response.Body).Decode(&caps); err != nil {
		t.Fatal(err)
	}
	if !caps.Features["kueuePendingPosition"] {
		t.Fatalf("features %v", caps.Features)
	}
}

func TestKueuePositionDropsAnswerAfterConnectionChange(t *testing.T) {
	fake := setupKueuePosition(t, true)
	started, release := make(chan struct{}), make(chan struct{})
	fake.onRead = func() { close(started); <-release }
	srv := New(Config{DevMode: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Stop() })
	result := make(chan *http.Response, 1)
	failure := make(chan error, 1)
	go func() {
		response, err := http.Get(ts.URL + "/api/kueue/position/default/pending")
		if err != nil {
			failure <- err
			return
		}
		result <- response
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("visibility request did not start")
	}
	restore := k8s.SetTestProfileSource("different-kubeconfig", "different-context", "user")
	defer restore()
	close(release)
	select {
	case err := <-failure:
		t.Fatal(err)
	case response := <-result:
		defer response.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusConflict || body["error_code"] != string(readreason.ConnectionChanged) || body["position"] != nil {
			t.Fatalf("changed connection returned %d %+v", response.StatusCode, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not complete")
	}
}

func TestProxyAuth_KueuePositionNamespaceCheckFailureIsRetryable(t *testing.T) {
	visibility := setupKueuePosition(t, true)
	fakeNamespaceAPIServer(t, http.StatusOK, http.StatusInternalServerError, "default")
	env := newAuthTestServer(t)
	response := env.authGet(t, "/api/kueue/position/default/pending", "alice", "")
	defer response.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	if response.StatusCode != http.StatusServiceUnavailable || body["error_code"] != string(readreason.UserPermissionCheckFailed) {
		t.Fatalf("got %d %v", response.StatusCode, body)
	}
	if visibility.count() != 0 {
		t.Fatal("an unchecked namespace must not reach Kueue")
	}
}

func TestKueuePositionRespectsNamespacePin(t *testing.T) {
	visibility := setupKueuePosition(t, true)
	previous := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("other")
	t.Cleanup(func() { k8s.ForceNamespaceScope = previous; k8s.SetFallbackNamespace("") })
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	permissions.SetCanI("get", kueueGroup, "workloads", "default", true)
	env.srv.permCache.Set("alice", nil, permissions)
	response := env.authGet(t, "/api/kueue/position/default/pending", "alice", "")
	defer response.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	if response.StatusCode != http.StatusConflict || body["error_code"] != string(readreason.OutsideNamespacePin) {
		t.Fatalf("got %d %v", response.StatusCode, body)
	}
	if visibility.count() != 0 {
		t.Fatal("a pinned-out namespace must not reach Kueue")
	}
}
