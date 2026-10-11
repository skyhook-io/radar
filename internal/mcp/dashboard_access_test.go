package mcp

import (
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// A missing TLS Secret is learned from the Secret, so the dashboard reports it
// only to a caller who can read Secrets in that namespace.
func TestDashboardSecretDerivedProblemsNeedSecretAccess(t *testing.T) {
	ns := "shop-tls"
	client := fake.NewClientset(&networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "front", Namespace: ns, CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec:       networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}, SecretName: "shop-tls"}}},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	ctx := withTestUserPerms(t, "tls-user", nil, []string{ns})
	perms := getPermCache().Get("tls-user", nil)
	perms.SetCanI("get", "", "secrets", ns, false)

	tlsProblem := func() bool {
		for _, p := range buildDashboard(ctx, k8s.GetResourceCache(), ns, false, false).Problems {
			if p.Name == "front" && p.Reason == "Missing TLS Secret" {
				return true
			}
		}
		return false
	}
	if tlsProblem() {
		t.Fatal("a caller without Secret access learned whether the TLS Secret exists")
	}
	perms.SetCanI("get", "", "secrets", ns, true)
	if !tlsProblem() {
		t.Fatal("a caller with Secret access lost the missing-TLS problem")
	}
}

// A cluster-scoped problem needs list on its kind, even for a caller who can
// read Pods everywhere.
func TestDashboardClusterScopedProblemsNeedListOnTheKind(t *testing.T) {
	client := fake.NewClientset(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "dangling"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "gone"},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: "someone"}},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	ctx := withTestUserPerms(t, "pod-reader", nil, nil)
	perms := getPermCache().Get("pod-reader", nil)
	perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterrolebindings", "", false)

	dangling := func() bool {
		for _, p := range buildDashboard(ctx, k8s.GetResourceCache(), "", false, false).Problems {
			if p.Kind == "ClusterRoleBinding" && p.Name == "dangling" {
				return true
			}
		}
		return false
	}
	if dangling() {
		t.Fatal("a caller who can't list ClusterRoleBindings saw one's problem")
	}
	perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterrolebindings", "", true)
	if !dangling() {
		t.Fatal("a caller who can list ClusterRoleBindings lost the problem")
	}
}
