package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "held", "namespace": "demo"}}}
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
