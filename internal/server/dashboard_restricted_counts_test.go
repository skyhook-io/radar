package server

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// useRBACNamespacesCache swaps in the cache an install limited by
// rbac.namespaces gets: Pods collected in three namespaces, and no Namespaces
// or Nodes informer because the ServiceAccount can't list them.
func useRBACNamespacesCache(t *testing.T) {
	t.Helper()
	client := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "alpha"}},
	)
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCacheNamespaces(client,
		map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "alpha"}},
		map[string][]string{k8score.Pods: {"alpha", "beta", "gamma"}},
	); err != nil {
		t.Fatal(err)
	}
}

func decodeDashboard(t *testing.T, resp *http.Response) DashboardResponse {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body DashboardResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	return body
}

func TestDashboardCountsWithoutRadarNamespaceAndNodeListers(t *testing.T) {
	useRBACNamespacesCache(t)

	// The picker narrows the workloads Home counts, not the namespace total,
	// which matches the listed count.
	for _, tc := range []struct{ name, query string }{
		{"all namespaces", ""},
		{"picker inside collection", "?namespaces=alpha"},
		{"picker outside collection", "?namespaces=delta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(testServer.URL + "/api/dashboard" + tc.query)
			if err != nil {
				t.Fatal(err)
			}
			counts := decodeDashboard(t, resp).ResourceCounts
			if counts.Namespaces != 3 {
				t.Errorf("namespaces = %d, want 3 (the namespaces Radar collects)", counts.Namespaces)
			}
			for _, kind := range []string{"nodes", "namespaces"} {
				if !slices.Contains(counts.Restricted, kind) {
					t.Errorf("restricted = %v, want it to include %q", counts.Restricted, kind)
				}
			}
			if counts.Nodes.Total != 0 {
				t.Errorf("nodes = %+v, want no count without a Nodes informer", counts.Nodes)
			}
		})
	}
}

func TestDashboardAuthUserNamespaceCountWithoutRadarNamespaceLister(t *testing.T) {
	useRBACNamespacesCache(t)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta", "delta"}}
	perms.SetCanI("list", "", "nodes", "", true)
	perms.SetCanI("list", "", "namespaces", "", true)
	env.srv.permCache.Set("ns-reader", nil, perms)

	for _, query := range []string{"", "?namespaces=alpha"} {
		counts := decodeDashboard(t, env.authGet(t, "/api/dashboard"+query, "ns-reader", "")).ResourceCounts
		// The user's own namespaces, narrowed to those Radar collects; never
		// Radar's full set, which includes gamma.
		if counts.Namespaces != 2 {
			t.Errorf("%q: namespaces = %d, want 2", query, counts.Namespaces)
		}
		for _, kind := range []string{"nodes", "namespaces"} {
			if !slices.Contains(counts.Restricted, kind) {
				t.Errorf("%q: restricted = %v, want it to include %q", query, counts.Restricted, kind)
			}
		}
	}
}

func TestDashboardAuthUserWhoCanListSeesExactCounts(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "nodes", "", true)
	perms.SetCanI("list", "", "namespaces", "", true)
	env.srv.permCache.Set("admin", nil, perms)

	counts := decodeDashboard(t, env.authGet(t, "/api/dashboard", "admin", "")).ResourceCounts
	if counts.Namespaces == 0 {
		t.Error("namespaces = 0, want the cluster's namespace count")
	}
	if slices.Contains(counts.Restricted, "nodes") || slices.Contains(counts.Restricted, "namespaces") {
		t.Errorf("restricted = %v, want neither nodes nor namespaces", counts.Restricted)
	}
}
