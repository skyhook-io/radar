package server

import (
	"encoding/json"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/search"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"net/http"
	"testing"
)

func TestSearchNewNamespacedKindsReportCallerDenial(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "default"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "default"}},
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "limits", Namespace: "default"}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "quota", Namespace: "default"}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "default"}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "binding", Namespace: "default"}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"}},
	))
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	env.srv.permCache.Set("search-kind-reader", nil, perms)
	for _, kind := range search.NamespacedSearchKinds {
		t.Run(kind.Kind, func(t *testing.T) {
			perms.SetCanI("list", kind.Group, kind.Resource, "default", false)
			resp := env.authGet(t, "/api/search?context=none&q=kind:"+kind.Kind, "search-kind-reader", "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			var result search.Result
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, gap := range result.Unsearched {
				if gap.Kind == kind.Kind && gap.Group == kind.Group && gap.Reason == "rbac_denied" {
					found = true
				}
			}
			if !result.Partial || !found || len(result.Hits) != 0 {
				t.Fatalf("kind gate: %+v", result)
			}
			perms.SetCanI("list", kind.Group, kind.Resource, "default", true)
			allowed := env.authGet(t, "/api/search?context=none&q=kind:"+kind.Kind, "search-kind-reader", "")
			defer allowed.Body.Close()
			if allowed.StatusCode != http.StatusOK {
				t.Fatalf("allowed status %d", allowed.StatusCode)
			}
			if err := json.NewDecoder(allowed.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if len(result.Hits) != 1 {
				t.Fatalf("authorized kind omitted: %+v", result)
			}
			for _, gap := range result.Unsearched {
				if gap.Kind == kind.Kind {
					t.Fatalf("authorized kind reports gap: %+v", result)
				}
			}
		})
	}
}
