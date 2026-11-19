package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// discoveryAPI stands in for the apiserver during namespace discovery: it
// lists team-a and team-b, answers SubjectAccessReviews from decide, counts
// discoveries by their first review (the cluster-wide pod list check),
// signals each on arrived, and holds every review until gate closes.
type discoveryAPI struct {
	client      *kubernetes.Clientset
	discoveries atomic.Int32
	arrived     chan struct{}
}

func newDiscoveryAPI(t *testing.T, gate <-chan struct{}, decide func(authorizationv1.ResourceAttributes) (allowed, fail bool)) *discoveryAPI {
	t.Helper()
	api := &discoveryAPI{arrived: make(chan struct{}, 64)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/namespaces":
			_, _ = w.Write([]byte(`{"kind":"NamespaceList","apiVersion":"v1","items":[{"metadata":{"name":"team-a"}},{"metadata":{"name":"team-b"}}]}`))
			return
		case "/apis/authorization.k8s.io/v1/subjectaccessreviews":
		default:
			http.NotFound(w, r)
			return
		}
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Spec.ResourceAttributes == nil {
			http.Error(w, "bad review", http.StatusBadRequest)
			return
		}
		attrs := *review.Spec.ResourceAttributes
		if attrs.Resource == "pods" && attrs.Namespace == "" && attrs.Verb == "list" {
			api.discoveries.Add(1)
			api.arrived <- struct{}{}
		}
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		allowed, fail := decide(attrs)
		if fail {
			http.Error(w, "apiserver unavailable", http.StatusInternalServerError)
			return
		}
		review.Status.Allowed = allowed
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host:          server.URL,
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
		QPS:           -1,
	})
	if err != nil {
		t.Fatalf("build Kubernetes client: %v", err)
	}
	api.client = client
	return api
}

// install makes api the cluster connection, with no informer cache, so
// discovery lists namespaces from it.
func (api *discoveryAPI) install(t *testing.T) {
	t.Helper()
	previous := k8s.SetTestClient(api.client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	t.Cleanup(k8s.SwapResourceCacheForTest(nil))
	getPermCache().Invalidate()
	t.Cleanup(getPermCache().Invalidate)
}

// waitForDiscoveries gives n unshared discoveries time to reach the
// apiserver. A shared discovery's count doesn't depend on it: a call that
// misses the run finds its answer cached.
func (api *discoveryAPI) waitForDiscoveries(n int32) {
	deadline := time.Now().Add(200 * time.Millisecond)
	for api.discoveries.Load() < n && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

// awaitDiscovery blocks until a discovery reaches the apiserver.
func (api *discoveryAPI) awaitDiscovery(t *testing.T) {
	t.Helper()
	select {
	case <-api.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("no discovery reached the apiserver")
	}
}

// allowOnly allows pod lists in namespaces; nil allows them cluster-wide.
func allowOnly(namespaces ...string) func(authorizationv1.ResourceAttributes) (bool, bool) {
	return func(a authorizationv1.ResourceAttributes) (bool, bool) {
		if a.Namespace == "" {
			return namespaces == nil, false
		}
		return slices.Contains(namespaces, a.Namespace), false
	}
}

func userContext(username string, groups ...string) context.Context {
	return pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: username, Groups: groups})
}

// Tool calls that miss the permission cache at once share one namespace
// discovery, whatever it decides: nil (every namespace), a list, or none.
func TestResolveUserPerms_ConcurrentMissesShareOneDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed []string
	}{
		{"cluster-wide", nil},
		{"one namespace", []string{"team-a"}},
		{"no namespace", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := make(chan struct{})
			api := newDiscoveryAPI(t, gate, allowOnly(tc.allowed...))
			api.install(t)

			const n = 8
			got := make([]*pkgauth.UserPermissions, n)
			failed := make([]bool, n)
			var wg sync.WaitGroup
			for i := range got {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, got[i], failed[i] = resolveUserPermsWithStatus(userContext("reader", "viewers"))
				}()
			}
			api.waitForDiscoveries(n)
			close(gate)
			wg.Wait()

			if d := api.discoveries.Load(); d != 1 {
				t.Fatalf("discoveries = %d, want 1 shared by %d tool calls", d, n)
			}
			for i, perms := range got {
				if failed[i] {
					t.Fatalf("call %d failed closed", i)
				}
				if (perms.AllowedNamespaces == nil) != (tc.allowed == nil) || !slices.Equal(perms.AllowedNamespaces, tc.allowed) {
					t.Fatalf("call %d namespaces = %#v, want %#v", i, perms.AllowedNamespaces, tc.allowed)
				}
			}
			if cached := getPermCache().Get("reader", []string{"viewers"}); cached != got[0] {
				t.Fatal("callers did not share the cached entry")
			}
		})
	}
}

