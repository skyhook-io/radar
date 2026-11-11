package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
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
	if len(objs) != 2 || cov.Listed != 2 || cov.Truncated || len(cov.Denied) != 1 || cov.Denied[0] != (outsideDenied{Resource: "services", Namespace: "team"}) || len(cov.Failed) != 0 {
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
	build := func(count int) func() (outsideChangesResponse, bool) {
		return func() (outsideChangesResponse, bool) { builds++; return outsideChangesResponse{Count: count}, true }
	}
	if got := m.loadOr(a, build(1)); got.Count != 1 {
		t.Fatalf("a = %+v", got)
	}
	if got := m.loadOr(b, build(2)); got.Count != 2 || builds != 2 {
		t.Fatalf("b = %+v, builds = %d", got, builds)
	}
	if got := m.loadOr(a, build(9)); got.Count != 1 || builds != 2 {
		t.Fatalf("cached a = %+v, builds = %d", got, builds)
	}
	m.clear()
	if got := m.loadOr(a, build(3)); got.Count != 3 || builds != 3 {
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

func TestListOutsideObjectsSeparatesFailuresFromDenials(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{deploymentsGVR: "DeploymentList", servicesGVR: "ServiceList"})
	dyn.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(context.DeadlineExceeded)
	})
	var cov outsideCoverage
	listOutsideObjects(context.Background(), dyn, []outsideListGroup{{GVR: servicesGVR, Kind: "Service", Namespace: "team"}}, "a=b", defaultOutsideLimits, &cov)
	if len(cov.Denied) != 0 || len(cov.Failed) != 1 || cov.Failed[0] != (outsideDenied{Resource: "services", Namespace: "team"}) {
		t.Fatalf("coverage = %+v", cov)
	}
}

func TestOutsideChangesMemoSkipsUncacheable(t *testing.T) {
	m := newOutsideChangesMemo(time.Minute)
	builds := 0
	build := func(cacheable bool) func() (outsideChangesResponse, bool) {
		return func() (outsideChangesResponse, bool) {
			builds++
			return outsideChangesResponse{Count: builds}, cacheable
		}
	}
	m.loadOr("k", build(false))
	if got := m.loadOr("k", build(true)); got.Count != 2 {
		t.Fatalf("uncacheable answer was served again: %+v", got)
	}
	if got := m.loadOr("k", build(true)); got.Count != 2 {
		t.Fatalf("cacheable answer not kept: %+v", got)
	}
}

func TestBuildOutsideChangesWithoutAClientIsUnavailable(t *testing.T) {
	if _, err := buildOutsideChanges(context.Background(), nil, nil, fluxHelmOwner(nil), helmOwner, nil); err == nil {
		t.Fatal("no dynamic client answered as if nothing changed")
	}
}

// Without authentication the LISTs run as Radar; the answer still carries only
// manager names, times, paths and allowlisted values.
func TestBuildOutsideChangesReturnsNoDataValues(t *testing.T) {
	configMapsGVR := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	fd := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fd.Resources = []*metav1.APIResourceList{{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}},
	}}}
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	cm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"namespace": "prod", "name": "settings", "labels": map[string]any{
			"helm.toolkit.fluxcd.io/name": "api", "helm.toolkit.fluxcd.io/namespace": "prod"},
			"managedFields": []any{map[string]any{"manager": "kubectl-edit", "operation": "Update", "fieldsType": "FieldsV1",
				"fieldsV1": map[string]any{"f:data": map[string]any{"f:password": map[string]any{}}}}}},
		"data": map[string]any{"password": "hunter2"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{configMapsGVR: "ConfigMapList"}, cm)
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{treeNode("", "ConfigMap", "prod", "settings")}}
	root := fluxHelmOwner(nil)
	root.Object["status"] = map[string]any{"inventory": map[string]any{"entries": []any{}}}
	resp, err := buildOutsideChanges(context.Background(), dyn, tree, root, helmOwner, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(resp)
	if resp.Count != 1 || !strings.Contains(string(body), "data.password") || strings.Contains(string(body), "hunter2") {
		t.Fatalf("response = %s", body)
	}
}
