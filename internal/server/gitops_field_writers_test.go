package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/pkg/topology"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFieldWritersReturnsChangesForFluxManagedObject(t *testing.T) {
	obj := evidenceTarget(t, nil, []metav1.ManagedFieldsEntry{
		fieldsEntry(t, "helm-controller", metav1.ManagedFieldsOperationApply, map[string]any{"f:spec": map[string]any{"f:template": map[string]any{}}}),
		fieldsEntry(t, "kubectl-patch", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{}}}),
		fieldsEntry(t, "kubectl-rollout", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:template": map[string]any{"f:metadata": map[string]any{"f:annotations": map[string]any{"f:kubectl.kubernetes.io/restartedAt": map[string]any{}}}}}}),
	})
	manager := &topology.ManagerAnswer{State: "agree", Lead: helmOwner}
	changes := fieldWriterChanges(obj, fluxLead(manager), fluxHelmOwner(map[string]any{"mode": "warn"}))
	if len(changes) != 1 || changes[0].Tool != "kubectl patch" || changes[0].Outcome != "reported" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestFieldWritersNoFluxManager(t *testing.T) {
	obj := evidenceTarget(t, nil, []metav1.ManagedFieldsEntry{
		fieldsEntry(t, "kubectl-patch", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{}}}),
	})
	argo := &topology.ManagerAnswer{State: "agree", Lead: argoOwner}
	for _, m := range []*topology.ManagerAnswer{nil, {State: "none"}, argo} {
		if lead := fluxLead(m); lead != nil {
			t.Fatalf("fluxLead(%+v) = %+v", m, lead)
		}
		if changes := fieldWriterChanges(obj, fluxLead(m), nil); changes == nil || len(changes) != 0 {
			t.Fatalf("changes = %#v, want an empty list", changes)
		}
	}
}

func TestFieldWritersRefusesSecrets(t *testing.T) {
	for _, kind := range []string{"secrets", "Secret"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("kind", kind)
		route.URLParams.Add("namespace", "prod")
		route.URLParams.Add("name", "creds")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		(&Server{}).handleGitOpsFieldWriters(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d", kind, w.Code)
		}
	}
}
