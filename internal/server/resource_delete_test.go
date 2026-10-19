package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestDeleteResourceHandler(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	for _, tc := range []struct {
		name      string
		deleteErr error
		status    int
	}{
		{name: "accepted with pending finalizers", status: http.StatusOK},
		{name: "forbidden", deleteErr: apierrors.NewForbidden(gvr.GroupResource(), "held", fmt.Errorf("denied")), status: http.StatusForbidden},
		{name: "not found", deleteErr: apierrors.NewNotFound(gvr.GroupResource(), "held"), status: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "held", "namespace": "demo", "uid": "original"}}}
			now := metav1.Now()
			obj.SetDeletionTimestamp(&now)
			obj.SetFinalizers([]string{"example.com/cleanup"})
			dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
			dyn.PrependReactor("delete", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, tc.deleteErr })
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Version: "v1", Kind: "ConfigMap", Name: "configmaps", Namespaced: true}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			router := chi.NewRouter()
			s := &Server{}
			router.Delete("/api/resources/{kind}/{namespace}/{name}", s.handleDeleteResource)
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(http.MethodDelete, "/api/resources/configmaps/demo/held", nil))
			if res.Code != tc.status {
				t.Fatalf("status %d: %s", res.Code, res.Body.String())
			}
			if tc.status == http.StatusOK {
				var result k8score.DeleteResourceResult
				if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.DeletionTimestamp == nil || len(result.PendingFinalizers) != 1 {
					t.Fatalf("missing pending observation: %+v", result)
				}
			}
			for _, a := range dyn.Actions() {
				if a.GetVerb() == "patch" {
					t.Fatal("normal delete patched finalizers")
				}
			}
		})
	}
}

func TestDeleteResourceHandlerFinalizerConsent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		query       string
		status      int
		wantRemoved bool
		patchErr    error
		rejectPatch error
		wantPatched bool
	}{
		{name: "consent for the shown list and uid", query: "?force=true&uid=original&removeFinalizers=example.com/b&removeFinalizers=example.com/a", status: http.StatusOK, wantRemoved: true},
		{name: "consent for a stale list", query: "?force=true&uid=original&removeFinalizers=example.com/a", status: http.StatusConflict},
		{name: "consent for a replaced object", query: "?force=true&uid=shown-earlier&removeFinalizers=example.com/a&removeFinalizers=example.com/b", status: http.StatusConflict},
		{name: "consent without a uid", query: "?force=true&removeFinalizers=example.com/a&removeFinalizers=example.com/b", status: http.StatusBadRequest},
		{name: "force without consent", query: "?force=true&uid=original", status: http.StatusOK},
		{name: "patch forbidden, finalizers still held", query: "?force=true&uid=original&removeFinalizers=example.com/a&removeFinalizers=example.com/b", status: http.StatusForbidden, rejectPatch: apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "held", fmt.Errorf("denied")), wantPatched: true},
		{name: "removal outcome unknown", query: "?force=true&uid=original&removeFinalizers=example.com/a&removeFinalizers=example.com/b", status: http.StatusInternalServerError, patchErr: apierrors.NewTimeoutError("patch response lost", 1), wantPatched: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "held", "namespace": "demo", "uid": "original"}}}
			now := metav1.Now()
			obj.SetDeletionTimestamp(&now)
			obj.SetFinalizers([]string{"example.com/a", "example.com/b"})
			dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
			dyn.PrependReactor("delete", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
			if tc.patchErr != nil {
				dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, tc.patchErr })
			}
			if tc.rejectPatch != nil {
				dyn.PrependReactor("patch", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, tc.rejectPatch })
			}
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Version: "v1", Kind: "ConfigMap", Name: "configmaps", Namespaced: true}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			router := chi.NewRouter()
			s := &Server{}
			router.Delete("/api/resources/{kind}/{namespace}/{name}", s.handleDeleteResource)
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(http.MethodDelete, "/api/resources/configmaps/demo/held"+tc.query, nil))
			if res.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", res.Code, tc.status, res.Body.String())
			}
			patched := false
			for _, a := range dyn.Actions() {
				if a.GetVerb() == "patch" {
					patched = true
				}
			}
			if patched != (tc.wantRemoved || tc.wantPatched) {
				t.Fatalf("patched=%v, want %v", patched, tc.wantRemoved || tc.wantPatched)
			}
			// A consented request's definite failures carry a code, so the UI
			// can tell them from an answer that isn't Radar's.
			if consented := strings.Contains(tc.query, "removeFinalizers"); consented && tc.status != http.StatusOK && tc.patchErr == nil {
				var body map[string]string
				if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["error_code"] != "finalizers_not_removed" {
					t.Fatalf("definite failure not marked: %s", res.Body.String())
				}
			}
			if tc.patchErr != nil {
				var body map[string]string
				if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				// The web client strips exactly this prefix before showing the cause.
				if !strings.HasPrefix(body["error"], "couldn't confirm whether the finalizers were removed. Check the resource before trying again: ") || body["error_code"] != "finalizer_removal_unknown" {
					t.Fatalf("unknown outcome not reported: %s", res.Body.String())
				}
			}
			if tc.wantRemoved {
				var result k8score.DeleteResourceResult
				if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.RemovedFinalizers) != 2 {
					t.Fatalf("removed finalizers not reported: %s", res.Body.String())
				}
			}
		})
	}
}
