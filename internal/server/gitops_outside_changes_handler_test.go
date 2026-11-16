package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"github.com/skyhook-io/radar/pkg/topology"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
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

// dynamicLister serves metadata lists from a fake dynamic client, which has no
// metadata-only mode: only the fields a metadata list carries are kept.
func dynamicLister(dyn dynamic.Interface) outsideLister {
	return func(ctx context.Context, gvr schema.GroupVersionResource, namespace string, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
		list, err := dyn.Resource(gvr).Namespace(namespace).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		out := &metav1.PartialObjectMetadataList{ListMeta: metav1.ListMeta{Continue: list.GetContinue()}}
		for _, item := range list.Items {
			var meta metav1.PartialObjectMetadata
			raw, _, _ := unstructured.NestedMap(item.Object, "metadata")
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &meta.ObjectMeta); err != nil {
				return nil, err
			}
			meta.TypeMeta = metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"}
			out.Items = append(out.Items, meta)
		}
		return out, nil
	}
}

func collectOutside(ctx context.Context, dyn dynamic.Interface, groups []outsideListGroup, selector string, limits outsideLimits, cov *outsideCoverage) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	listOutsideObjects(ctx, dynamicLister(dyn), groups, selector, limits, cov, func(_ context.Context, _ outsideListGroup, obj *unstructured.Unstructured) {
		out = append(out, obj)
	})
	return out
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
	objs := collectOutside(context.Background(), dyn, groups, selector, defaultOutsideLimits, &cov)
	if len(objs) != 2 || cov.Listed != 2 || cov.Truncated || len(cov.Denied) != 1 || cov.Denied[0] != (outsideDenied{Resource: "services", Namespace: "team"}) || len(cov.Failed) != 0 {
		t.Fatalf("objects = %d, coverage = %+v", len(objs), cov)
	}
	for _, s := range selectors {
		if s != selector {
			t.Fatalf("list selector = %q, want %q", s, selector)
		}
	}

	cov = outsideCoverage{}
	objs = collectOutside(context.Background(), dyn, groups, selector, outsideLimits{Lists: 40, Objects: 1, Timeout: time.Second}, &cov)
	if len(objs) != 1 || cov.Listed != 1 || !cov.Truncated {
		t.Fatalf("objects = %d, coverage = %+v", len(objs), cov)
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
	collectOutside(context.Background(), dyn, []outsideListGroup{{GVR: servicesGVR, Kind: "Service", Namespace: "team"}}, "a=b", defaultOutsideLimits, &cov)
	if len(cov.Denied) != 0 || len(cov.Failed) != 1 || cov.Failed[0] != (outsideDenied{Resource: "services", Namespace: "team"}) {
		t.Fatalf("coverage = %+v", cov)
	}
}

func TestBuildOutsideChangesWithoutAClientIsUnavailable(t *testing.T) {
	if _, err := buildOutsideChanges(context.Background(), nil, nil, nil, fluxHelmOwner(nil), helmOwner, nil); err == nil {
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
	resp, err := buildOutsideChanges(context.Background(), dynamicLister(dyn), dyn, tree, root, helmOwner, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(resp)
	if resp.Count != 1 || !strings.Contains(string(body), "data.password") || strings.Contains(string(body), "hunter2") {
		t.Fatalf("response = %s", body)
	}
}

func TestListOutsideObjectsKeepsOnlyMembersAndPages(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{deploymentsGVR: "DeploymentList"},
		labelledDeployment("a"), labelledDeployment("b"))
	var limits []int64
	dyn.PrependReactor("list", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		limits = append(limits, action.(k8stesting.ListActionImpl).ListOptions.Limit)
		return false, nil, nil
	})
	var cov outsideCoverage
	objs := collectOutside(context.Background(), dyn, []outsideListGroup{{GVR: deploymentsGVR, Kind: "Deployment", Namespace: "team", Members: map[string]bool{"a": true}}}, "", defaultOutsideLimits, &cov)
	if len(objs) != 1 || objs[0].GetName() != "a" || cov.Listed != 1 {
		t.Fatalf("objects = %d, coverage = %+v", len(objs), cov)
	}
	if len(limits) != 1 || limits[0] <= 0 || limits[0] > int64(defaultOutsideLimits.Objects) {
		t.Fatalf("list limits = %v", limits)
	}
}

