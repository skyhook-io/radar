package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

var testEndpointSliceGVR = schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}

func testEndpointSlice(namespace, name string, labels map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{"name": name, "namespace": namespace}
	if labels != nil {
		metadata["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion":  "discovery.k8s.io/v1",
		"kind":        "EndpointSlice",
		"metadata":    metadata,
		"addressType": "IPv4",
	}}
}

func installEndpointSliceClient(t *testing.T, objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{testEndpointSliceGVR: "EndpointSliceList"},
		objects...,
	)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "discovery.k8s.io", Version: "v1", Kind: "EndpointSlice", Name: "endpointslices", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	return dyn
}

func serviceEndpointSlicesRequest(namespace, name string, user *auth.User) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("namespace", namespace)
	rctx.URLParams.Add("name", name)
	req := requestWithUser(http.MethodGet, "/api/services/"+namespace+"/"+name+"/endpointslices", user)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func decodeServiceEndpointSlices(t *testing.T, rec *httptest.ResponseRecorder) serviceEndpointSlicesResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var body serviceEndpointSlicesResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestServiceEndpointSlicesSelectsByServiceLabelWithLimit(t *testing.T) {
	dyn := installEndpointSliceClient(t,
		testEndpointSlice("team", "web-abc", map[string]any{"kubernetes.io/service-name": "web"}),
		testEndpointSlice("team", "web-def", map[string]any{"kubernetes.io/service-name": "web"}),
		testEndpointSlice("team", "api-abc", map[string]any{"kubernetes.io/service-name": "api"}),
		testEndpointSlice("team", "unlabeled", nil),
		testEndpointSlice("other", "web-other", map[string]any{"kubernetes.io/service-name": "web"}),
	)

	rec := httptest.NewRecorder()
	testServerSrv.handleServiceEndpointSlices(rec, serviceEndpointSlicesRequest("team", "web", nil))
	body := decodeServiceEndpointSlices(t, rec)

	names := map[string]bool{}
	for _, item := range body.Items {
		names[item.GetNamespace()+"/"+item.GetName()] = true
	}
	if len(names) != 2 || !names["team/web-abc"] || !names["team/web-def"] {
		t.Fatalf("items = %v, want only team/web-abc and team/web-def", names)
	}
	if body.Truncated {
		t.Fatal("truncated = true for a complete page")
	}

	var listed bool
	for _, action := range dyn.Actions() {
		list, ok := action.(k8stesting.ListActionImpl)
		if !ok || list.GetResource() != testEndpointSliceGVR {
			continue
		}
		listed = true
		if list.GetNamespace() != "team" {
			t.Fatalf("listed namespace %q, want team", list.GetNamespace())
		}
		if list.ListOptions.LabelSelector != "kubernetes.io/service-name=web" {
			t.Fatalf("label selector = %q", list.ListOptions.LabelSelector)
		}
		if list.ListOptions.Limit != serviceEndpointSliceLimit {
			t.Fatalf("limit = %d, want %d", list.ListOptions.Limit, serviceEndpointSliceLimit)
		}
	}
	if !listed {
		t.Fatal("no EndpointSlice list was issued")
	}
}

func TestServiceEndpointSlicesReportsTruncation(t *testing.T) {
	dyn := installEndpointSliceClient(t)
	dyn.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{
			*testEndpointSlice("team", "web-abc", map[string]any{"kubernetes.io/service-name": "web"}),
		}}
		list.SetContinue("more")
		return true, list, nil
	})

	rec := httptest.NewRecorder()
	testServerSrv.handleServiceEndpointSlices(rec, serviceEndpointSlicesRequest("team", "web", nil))
	body := decodeServiceEndpointSlices(t, rec)
	if !body.Truncated || len(body.Items) != 1 {
		t.Fatalf("truncated = %v, items = %d; want truncated with 1 item", body.Truncated, len(body.Items))
	}
}

func TestServiceEndpointSlicesEmptyIsAnEmptyArray(t *testing.T) {
	installEndpointSliceClient(t)
	rec := httptest.NewRecorder()
	testServerSrv.handleServiceEndpointSlices(rec, serviceEndpointSlicesRequest("team", "web", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(raw["items"]) != "[]" {
		t.Fatalf("items = %s, want []", raw["items"])
	}
}

func TestServiceEndpointSlicesRejectsSelectorInjection(t *testing.T) {
	installEndpointSliceClient(t)
	rec := httptest.NewRecorder()
	testServerSrv.handleServiceEndpointSlices(rec, serviceEndpointSlicesRequest("team", "web,app=x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestServiceEndpointSlicesDeniesNamespaceOutsideUserAccess(t *testing.T) {
	installEndpointSliceClient(t,
		testEndpointSlice("team", "web-abc", map[string]any{"kubernetes.io/service-name": "web"}),
	)
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "alice"}
	s.permCache.Set(user.Username, nil, &auth.UserPermissions{AllowedNamespaces: []string{"other"}})

	rec := httptest.NewRecorder()
	s.handleServiceEndpointSlices(rec, serviceEndpointSlicesRequest("team", "web", user))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["error"] == "" {
		t.Fatalf("want {\"error\": ...}, got %v (decode err %v)", body, err)
	}

	s.permCache.Set(user.Username, nil, &auth.UserPermissions{AllowedNamespaces: []string{"team"}})
	rec = httptest.NewRecorder()
	s.handleServiceEndpointSlices(rec, serviceEndpointSlicesRequest("team", "web", user))
	if got := decodeServiceEndpointSlices(t, rec); len(got.Items) != 1 {
		t.Fatalf("allowed namespace returned %d items, want 1", len(got.Items))
	}
}
