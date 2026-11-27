package prometheus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// bindingTestStore is a metrics store that answers discovery's probe and
// records every other query it receives.
func bindingTestStore(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.FormValue("query")
		if q == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/query_range") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(queries)
	}
}

// connectBindingTestClient points the global client at url and connects it.
func connectBindingTestClient(t *testing.T, url string) *Client {
	t.Helper()
	SetManualURL(url)
	client, err := ClientForOperation()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	return client
}

// initBindingTestCluster gives the resource cache one Deployment, shop/api,
// and a fresh global metrics client.
func initBindingTestCluster(t *testing.T) *k8s.ResourceCache {
	t.Helper()
	one := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app"}},
		}}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(deployment)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	cache := k8s.GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := cache.Deployments().Deployments("shop").Get("api"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	Initialize(nil, nil, "binding-test", nil)
	t.Cleanup(func() {
		Reset()
		Initialize(nil, nil, "", nil)
	})
	return cache
}

// replaceRightsizingScopeResolver swaps rightsizing's check for one that
// answers an unfiltered scope after running during, and counts its runs.
func replaceRightsizingScopeResolver(t *testing.T, during func()) *int {
	t.Helper()
	checks := 0
	prev := resolveRightsizingScope
	resolveRightsizingScope = func(_ context.Context, _ *Client, req ViewScopeRequest, _ *k8s.ResourceCache) (ViewScope, error) {
		checks++
		during()
		filters := map[string]string{}
		for _, m := range req.Metrics {
			filters[m.Name] = ""
		}
		return ViewScope{State: ViewScopeSingleClusterAsserted, Filters: filters, Note: viewScopeSingleNote}, nil
	}
	t.Cleanup(func() { resolveRightsizingScope = prev })
	return &checks
}

// Rightsizing's check and its queries must describe one backend: when the
// connection moves from store A to store B during the check, B's approval of
// unfiltered queries must not send them to A, nor mix in B's answers.
func TestRightsizingCheckOnAMovedConnectionWithholds(t *testing.T) {
	initBindingTestCluster(t)
	storeA, queriesA := bindingTestStore(t)
	storeB, queriesB := bindingTestStore(t)
	connectBindingTestClient(t, storeA.URL)
	checks := replaceRightsizingScopeResolver(t, func() { connectBindingTestClient(t, storeB.URL) })

	resp, err := RightsizingForWorkload(context.Background(), "Deployment", "shop", "api")
	if err != nil {
		t.Fatal(err)
	}
	if *checks != 1 {
		t.Fatalf("checks = %d, want 1", *checks)
	}
	if resp.Reason != ReasonClusterScopeWithheld || resp.SampleAvailable {
		t.Errorf("reason = %q sample = %v, want %q and no sample", resp.Reason, resp.SampleAvailable, ReasonClusterScopeWithheld)
	}
	if resp.ClusterScope == nil || resp.ClusterScope.State != ViewScopeUnknown {
		t.Errorf("clusterScope = %+v, want the unknown state", resp.ClusterScope)
	}
	if got := queriesA(); len(got) != 0 {
		t.Errorf("rightsizing queries reached the old store: %q", got)
	}
	if got := queriesB(); len(got) != 0 {
		t.Errorf("rightsizing queries reached the new store: %q", got)
	}
}

// Unmoved, the queries run on the store the check approved.
func TestRightsizingCheckOnTheSameConnectionQueriesItsStore(t *testing.T) {
	initBindingTestCluster(t)
	store, queries := bindingTestStore(t)
	connectBindingTestClient(t, store.URL)
	replaceRightsizingScopeResolver(t, func() {})

	resp, err := RightsizingForWorkload(context.Background(), "Deployment", "shop", "api")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reason == ReasonClusterScopeWithheld {
		t.Fatalf("withheld on an unmoved connection: %+v", resp)
	}
	if len(queries()) == 0 {
		t.Fatal("no rightsizing query reached the store")
	}
}

// A store that can't be reached is a failed query, not a cluster scope that
// couldn't be checked: no check runs and the reason says the queries failed.
func TestRightsizingOnAnUnreachableStoreReportsFailedQueries(t *testing.T) {
	initBindingTestCluster(t)
	store, _ := bindingTestStore(t)
	connectBindingTestClient(t, store.URL)
	store.Close()
	checks := replaceRightsizingScopeResolver(t, func() {})

	resp, err := RightsizingForWorkload(context.Background(), "Deployment", "shop", "api")
	if err != nil {
		t.Fatal(err)
	}
	if *checks != 0 {
		t.Errorf("checks = %d, want none without a store", *checks)
	}
	if resp.Reason != ReasonRightsizingQueriesFailed || resp.SampleAvailable || resp.ClusterScope != nil {
		t.Errorf("reason = %q sample = %v clusterScope = %+v, want %q, no sample and no scope", resp.Reason, resp.SampleAvailable, resp.ClusterScope, ReasonRightsizingQueriesFailed)
	}
}

