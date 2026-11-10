package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestCNPGCachedReadFailurePreservesCauseWithoutExposingIt(t *testing.T) {
	for _, kind := range []string{"Cluster", "Pooler"} {
		t.Run(kind, func(t *testing.T) {
			cause := errors.New("internal cache failure details")
			_, err := cnpgCachedResourceResult(nil, cause, kind, "pg", "orders")
			var failure *cnpgsvc.ReadFailure
			if !errors.Is(err, cause) || !errors.As(err, &failure) || failure.Status != http.StatusInternalServerError {
				t.Fatalf("cache failure = %v", err)
			}
			w := httptest.NewRecorder()
			(&Server{}).writeCNPGCachedReadError(w, err, "pg", "orders")
			if w.Code != http.StatusInternalServerError || w.Body.String() != "{\"error\":\"failed to read CloudNativePG "+kind+"\"}\n" {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCNPGCachedReadDistinguishesMissingFromSyncing(t *testing.T) {
	for _, kind := range []string{"Cluster", "Pooler"} {
		for _, tc := range []struct {
			name    string
			cause   error
			status  int
			message string
		}{
			{"missing", nil, http.StatusNotFound, "CloudNativePG " + kind + " pg/orders not found"},
			{"not installed", k8s.ErrUnknownDynamicKind, http.StatusNotFound, "CloudNativePG " + kind + " pg/orders not found"},
			{"syncing", integration.ErrDynamicNotSynced, http.StatusServiceUnavailable, "CloudNativePG " + kind + "s are still syncing"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				_, err := cnpgCachedResourceResult(nil, tc.cause, kind, "pg", "orders")
				w := httptest.NewRecorder()
				(&Server{}).writeCNPGCachedReadError(w, err, "pg", "orders")
				if w.Code != tc.status || w.Body.String() != "{\"error\":\""+tc.message+"\"}\n" {
					t.Fatalf("response = %d %s", w.Code, w.Body.String())
				}
			})
		}
		object := &unstructured.Unstructured{}
		if got, err := cnpgCachedResourceResult(object, nil, kind, "pg", "orders"); err != nil || got != object {
			t.Fatalf("successful %s read = %v %v", kind, got, err)
		}
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
	if access, read := reader.Observations.TypedScope(ctx, cache, []string{"pg"}, "", "pods"); access.State != integration.KindCoverageSyncing || len(read) != 0 {
		t.Fatalf("superseded typed read = %+v %+v", access, read)
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

func TestCNPGReadAdapterBuildsCallerClientsAfterCapture(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Impersonate-User") != "alice" || r.Header.Get("Impersonate-Group") != "operators" {
			t.Errorf("caller identity = %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p","namespace":"pg"}}`))
	}))
	defer api.Close()
	previous := k8s.SetTestConfig(&rest.Config{Host: api.URL, WrapTransport: func(next http.RoundTripper) http.RoundTripper {
		if err := k8s.TryClusterConfiguration(func() error { return nil }); err != nil {
			t.Errorf("client construction held the read-capture lock: %v", err)
		}
		return next
	}})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })
	r := requestWithUser(http.MethodGet, "/", &auth.User{Username: "alice", Groups: []string{"operators"}})
	reader := (&Server{}).cnpgReader(r)
	if !reader.captured || reader.Clients.Typed == nil || reader.dynamic == nil || reader.config.Impersonate.UserName != "alice" {
		t.Fatal("caller-bound dependencies were not captured")
	}
	if _, err := reader.Clients.Typed.CoreV1().Pods("pg").Get(context.Background(), "p", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestCNPGReadAdapterClientConstructionFailureHasNoTypedNil(t *testing.T) {
	previous := k8s.SetTestConfig(&rest.Config{Host: "://invalid"})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })
	r := requestWithUser(http.MethodGet, "/", &auth.User{Username: "alice", Groups: []string{"operators"}})
	reader := (&Server{}).cnpgReader(r)
	if !reader.captured || reader.Clients.Typed != nil || reader.dynamic != nil {
		t.Fatal("failed client construction retained a typed-nil interface")
	}
}
