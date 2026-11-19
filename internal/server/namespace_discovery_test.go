package server

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

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// discoveryAPI stands in for the apiserver's SubjectAccessReview endpoint
// during namespace discovery. It counts discoveries by their first review, the
// cluster-wide pod list check, signals each on arrived, and holds every review
// until gate closes.
type discoveryAPI struct {
	client      *kubernetes.Clientset
	discoveries atomic.Int32
	arrived     chan struct{}
}

func newDiscoveryAPI(t *testing.T, gate <-chan struct{}, decide func(authv1.ResourceAttributes) (allowed, fail bool)) *discoveryAPI {
	t.Helper()
	api := &discoveryAPI{arrived: make(chan struct{}, 64)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			http.NotFound(w, r)
			return
		}
		var review authv1.SubjectAccessReview
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
		w.Header().Set("Content-Type", "application/json")
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

func (api *discoveryAPI) install(t *testing.T) {
	t.Helper()
	previous := k8s.SetTestClient(api.client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
}

// waitForDiscoveries gives n unshared discoveries time to reach the
// apiserver. A shared discovery's count doesn't depend on it: a request that
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
func allowOnly(namespaces ...string) func(authv1.ResourceAttributes) (bool, bool) {
	return func(a authv1.ResourceAttributes) (bool, bool) {
		if a.Namespace == "" {
			return namespaces == nil, false
		}
		return slices.Contains(namespaces, a.Namespace), false
	}
}

// Requests that miss the permission cache at once share one namespace
// discovery, whatever it decides: nil (every namespace), a list, or none.
func TestGetUserNamespaces_ConcurrentMissesShareOneDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed []string
	}{
		{"cluster-wide", nil},
		{"one namespace", []string{"default"}},
		{"no namespace", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := make(chan struct{})
			api := newDiscoveryAPI(t, gate, allowOnly(tc.allowed...))
			api.install(t)
			s := newAuthServer(auth.Config{Mode: "proxy"})
			user := &auth.User{Username: "reader", Groups: []string{"viewers"}}

			const n = 8
			got := make([][]string, n)
			failed := make([]bool, n)
			var wg sync.WaitGroup
			for i := range got {
				wg.Add(1)
				go func() {
					defer wg.Done()
					got[i], failed[i] = s.getUserNamespacesWithStatus(requestWithUser(http.MethodGet, "/api/resources/pods", user), nil)
				}()
			}
			api.waitForDiscoveries(n)
			close(gate)
			wg.Wait()

			if d := api.discoveries.Load(); d != 1 {
				t.Fatalf("discoveries = %d, want 1 shared by %d requests", d, n)
			}
			for i := range got {
				if failed[i] {
					t.Fatalf("request %d failed closed", i)
				}
				if (got[i] == nil) != (tc.allowed == nil) || !slices.Equal(got[i], tc.allowed) {
					t.Fatalf("request %d namespaces = %#v, want %#v", i, got[i], tc.allowed)
				}
			}
			cached := s.permCache.Get(user.Username, user.Groups)
			if cached == nil {
				t.Fatal("the shared discovery was not cached")
			}
			if (cached.AllowedNamespaces == nil) != (tc.allowed == nil) || !slices.Equal(cached.AllowedNamespaces, tc.allowed) {
				t.Fatalf("cached namespaces = %#v, want %#v", cached.AllowedNamespaces, tc.allowed)
			}
		})
	}
}

// A discovery the apiserver fails denies every request sharing it, caches
// nothing, and the next request discovers again.
func TestGetUserNamespaces_SharedDiscoveryErrorFailsClosed(t *testing.T) {
	gate := make(chan struct{})
	api := newDiscoveryAPI(t, gate, func(authv1.ResourceAttributes) (bool, bool) { return false, true })
	api.install(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "reader"}

	const n = 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, failed := s.getUserNamespacesWithStatus(requestWithUser(http.MethodGet, "/api/resources/pods", user), nil)
			if !failed || got == nil || len(got) != 0 {
				t.Errorf("namespaces = %#v, failed = %v; want a fail-closed []", got, failed)
			}
		}()
	}
	api.waitForDiscoveries(n)
	close(gate)
	wg.Wait()
	if s.permCache.Get(user.Username, user.Groups) != nil {
		t.Fatal("a failed discovery was cached")
	}
	s.getUserNamespacesWithStatus(requestWithUser(http.MethodGet, "/api/resources/pods", user), nil)
	if d := api.discoveries.Load(); d != 2 {
		t.Fatalf("discoveries = %d, want a fresh one after the failure", d)
	}
}

