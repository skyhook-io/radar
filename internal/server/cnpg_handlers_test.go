package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

// A ClusterImageCatalog is cluster-scoped and referenceable from any namespace,
// which is why this lookup is an endpoint rather than a client-side list: asking
// the generic resources endpoint without a namespace inherits the caller's
// namespace view filter, and "no cluster uses this catalog" is exactly the
// sentence someone reads before editing it.

// A caller who may not list Clusters is told so. Returning an empty list would
// read as "nothing depends on this catalog", which is the answer that gets a
// catalog edited out from under a running database.
func TestCNPGCatalogUsers_DeniesRatherThanReportingNoUsers(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
	allow(perms, cnpgsvc.Group, "clusters", "", false)
	env.srv.permCache.Set("nobody", nil, perms)

	resp := env.authGet(t, "/api/cnpg/clusterimagecatalogs/postgres-fleet/clusters", "nobody", "")
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403 for a caller who may not list clusters", resp.StatusCode)
	}
}

// The namespaced route authorizes against that namespace, not cluster-wide.
func TestCNPGCatalogUsers_NamespacedRouteAuthorizesInItsNamespace(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"pg"}}
	allow(perms, cnpgsvc.Group, "clusters", "pg", false)
	env.srv.permCache.Set("scoped", nil, perms)

	resp := env.authGet(t, "/api/cnpg/imagecatalogs/pg/postgres-pinned/clusters", "scoped", "")
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403 — the namespaced route must gate on its own namespace", resp.StatusCode)
	}
}

// An empty result is only an absence if the cache actually looked. The informer
// for a namespace is started BY the read, so a gate that refuses to read until
// one exists never lets the first read happen — ListBlocking starts it and waits
// instead, which is what makes a subsequent empty list mean something.
func TestHandlersEstablishAbsenceRatherThanAssumeIt(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	items, err := listDynamicSynced(context.Background(), k8s.GetResourceCache(), "Cluster", cnpgsvc.Group, "cold-empty")
	if err != nil || len(items) != 0 {
		t.Fatalf("cold empty namespace = %v, %v", items, err)
	}
	if !k8s.GetDynamicResourceCache().IsNamespaceSynced(cnpgsvc.ClusterGVR, "cold-empty") {
		t.Fatal("empty result did not establish the requested scope")
	}
}

func TestFailedDynamicReadDoesNotEstablishAbsence(t *testing.T) {
	seedCNPGFallbackCache(t, fallbackFixture{fallbacks: []string{"healthy"}, fail: inNamespaces("broken")})
	budget := newSyncBudget(context.Background())
	budget.deadline = time.Now().Add(30 * time.Millisecond)
	items, err := listDynamicSyncedWithin(context.Background(), k8s.GetResourceCache(), "Cluster", cnpgsvc.Group, "broken", budget)
	if !errors.Is(err, integration.ErrDynamicNotSynced) || len(items) != 0 {
		t.Fatalf("failed inventory must be unread rather than empty success: %v, %v", items, err)
	}
}
