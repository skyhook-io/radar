package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestCNPGCachedReadFailurePreservesCauseWithoutExposingIt(t *testing.T) {
	cause := errors.New("internal cache failure details")
	_, err := cnpgCachedClusterResult(nil, cause, "pg", "orders")
	var failure *cnpgsvc.ReadFailure
	if !errors.Is(err, cause) || !errors.As(err, &failure) || failure.Status != http.StatusInternalServerError {
		t.Fatalf("cache failure = %v", err)
	}
	w := httptest.NewRecorder()
	(&Server{}).writeCNPGCachedReadError(w, err, "pg", "orders")
	if w.Code != http.StatusInternalServerError || w.Body.String() != "{\"error\":\"failed to read CloudNativePG Cluster\"}\n" {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestCNPGReadAdapterRefusesSupersededCluster(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds, withUID(cnpgObj(cnpgsvc.Group+"/v1", "Cluster", "pg", "orders", nil, nil), "orders-uid"))
	srv := &Server{}
	r := httptest.NewRequest("GET", "/", nil)
	reader := srv.cnpgReader(r)
	ctx := context.Background()
	cache, cluster, err := reader.Observations.Cluster(ctx, "pg", "orders")
	if err != nil || cache == nil || cluster.GetUID() != "orders-uid" {
		t.Fatalf("initial read = %v %v %v", cache, cluster, err)
	}
	k8s.CancelOngoingOperations()
	if _, _, err := reader.Observations.Cluster(ctx, "pg", "orders"); !errors.Is(err, cnpgsvc.ErrCNPGDisconnected) {
		t.Fatalf("superseded read = %v", err)
	}
	grant := auth.Grant{Group: cnpgsvc.Group, Resource: "clusters", Verb: "get", Namespace: "pg"}
	if reader.Access.CanRead(ctx, grant.Group, grant.Resource, grant.Namespace, grant.Verb) || reader.Access.Permission(ctx, grant) != integration.PermissionDenied {
		t.Fatal("superseded adapter accepted a new permission verdict")
	}
	if _, err := reader.Observations.DynamicList(ctx, cache, "Cluster", cnpgsvc.Group, "pg"); !errors.Is(err, integration.ErrDynamicNotSynced) {
		t.Fatalf("superseded dynamic read = %v", err)
	}
	if access, objects := reader.Observations.WorkspaceRead(ctx, cache, cnpgWorkspaceFixtureKinds[0], []string{"pg"}, []string{cnpgsvc.Group}); access.State != integration.KindCoverageSyncing || len(objects) != 0 {
		t.Fatalf("superseded workspace read = %+v %+v", access, objects)
	}
	if _, cluster, err := srv.cnpgReader(r).Observations.Cluster(ctx, "pg", "orders"); err != nil || cluster.GetUID() != "orders-uid" {
		t.Fatalf("fresh adapter did not recover: %v %v", cluster, err)
	}
}

func TestCNPGActionClientBindingRejectsDifferentReviewedContext(t *testing.T) {
	seedCNPGWorkspace(t, cnpgWorkspaceTestKinds)
	_, err := (&Server{}).cnpgActionClients(httptest.NewRequest("POST", "/", nil), integration.ActionRequest{ReviewedContext: "different-context", UID: "orders-uid"})
	var refusal *integration.ActionError
	if !errors.As(err, &refusal) || refusal.Status != 409 || refusal.Code != integration.ActionCodeContextChanged {
		t.Fatalf("wrong-context client binding = %v", err)
	}
}
