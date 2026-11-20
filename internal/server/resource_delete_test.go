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
		kind      string
		deleteErr error
		status    int
		state     k8s.ConnectionState
	}{
		{name: "accepted with pending finalizers", status: http.StatusOK},
		{name: "unknown kind", kind: "unknownkind", status: http.StatusBadRequest},
		{name: "connecting with a client", state: k8s.StateConnecting, status: http.StatusServiceUnavailable},
		{name: "disconnected with a client", state: k8s.StateDisconnected, status: http.StatusServiceUnavailable},
		{name: "forbidden", deleteErr: apierrors.NewForbidden(gvr.GroupResource(), "held", fmt.Errorf("denied")), status: http.StatusForbidden},
		{name: "not found", deleteErr: apierrors.NewNotFound(gvr.GroupResource(), "held"), status: http.StatusNotFound},
		{name: "invalid", deleteErr: apierrors.NewBadRequest("invalid deletion"), status: http.StatusBadRequest},
		{name: "throttled", deleteErr: apierrors.NewTooManyRequests("slow down", 1), status: http.StatusTooManyRequests},
		{name: "unsupported", deleteErr: apierrors.NewMethodNotSupported(gvr.GroupResource(), "delete"), status: http.StatusMethodNotAllowed},
		{name: "request timeout", deleteErr: &apierrors.StatusError{ErrStatus: metav1.Status{Code: http.StatusRequestTimeout, Reason: metav1.StatusReasonTimeout}}, status: http.StatusRequestTimeout},
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
			previous := k8s.GetConnectionStatus()
			state := tc.state
			if state == "" {
				state = k8s.StateConnected
			}
			k8s.SetConnectionStatus(k8s.ConnectionStatus{State: state})
			t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
			router := chi.NewRouter()
			s := &Server{}
			router.Delete("/api/resources/{kind}/{namespace}/{name}", s.handleDeleteResource)
			res := httptest.NewRecorder()
			kind := tc.kind
			if kind == "" {
				kind = "configmaps"
			}
			router.ServeHTTP(res, httptest.NewRequest(http.MethodDelete, "/api/resources/"+kind+"/demo/held", nil))
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
			if (tc.kind != "" || tc.state != "") && len(dyn.Actions()) != 0 {
				t.Fatal("refused deletion reached the Kubernetes client")
			}
			for _, a := range dyn.Actions() {
				if a.GetVerb() == "patch" {
					t.Fatal("normal delete patched finalizers")
				}
			}
		})
	}
}

// The reviewed UID and resourceVersion reach the API server as preconditions;
// a conflict (the object changed since review) is 409, and force can't be bound.
func TestDeleteResourceHandlerBindsToTheReviewedObject(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "held", "namespace": "demo", "uid": "original", "resourceVersion": "9"}}}
	for _, tc := range []struct {
		name, query string
		deleteErr   error
		status      int
		wantRV      string
		wantMessage string
	}{
		{name: "bound", query: "?uid=original&resourceVersion=8", status: http.StatusOK, wantRV: "8"},
		{name: "changed since review", query: "?uid=original&resourceVersion=8", deleteErr: apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "held", fmt.Errorf("Precondition failed: ResourceVersion in precondition: 8, ResourceVersion in object meta: 9")), status: http.StatusConflict, wantRV: "8", wantMessage: "changed since you reviewed it"},
		{name: "a webhook quoting the phrase keeps its own words", query: "?uid=original&resourceVersion=8", deleteErr: apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "held", fmt.Errorf("Precondition failed: the change window is closed")), status: http.StatusConflict, wantRV: "8", wantMessage: "the change window is closed"},
		{name: "a webhook's conflict keeps its own words", query: "?uid=original&resourceVersion=8", deleteErr: apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "held", fmt.Errorf("policy holds this object")), status: http.StatusConflict, wantRV: "8", wantMessage: "policy holds this object"},
		{name: "force can't be bound", query: "?force=true&resourceVersion=8", status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj.DeepCopy())
			var got *metav1.Preconditions
			dyn.PrependReactor("delete", "configmaps", func(a clienttesting.Action) (bool, runtime.Object, error) {
				got = a.(clienttesting.DeleteActionImpl).DeleteOptions.Preconditions
				return true, nil, tc.deleteErr
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Version: "v1", Kind: "ConfigMap", Name: "configmaps", Namespaced: true}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			previous := k8s.GetConnectionStatus()
			k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
			t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
			router := chi.NewRouter()
			s := &Server{}
			router.Delete("/api/resources/{kind}/{namespace}/{name}", s.handleDeleteResource)
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(http.MethodDelete, "/api/resources/configmaps/demo/held"+tc.query, nil))
			if res.Code != tc.status {
				t.Fatalf("status %d: %s", res.Code, res.Body.String())
			}
			if tc.wantMessage != "" && !strings.Contains(res.Body.String(), tc.wantMessage) {
				t.Errorf("body %s, want %q", res.Body.String(), tc.wantMessage)
			}
			if tc.wantRV != "" && (got == nil || got.ResourceVersion == nil || *got.ResourceVersion != tc.wantRV || got.UID == nil || *got.UID != "original") {
				t.Fatalf("preconditions %+v", got)
			}
		})
	}
}
