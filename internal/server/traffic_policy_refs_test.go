package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/traffic"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// A policy's name is a read of that policy: the plugin's attribution of a
// flow is delivered only for kinds the caller may list, and a cluster-wide
// policy needs the cluster-scoped grant.
func TestRedactPolicyRefsPerCaller(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"shop"}}
	perms.SetCanI("list", "networking.k8s.io", "networkpolicies", "shop", true)
	perms.SetCanI("list", "cilium.io", "ciliumnetworkpolicies", "shop", false)
	perms.SetCanI("list", "cilium.io", "ciliumclusterwidenetworkpolicies", "", false)
	env.srv.permCache.Set("reader", nil, perms)

	req, _ := http.NewRequest("GET", "/", nil)
	user := &auth.User{Username: "reader"}
	req = req.WithContext(pkgauth.ContextWithUser(context.Background(), user))

	pv := &traffic.PolicyVerdict{
		DeniedBy: []traffic.PolicyRef{
			{Kind: "NetworkPolicy", Namespace: "shop", Name: "deny-all"},
			{Kind: "CiliumNetworkPolicy", Namespace: "shop", Name: "l7"},
			{Kind: "CiliumClusterwideNetworkPolicy", Name: "segmentation"},
			{Kind: "SomeFuturePolicy", Namespace: "shop", Name: "x"},
		},
		AllowedBy: []traffic.PolicyRef{
			{Kind: "NetworkPolicy", Namespace: "shop", Name: "allow-web"},
			{Kind: "CiliumNetworkPolicy", Namespace: "shop", Name: "allow-l7"},
		},
	}
	got := env.srv.redactPolicyRefs(req, pv)
	if len(got.DeniedBy) != 1 || got.DeniedBy[0].Name != "deny-all" {
		t.Fatalf("DeniedBy = %+v", got.DeniedBy)
	}
	if len(got.AllowedBy) != 1 || got.AllowedBy[0].Name != "allow-web" {
		t.Fatalf("AllowedBy = %+v", got.AllowedBy)
	}
	if got.Withheld != 3 {
		t.Fatalf("Withheld = %d, want 3 (denies only; an unreadable allow is dropped uncounted)", got.Withheld)
	}
	if env.srv.redactPolicyRefs(req, nil) != nil {
		t.Fatal("nil verdict must stay nil")
	}
}