func TestBuildOutsideChangesCountsFoldedActionsPerObject(t *testing.T) {
	fd := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fd.Resources = []*metav1.APIResourceList{{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
		{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}},
	}}}
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	restart := map[string]any{"f:spec": map[string]any{"f:template": map[string]any{"f:metadata": map[string]any{"f:annotations": map[string]any{"f:kubectl.kubernetes.io/restartedAt": map[string]any{}}}}}}
	d := labelledDeployment("a")
	d.SetLabels(map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"})
	d.Object["metadata"].(map[string]any)["managedFields"] = []any{
		map[string]any{"manager": "kubectl-rollout", "operation": "Update", "fieldsType": "FieldsV1", "fieldsV1": restart},
		map[string]any{"manager": "radar", "operation": "Update", "fieldsType": "FieldsV1", "fieldsV1": restart},
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{deploymentsGVR: "DeploymentList"}, d)
	resp, err := buildOutsideChanges(context.Background(), dynamicLister(dyn), dyn, &gitopstree.ResourceTree{Nodes: []gitopstree.Node{treeNode("apps", "Deployment", "team", "a")}}, kustomizationRoot(), &topology.ResourceRef{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Harmless) != 1 || resp.Harmless[0].Count != 1 {
		t.Fatalf("harmless = %+v", resp.Harmless)
	}
}

// An inventory member without Flux's labels (removed by hand, not yet
// re-applied) isn't returned by the label LIST; the answer names it rather
// than reading clean.
func TestListOutsideObjectsNamesMembersTheListMissed(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{deploymentsGVR: "DeploymentList", servicesGVR: "ServiceList"},
		labelledDeployment("a"))
	dyn.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", nil)
	})
	var cov outsideCoverage
	collectOutside(context.Background(), dyn, []outsideListGroup{
		{GVR: deploymentsGVR, Kind: "Deployment", Namespace: "team", Members: map[string]bool{"a": true, "b": true}},
		{GVR: servicesGVR, Kind: "Service", Namespace: "team", Members: map[string]bool{"web": true}},
	}, fluxMemberSelector(fluxHelmOwner(nil)), defaultOutsideLimits, &cov)
	want := []outsideRef{{Group: "apps", Kind: "Deployment", Namespace: "team", Name: "b"}}
	if !reflect.DeepEqual(cov.Unlisted, want) || cov.UnlistedMore != 0 {
		t.Fatalf("coverage = %+v", cov)
	}
}

// Members are listed as metadata; the full object is read only for a hand
// edit whose value the answer shows.
func TestBuildOutsideChangesReadsFullObjectsOnlyForShownValues(t *testing.T) {
	fd := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{}}
	fd.Resources = []*metav1.APIResourceList{{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
		{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}},
	}}}
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	member := func(name, manager string, fields map[string]any) *unstructured.Unstructured {
		d := labelledDeployment(name)
		d.Object["metadata"].(map[string]any)["managedFields"] = []any{
			map[string]any{"manager": manager, "operation": "Update", "fieldsType": "FieldsV1", "fieldsV1": fields},
		}
		d.Object["spec"] = map[string]any{"replicas": int64(5), "paused": true}
		return d
	}
	scaled := member("scaled", "kubectl-scale", map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{}}})
	paused := member("paused", "kubectl-patch", map[string]any{"f:spec": map[string]any{"f:paused": map[string]any{}}})
	controlled := member("controlled", "helm-controller", map[string]any{"f:spec": map[string]any{"f:replicas": map[string]any{}}})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{deploymentsGVR: "DeploymentList"}, scaled, paused, controlled)
	var gets []string
	dyn.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		gets = append(gets, action.(k8stesting.GetAction).GetName())
		return false, nil, nil
	})
	root := fluxHelmOwner(nil)
	root.Object["status"] = map[string]any{"inventory": map[string]any{"entries": []any{}}}
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{
		treeNode("apps", "Deployment", "team", "scaled"), treeNode("apps", "Deployment", "team", "paused"), treeNode("apps", "Deployment", "team", "controlled"),
	}}
	resp, err := buildOutsideChanges(context.Background(), dynamicLister(dyn), dyn, tree, root, helmOwner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gets, []string{"scaled"}) {
		t.Fatalf("full reads = %q, want only the member whose value is shown", gets)
	}
	values := map[string]map[string]string{}
	for _, o := range resp.Objects {
		values[o.Ref.Name] = o.Changes[0].Values
	}
	if resp.Count != 2 || values["scaled"]["spec.replicas"] != "5" || values["paused"] != nil {
		t.Fatalf("objects = %+v", resp.Objects)
	}
}

// A drift-ignore rule resolves against the object's content; when the full
// read fails the path isn't counted as ignored.
func TestClassifyOutsideObjectWithoutContentKeepsIgnoredPathsUncovered(t *testing.T) {
	obj := evidenceTarget(t, nil, []metav1.ManagedFieldsEntry{fieldsEntry(t, "kubectl-edit", metav1.ManagedFieldsOperationUpdate, map[string]any{"f:spec": map[string]any{"f:paused": map[string]any{}}})})
	owner := fluxHelmOwner(map[string]any{"mode": "enabled", "ignore": []any{map[string]any{"paths": []any{"/spec/paused"}}}})
	reads := 0
	out, _, _ := classifyOutsideObject(obj, helmOwner, owner, func() *unstructured.Unstructured { reads++; return nil })
	if reads != 1 || len(out.Changes) != 1 || out.Changes[0].Outcome != "reverted" {
		t.Fatalf("reads = %d, changes = %+v", reads, out.Changes)
	}
	reads = 0
	out, _, _ = classifyOutsideObject(obj, helmOwner, fluxHelmOwner(map[string]any{"mode": "enabled"}), func() *unstructured.Unstructured { reads++; return nil })
	if reads != 0 || len(out.Changes) != 1 {
		t.Fatalf("read the object with no rule or value to resolve: reads = %d, changes = %+v", reads, out.Changes)
	}
}
