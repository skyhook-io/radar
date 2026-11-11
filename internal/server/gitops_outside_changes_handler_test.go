package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/pkg/auth"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

var (
	deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	servicesGVR    = schema.GroupVersionResource{Version: "v1", Resource: "services"}
)

func labelledDeployment(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"namespace": "team", "name": name, "labels": map[string]any{
			"helm.toolkit.fluxcd.io/name": "api", "helm.toolkit.fluxcd.io/namespace": "prod"}},
	}}
}

func TestListOutsideObjectsBoundsAndDenied(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{deploymentsGVR: "DeploymentList", servicesGVR: "ServiceList"},
		labelledDeployment("a"), labelledDeployment("b"))
	selector := fluxMemberSelector(fluxHelmOwner(nil))
	var selectors []string
	dyn.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		selectors = append(selectors, action.(k8stesting.ListAction).GetListRestrictions().Labels.String())
		if action.GetResource().Resource == "services" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", nil)
		}
		return false, nil, nil
	})
	groups := []outsideListGroup{{GVR: deploymentsGVR, Kind: "Deployment", Namespace: "team"}, {GVR: servicesGVR, Kind: "Service", Namespace: "team"}}

	var cov outsideCoverage
	objs := listOutsideObjects(context.Background(), dyn, groups, selector, defaultOutsideLimits, &cov)
	if len(objs) != 2 || cov.Listed != 2 || cov.Truncated || len(cov.Denied) != 1 || cov.Denied[0] != (outsideDenied{Resource: "services", Namespace: "team"}) {
		t.Fatalf("objects = %d, coverage = %+v", len(objs), cov)
	}
	for _, s := range selectors {
		if s != selector {
			t.Fatalf("list selector = %q, want %q", s, selector)
		}
	}

	cov = outsideCoverage{}
	objs = listOutsideObjects(context.Background(), dyn, groups, selector, outsideLimits{Lists: 40, Objects: 1, Timeout: time.Second}, &cov)
	if len(objs) != 1 || cov.Listed != 1 || !cov.Truncated {
		t.Fatalf("objects = %d, coverage = %+v", len(objs), cov)
	}
}

func TestOutsideChangesMemoIsPerUser(t *testing.T) {
	m := newOutsideChangesMemo(time.Minute)
	root := "\x00helm.toolkit.fluxcd.io/HelmRelease/prod/api\x00rv1"
	a := gitopsIssuesMemoKey(&auth.User{Username: "a"}, nil) + root
	b := gitopsIssuesMemoKey(&auth.User{Username: "b"}, nil) + root
	if a == b {
		t.Fatal("two users share a memo key")
	}
	builds := 0
	build := func(count int) func() outsideChangesResponse {
		return func() outsideChangesResponse { builds++; return outsideChangesResponse{Count: count} }
	}
	if got := m.load(a, build(1)); got.Count != 1 {
		t.Fatalf("a = %+v", got)
	}
	if got := m.load(b, build(2)); got.Count != 2 || builds != 2 {
		t.Fatalf("b = %+v, builds = %d", got, builds)
	}
	if got := m.load(a, build(9)); got.Count != 1 || builds != 2 {
		t.Fatalf("cached a = %+v, builds = %d", got, builds)
	}
	m.clear()
	if got := m.load(a, build(3)); got.Count != 3 || builds != 3 {
		t.Fatalf("after clear a = %+v, builds = %d", got, builds)
	}
}

func TestHandleGitOpsOutsideChangesRejectsNonFlux(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?group=argoproj.io", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("kind", "applications")
	route.URLParams.Add("namespace", "argocd")
	route.URLParams.Add("name", "app")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
	w := httptest.NewRecorder()
	(&Server{}).handleGitOpsOutsideChanges(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}