// A request that gives up while another waits fails closed and leaves the
// shared discovery running for the one still waiting.
func TestGetUserNamespaces_RequestGivingUpKeepsTheSharedDiscovery(t *testing.T) {
	gate := make(chan struct{})
	api := newDiscoveryAPI(t, gate, allowOnly())
	api.install(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "reader"}

	type result struct {
		namespaces []string
		failed     bool
	}
	waiting := make(chan result, 1)
	go func() {
		ns, failed := s.getUserNamespacesWithStatus(requestWithUser(http.MethodGet, "/api/resources/pods", user), nil)
		waiting <- result{ns, failed}
	}()
	api.awaitDiscovery(t)

	ctx, cancel := context.WithTimeout(auth.ContextWithUser(context.Background(), user), 50*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/resources/pods", nil).WithContext(ctx)
	if ns, failed := s.getUserNamespacesWithStatus(r, nil); !failed || ns == nil || len(ns) != 0 {
		t.Fatalf("the request that gave up = %#v, failed = %v; want a fail-closed []", ns, failed)
	}
	close(gate)
	if got := <-waiting; got.failed || got.namespaces != nil {
		t.Fatalf("waiting request = %#v, want every namespace", got)
	}
	if d := api.discoveries.Load(); d != 1 {
		t.Fatalf("discoveries = %d, want the request that gave up to have joined the shared one", d)
	}
}

// A same-name reconnect can reach a different cluster, so a request after it
// never joins a discovery sent over the previous connection.
func TestGetUserNamespaces_ReconnectStartsANewDiscovery(t *testing.T) {
	gate := make(chan struct{})
	old := newDiscoveryAPI(t, gate, allowOnly())
	old.install(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "reader"}
	earlier := make(chan struct{})
	go func() {
		defer close(earlier)
		s.getUserNamespacesWithStatus(requestWithUser(http.MethodGet, "/api/resources/pods", user), nil)
	}()
	old.awaitDiscovery(t)

	fresh := newDiscoveryAPI(t, nil, func(authv1.ResourceAttributes) (bool, bool) { return false, false })
	t.Cleanup(k8s.ReconnectTestClient(fresh.client))
	s.permCache.Invalidate()

	ctx, cancel := context.WithTimeout(auth.ContextWithUser(context.Background(), user), 5*time.Second)
	defer cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/resources/pods", nil).WithContext(ctx)
	got, failed := s.getUserNamespacesWithStatus(r, nil)
	close(gate)
	<-earlier
	if failed || got == nil || len(got) != 0 {
		t.Fatalf("namespaces = %#v, failed = %v; want the new cluster's []", got, failed)
	}
	if d := fresh.discoveries.Load(); d != 1 {
		t.Fatalf("discoveries on the new connection = %d, want 1", d)
	}
}

// A discovery sent over the previous connection that finishes after a
// reconnect answers its own request but never replaces the entry, or the
// verdicts, cached for the new connection.
func TestGetUserNamespaces_LateDiscoveryKeepsTheNewConnectionsEntry(t *testing.T) {
	gate := make(chan struct{})
	old := newDiscoveryAPI(t, gate, allowOnly())
	old.install(t)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "reader"}
	late := make(chan []string, 1)
	go func() {
		got, _ := s.getUserNamespacesWithStatus(requestWithUser(http.MethodGet, "/api/resources/pods", user), nil)
		late <- got
	}()
	old.awaitDiscovery(t)

	fresh := newDiscoveryAPI(t, nil, allowOnly([]string{}...))
	t.Cleanup(k8s.ReconnectTestClient(fresh.client))
	if got, failed := s.getUserNamespacesWithStatus(requestWithUser(http.MethodGet, "/api/resources/pods", user), nil); failed || got == nil || len(got) != 0 {
		t.Fatalf("namespaces = %#v, failed = %v; want the new cluster's []", got, failed)
	}
	s.permCache.Get(user.Username, user.Groups).SetCanI("list", "", "nodes", "", false)
	close(gate)
	if got := <-late; got != nil {
		t.Fatalf("the earlier request got %#v, want its own discovery's every-namespace answer", got)
	}

	cached := s.permCache.Get(user.Username, user.Groups)
	if cached == nil || cached.AllowedNamespaces == nil || len(cached.AllowedNamespaces) != 0 {
		t.Fatal("the new cluster's [] entry was replaced")
	}
	if _, ok := cached.CanI("list", "", "nodes", ""); !ok {
		t.Fatal("the verdict cached for the new connection was dropped")
	}
}