// A discovery the apiserver fails denies every tool call sharing it, caches
// nothing, and the next call discovers again.
func TestResolveUserPerms_SharedDiscoveryErrorFailsClosed(t *testing.T) {
	gate := make(chan struct{})
	api := newDiscoveryAPI(t, gate, func(authorizationv1.ResourceAttributes) (bool, bool) { return false, true })
	api.install(t)

	const n = 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, perms, failed := resolveUserPermsWithStatus(userContext("reader"))
			if !failed || perms.AllowedNamespaces == nil || len(perms.AllowedNamespaces) != 0 {
				t.Errorf("perms = %#v, failed = %v; want a fail-closed []", perms, failed)
			}
		}()
	}
	api.waitForDiscoveries(n)
	close(gate)
	wg.Wait()
	if getPermCache().Get("reader", nil) != nil {
		t.Fatal("a failed discovery was cached")
	}
	resolveUserPerms(userContext("reader"))
	if d := api.discoveries.Load(); d != 2 {
		t.Fatalf("discoveries = %d, want a fresh one after the failure", d)
	}
}

// A tool call that gives up while another waits fails closed and leaves the
// shared discovery running for the one still waiting.
func TestResolveUserPerms_CallGivingUpKeepsTheSharedDiscovery(t *testing.T) {
	gate := make(chan struct{})
	api := newDiscoveryAPI(t, gate, allowOnly())
	api.install(t)
	waiting := make(chan *pkgauth.UserPermissions, 1)
	go func() {
		_, perms := resolveUserPerms(userContext("reader"))
		waiting <- perms
	}()
	api.awaitDiscovery(t)

	ctx, cancel := context.WithTimeout(userContext("reader"), 50*time.Millisecond)
	defer cancel()
	if _, perms, failed := resolveUserPermsWithStatus(ctx); !failed || perms.AllowedNamespaces == nil || len(perms.AllowedNamespaces) != 0 {
		t.Fatalf("the call that gave up: perms = %#v, failed = %v; want a fail-closed []", perms, failed)
	}
	close(gate)
	if got := <-waiting; got == nil || got.AllowedNamespaces != nil {
		t.Fatal("the waiting call didn't get every namespace")
	}
	if d := api.discoveries.Load(); d != 1 {
		t.Fatalf("discoveries = %d, want the call that gave up to have joined the shared one", d)
	}
}

// A same-name reconnect can reach a different cluster, so a tool call after
// it never joins a discovery sent over the previous connection.
func TestResolveUserPerms_ReconnectStartsANewDiscovery(t *testing.T) {
	gate := make(chan struct{})
	old := newDiscoveryAPI(t, gate, allowOnly())
	old.install(t)
	earlier := make(chan struct{})
	go func() {
		defer close(earlier)
		resolveUserPerms(userContext("reader"))
	}()
	old.awaitDiscovery(t)

	fresh := newDiscoveryAPI(t, nil, allowOnly([]string{}...))
	t.Cleanup(k8s.ReconnectTestClient(fresh.client))
	getPermCache().Invalidate()

	ctx, cancel := context.WithTimeout(userContext("reader"), 5*time.Second)
	defer cancel()
	_, perms, failed := resolveUserPermsWithStatus(ctx)
	close(gate)
	<-earlier
	if failed || perms.AllowedNamespaces == nil || len(perms.AllowedNamespaces) != 0 {
		t.Fatalf("perms = %#v, failed = %v; want the new cluster's []", perms, failed)
	}
	if d := fresh.discoveries.Load(); d != 1 {
		t.Fatalf("discoveries on the new connection = %d, want 1", d)
	}
}

// A discovery sent over the previous connection that finishes after a
// reconnect answers its own tool call but never replaces the entry, or the
// verdicts, cached for the new connection.
func TestResolveUserPerms_LateDiscoveryKeepsTheNewConnectionsEntry(t *testing.T) {
	gate := make(chan struct{})
	old := newDiscoveryAPI(t, gate, allowOnly())
	old.install(t)
	late := make(chan *pkgauth.UserPermissions, 1)
	go func() {
		_, perms := resolveUserPerms(userContext("reader"))
		late <- perms
	}()
	old.awaitDiscovery(t)

	fresh := newDiscoveryAPI(t, nil, allowOnly([]string{}...))
	t.Cleanup(k8s.ReconnectTestClient(fresh.client))
	_, perms, failed := resolveUserPermsWithStatus(userContext("reader"))
	if failed || perms.AllowedNamespaces == nil || len(perms.AllowedNamespaces) != 0 {
		t.Fatalf("perms = %#v, failed = %v; want the new cluster's []", perms, failed)
	}
	perms.SetCanI("list", "", "nodes", "", false)
	close(gate)
	if got := <-late; got == nil || got.AllowedNamespaces != nil {
		t.Fatal("the earlier call didn't get its own discovery's every-namespace answer")
	}

	cached := getPermCache().Get("reader", nil)
	if cached != perms {
		t.Fatal("the new connection's entry was replaced")
	}
	if _, ok := cached.CanI("list", "", "nodes", ""); !ok {
		t.Fatal("the verdict cached for the new connection was dropped")
	}
}
