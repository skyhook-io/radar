package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
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
	if changes := fieldWriterChanges(obj, nil, nil); changes == nil || len(changes) != 0 {
		t.Fatalf("changes = %#v, want an empty list", changes)
	}
}

func TestFluxLeadNeedsConfirmedDirectEvidence(t *testing.T) {
	claim := func(check string) topology.ManagerEvidence {
		return topology.ManagerEvidence{Claims: []topology.ManagerClaim{{Manager: *helmOwner, Source: "flux-helm-labels", Check: check}}}
	}
	for _, tc := range []struct {
		name   string
		answer *topology.ManagerAnswer
		want   bool
	}{
		{"nil", nil, false},
		{"none", &topology.ManagerAnswer{State: "none"}, false},
		{"argo", &topology.ManagerAnswer{State: "agree", Lead: argoOwner}, false},
		{"listed", &topology.ManagerAnswer{State: "listed", Lead: helmOwner}, true},
		{"agree", &topology.ManagerAnswer{State: "agree", Lead: helmOwner}, true},
		{"claimed unreadable", &topology.ManagerAnswer{State: "claimed", Lead: helmOwner, Direct: claim("unreadable")}, true},
		{"claimed recorded", &topology.ManagerAnswer{State: "claimed", Lead: helmOwner, Direct: claim("recorded")}, true},
		{"claimed missing", &topology.ManagerAnswer{State: "claimed", Lead: helmOwner, Direct: claim("missing")}, false},
		{"claimed remote", &topology.ManagerAnswer{State: "claimed", Lead: helmOwner, Direct: claim("remote")}, false},
		{"claimed weak", &topology.ManagerAnswer{State: "claimed", Lead: helmOwner, Direct: topology.ManagerEvidence{Claims: []topology.ManagerClaim{{Manager: *helmOwner, Check: "unreadable", Weak: true}}}}, false},
		{"via an ancestor", &topology.ManagerAnswer{State: "agree", Lead: helmOwner, Via: &topology.ManagerEvidence{Recorders: []topology.ResourceRef{*helmOwner}}}, false},
		{"no lead namespace", &topology.ManagerAnswer{State: "listed", Lead: &topology.ResourceRef{Group: helmOwner.Group, Kind: "HelmRelease", Name: "api"}}, false},
	} {
		if got := fluxLead(tc.answer) != nil; got != tc.want {
			t.Errorf("%s: fluxLead != nil is %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFieldWritersReadsTheRootPolicy(t *testing.T) {
	helmGVR := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	fd := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fd.Resources = []*metav1.APIResourceList{{GroupVersion: "helm.toolkit.fluxcd.io/v2", APIResources: []metav1.APIResource{
		{Name: "helmreleases", Kind: "HelmRelease", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}},
	}}}
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	obj := evidenceTarget(t, nil, []metav1.ManagedFieldsEntry{
		fieldsEntry(t, "kubectl-patch", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{}}}),
	})
	withRoot := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{helmGVR: "HelmReleaseList"}, fluxHelmOwner(map[string]any{"mode": "warn"}))
	if changes := fieldWritersFor(context.Background(), withRoot, obj, helmOwner); len(changes) != 1 || changes[0].Outcome != "reported" {
		t.Fatalf("changes = %+v", changes)
	}
	noRoot := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{helmGVR: "HelmReleaseList"})
	if changes := fieldWritersFor(context.Background(), noRoot, obj, helmOwner); len(changes) != 1 || changes[0].Outcome != "unknown" {
		t.Fatalf("changes = %+v", changes)
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