func TestBindStoreAfterAFailedConnectFailsItsQueries(t *testing.T) {
	initBindingTestCluster(t)
	store, queries := bindingTestStore(t)
	client := connectBindingTestClient(t, store.URL)
	wantErr := errors.New("connect failed")
	bound := boundStore{err: wantErr}
	if _, err := bound.Query(context.Background(), "up"); !errors.Is(err, wantErr) {
		t.Errorf("query error = %v, want the connect error", err)
	}
	if bound, err := client.bindStore(context.Background()); err != nil || bound.store == nil {
		t.Fatalf("bind on a reachable store: %+v err=%v", bound, err)
	}
	store.Close()
	bound, err := client.bindStore(context.Background())
	if err == nil || bound.store != nil {
		t.Fatalf("bind on an unreachable store: %+v err=%v", bound, err)
	}
	if _, qErr := bound.Query(context.Background(), "x"); !errors.Is(qErr, err) {
		t.Errorf("query error = %v, want the connect error %v", qErr, err)
	}
	if got := queries(); len(got) != 0 {
		t.Errorf("queries reached the store: %q", got)
	}
}

func TestRightsizingScanCheckOnAMovedConnectionWithholds(t *testing.T) {
	for _, moved := range []bool{true, false} {
		name := "unmoved"
		if moved {
			name = "moved during the check"
		}
		t.Run(name, func(t *testing.T) {
			cache := initBindingTestCluster(t)
			storeA, queriesA := bindingTestStore(t)
			storeB, queriesB := bindingTestStore(t)
			client := connectBindingTestClient(t, storeA.URL)
			replaceRightsizingScopeResolver(t, func() {
				if moved {
					connectBindingTestClient(t, storeB.URL)
				}
			})

			scope := RightsizingScanScope{NamespacesByKind: map[string][]string{"Deployment": {"shop"}}}
			resp := runRightsizingScan(context.Background(), scope, client, cache, func(RightsizingScanResponse) {})
			if !moved {
				if resp.Reason == ScanReasonClusterScopeWithheld || len(queriesA()) == 0 {
					t.Fatalf("unmoved scan withheld or never queried its store: reason=%q queries=%q", resp.Reason, queriesA())
				}
				return
			}
			if resp.State != RightsizingScanUnavailable || resp.Reason != ScanReasonClusterScopeWithheld {
				t.Errorf("state=%s reason=%q, want unavailable and %q", resp.State, resp.Reason, ScanReasonClusterScopeWithheld)
			}
			if resp.ClusterScope == nil || resp.ClusterScope.State != ViewScopeUnknown {
				t.Errorf("clusterScope = %+v, want the unknown state", resp.ClusterScope)
			}
			if len(resp.Workloads) != 1 {
				t.Errorf("workloads = %d, want the one workload listed as withheld", len(resp.Workloads))
			}
			if got := queriesA(); len(got) != 0 {
				t.Errorf("scan queries reached the old store: %q", got)
			}
			if got := queriesB(); len(got) != 0 {
				t.Errorf("scan queries reached the new store: %q", got)
			}
		})
	}
}

func TestResolveOnConnection(t *testing.T) {
	approve := ViewScope{State: ViewScopeSingleClusterAsserted, Filters: map[string]string{"m": ""}}
	for _, tc := range []struct {
		name         string
		before       bool
		after        bool
		wantResolved bool
		wantUnknown  bool
	}{
		{name: "unchanged", before: true, after: true, wantResolved: true},
		{name: "moved before the check", before: false, after: false, wantUnknown: true},
		{name: "moved during the check", before: true, after: false, wantResolved: true, wantUnknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, resolved := 0, false
			current := func() bool {
				calls++
				if calls == 1 {
					return tc.before
				}
				return tc.after
			}
			scope, err := resolveOnConnection(current, func() (ViewScope, error) {
				resolved = true
				return approve, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if resolved != tc.wantResolved {
				t.Errorf("resolved = %v, want %v", resolved, tc.wantResolved)
			}
			if unknown := scope.State == ViewScopeUnknown && scope.Withheld; unknown != tc.wantUnknown {
				t.Errorf("scope = %+v, want unknown=%v", scope, tc.wantUnknown)
			}
		})
	}
}
