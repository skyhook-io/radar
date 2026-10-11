package mcp

import (
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
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
